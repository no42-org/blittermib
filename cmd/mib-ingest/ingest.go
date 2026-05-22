package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/mibcorpus"
)

// outcome encodes what happened to a single upload-folder file.
//
// outcomeUnknown is the zero value so a `result{}` literal that
// forgets to set the field doesn't accidentally classify as
// "moved" — defensive default flagged in the post-merge review.
//
// outcomeSkippedNonMIB and outcomeParseError both leave the file in
// upload/, but only the latter is an actionable failure. A file
// without the SMI lexical marker is a non-MIB the operator dropped
// alongside their actual MIBs (READMEs, partial downloads); a file
// with the marker that smidump rejected is a real parse error worth
// surfacing.
type outcome int

const (
	outcomeUnknown        outcome = iota
	outcomeMoved                  // moved to canonical destination
	outcomeRoutedUnsorted         // low confidence → mibs/unsorted/
	outcomeRefused                // destination already exists
	outcomeSkippedNonMIB          // no MIB marker — expected non-MIB file
	outcomeParseError             // had marker but smidump rejected
)

type result struct {
	src     string
	dst     string // repo-relative path under <root>; e.g. "mibs/vendors/9-cisco/CISCO-FOO-MIB"
	outcome outcome
	conf    mibcorpus.Confidence
	reason  string
	// sha is the lowercase hex sha256 of the source file's raw
	// bytes. Empty when the file could not be opened or read.
	// Populated during the readability + lexical-marker pre-check
	// so broken-but-readable files still participate in dedup
	// detection.
	sha string
	// size is the source file's byte length. Zero only when the
	// file is unreadable (in which case sha is also empty); a
	// 0-byte readable file legitimately has size=0 and a
	// non-empty sha (the sha256 of empty input).
	size int64
	// moduleName, oidRoot, and lastUpdated are populated only for
	// successfully-parsed results (outcomeMoved / outcomeRoutedUnsorted).
	// Empty on parse-error and non-MIB-skip results. Carried on
	// `result` so the report-mode grouping passes can work off the
	// same per-file record without re-parsing.
	moduleName  string
	oidRoot     string
	lastUpdated string
}

func ingestCmd(args []string) error {
	flags := flag.NewFlagSet("blittermib-ingest", flag.ContinueOnError)
	src := flags.String("src", "mibs/upload", "drop directory to walk")
	root := flags.String("root", ".", "repository root (corpus lives at <root>/mibs/)")
	groupsPath := flags.String("groups", "mibs/_groups.yaml", "IETF groups map (read-only; missing OK)")
	smidump := flags.String("smidump", "smidump", "smidump binary path")
	smilint := flags.String("smilint", "smilint", "smilint binary path; pass '' to skip")
	dryRun := flags.Bool("dry-run", false, "print planned moves without touching files")
	gitAdd := flags.Bool("git-add", false, "after a successful move, run `git add <dst>`")
	noIndex := flags.Bool("no-index", false, "skip the post-ingest `make index` step")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if info, err := os.Stat(*src); err != nil {
		return fmt.Errorf("--src: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("--src must be a directory, got %s", *src)
	}

	// Resolve the groups path under --root. No silent fallback — a
	// malformed YAML at the root-relative path used to get retried
	// at the bare path, masking the real error. If you need a
	// different path, pass `--groups <path>`.
	groups, err := mibcorpus.LoadGroups(filepath.Join(*root, *groupsPath))
	if err != nil {
		return fmt.Errorf("load groups: %w", err)
	}

	files, err := walkUpload(*src)
	if err != nil {
		return fmt.Errorf("walk %s: %w", *src, err)
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "ingest: no MIB-shaped files in %s\n", *src)
		return nil
	}

	results, parseErrors := classifyFiles(*smidump, *smilint, *src, *root, files, groups)
	results = append(results, parseErrors...)
	moves, refusedCount, skippedNonMIB, parseErrorCount := planMoves(results, *root)

	if *dryRun {
		printDryRun(os.Stdout, moves)
		return nil
	}

	movedCount, refusedAtMove, gitAddFailures, err := applyMoves(moves, *root, *gitAdd)
	if err != nil {
		printSummary(os.Stdout, moves, movedCount, refusedCount+refusedAtMove, skippedNonMIB, parseErrorCount, gitAddFailures)
		return err
	}

	if !*noIndex && movedCount > 0 {
		if err := runMakeIndex(*root); err != nil {
			fmt.Fprintf(os.Stderr, "ingest: make index failed: %v\n", err)
			// Continue to summary — the moves still happened.
		}
	}

	printSummary(os.Stdout, moves, movedCount, refusedCount+refusedAtMove, skippedNonMIB, parseErrorCount, gitAddFailures)

	// Non-zero exit only on actionable failures. Non-MIB files
	// dropped in upload/ are EXPECTED collateral when an operator
	// drops a vendor's archive (READMEs, LICENSEs, partial files);
	// they don't fail the run. Refusals, parse errors, and
	// `git add` failures DO.
	totalRefused := refusedCount + refusedAtMove
	if totalRefused > 0 || parseErrorCount > 0 || gitAddFailures > 0 {
		return fmt.Errorf("%d refused, %d parse errors, %d git-add failures",
			totalRefused, parseErrorCount, gitAddFailures)
	}
	return nil
}

// walkUpload returns the MIB-shaped files in dir (single level —
// the drop folder isn't expected to be nested). Filename
// heuristics: `.mib`, `.txt`, `.my`, or no extension. Hidden files
// and the `.gitkeep` placeholder are skipped, as are symlinks and
// irregular file types.
func walkUpload(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("walk error; skipping", "path", path, "err", err)
			return nil
		}
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if d.Type()&(fs.ModeSymlink|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeDevice|fs.ModeIrregular) != 0 {
			return nil
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".mib", ".txt", ".my", "":
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// hashFile streams the file at path through sha256 and returns the
// lowercase-hex digest plus the byte length. Streaming via io.Copy
// keeps memory bounded for large MIBs. Errors from Open or read
// propagate so callers can surface the file as a "non-mib" finding
// without a hash.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// classifyFiles runs the lexical-marker check + libsmi parse +
// mibcorpus.Classify pipeline for every input file. Files that fail
// the marker check or libsmi parse are returned as parseErrors
// with outcome=outcomeLeftInUpload. Compile is bounded by a
// per-batch timeout so a hung smidump can't hang the ingest forever.
func classifyFiles(smidumpPath, smilintPath, srcDir, root string, files []string, groups mibcorpus.GroupMap) (parsed []result, parseErrors []result) {
	// Filter to only files that pass the lexical-marker check —
	// avoids feeding LICENSE / README / partial downloads to libsmi.
	// Hashing happens here too, so every readable file (including
	// marker-less ones and ones that later fail smidump) carries a
	// sha for downstream byte-identical detection. Files that can't
	// be opened skip hashing entirely and surface as non-MIB skips
	// without a sha.
	type fileMeta struct {
		sha  string
		size int64
	}
	hashes := make(map[string]fileMeta, len(files))
	var keep []string
	for _, f := range files {
		sha, size, err := hashFile(f)
		if err != nil {
			// Read failures are non-actionable for the operator — the
			// file is unreadable, but most vendor archives include a
			// few of these (broken symlinks, mode bits). Treat as
			// "non-MIB skipped" without a hash.
			parseErrors = append(parseErrors, result{
				src:     f,
				outcome: outcomeSkippedNonMIB,
				reason:  fmt.Sprintf("read failed: %v", err),
			})
			continue
		}
		hashes[f] = fileMeta{sha: sha, size: size}

		ok, err := mibcorpus.HasMIBOpener(f)
		if err != nil {
			// Hash already succeeded; if HasMIBOpener now hits an I/O
			// error the file went unreadable between the two reads
			// (rare). Surface as non-MIB skip but keep the hash so
			// dedup still works.
			parseErrors = append(parseErrors, result{
				src:     f,
				outcome: outcomeSkippedNonMIB,
				reason:  fmt.Sprintf("read failed: %v", err),
				sha:     sha,
				size:    size,
			})
			continue
		}
		if !ok {
			parseErrors = append(parseErrors, result{
				src:     f,
				outcome: outcomeSkippedNonMIB,
				reason:  "no MIB marker (DEFINITIONS ::= BEGIN absent in first 32 KB)",
				sha:     sha,
				size:    size,
			})
			continue
		}
		keep = append(keep, f)
	}

	if len(keep) == 0 {
		return parsed, parseErrors
	}

	c := &compile.Compiler{
		Smidump: &compile.Smidump{Path: smidumpPath, Paths: []string{srcDir}},
	}
	if smilintPath != "" {
		c.Smilint = &compile.Smilint{Path: smilintPath, Paths: []string{srcDir}}
	}

	// Bound the compile pipeline — a pathological MIB or hung
	// smidump shouldn't hang ingest indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results := c.Compile(ctx, keep)

	for _, r := range results {
		meta := hashes[r.Target] // zero value if absent; only happens for path mismatches
		if r.Err != nil || r.Module == nil || r.Module.Name == "" {
			// File had the marker but smidump rejected — actionable
			// parse error worth surfacing on the exit code. A missing
			// MODULE-IDENTITY OID is NOT counted here; mibcorpus.
			// Classify routes SMIv1 / TC-only modules by name.
			parseErrors = append(parseErrors, result{
				src:     r.Target,
				outcome: outcomeParseError,
				reason:  parseFailReason(r),
				sha:     meta.sha,
				size:    meta.size,
			})
			continue
		}
		if !mibcorpus.ValidModuleName.MatchString(r.Module.Name) {
			parseErrors = append(parseErrors, result{
				src:     r.Target,
				outcome: outcomeParseError,
				reason:  fmt.Sprintf("module name %q contains characters disallowed in a corpus filename", r.Module.Name),
				sha:     meta.sha,
				size:    meta.size,
			})
			continue
		}
		cls := mibcorpus.Classify(r.Module.OIDRoot, r.Module.Name, groups, nil)
		dst, err := classificationToDst(cls, r.Module.Name, r.Target)
		if err != nil {
			parseErrors = append(parseErrors, result{
				src:     r.Target,
				outcome: outcomeParseError,
				reason:  err.Error(),
				sha:     meta.sha,
				size:    meta.size,
			})
			continue
		}
		// Per-file warning for medium-confidence routes — the
		// PEN isn't in the curated registry. Operator should
		// either confirm `vendors/{PEN}-unknown/` is OK or pin
		// a slug via `make refresh-pen` + an explicit override.
		if cls.Confidence == mibcorpus.ConfidenceMedium {
			fmt.Fprintf(os.Stderr,
				"[medium] %s → %s (PEN %d not in curated registry)\n",
				r.Target, dst, cls.PEN)
		}
		parsed = append(parsed, result{
			src:         r.Target,
			conf:        cls.Confidence,
			dst:         dst,
			sha:         meta.sha,
			size:        meta.size,
			moduleName:  r.Module.Name,
			oidRoot:     r.Module.OIDRoot,
			lastUpdated: r.Module.LastUpdated,
		})
	}
	_ = root // kept on the signature for future containment checks
	return parsed, parseErrors
}

func parseFailReason(r compile.Result) string {
	if r.Err != nil {
		return fmt.Sprintf("smidump failed: %v", r.Err)
	}
	if r.Module == nil {
		return "smidump produced no module"
	}
	if r.Module.Name == "" {
		return "smidump produced empty module name"
	}
	return "smidump rejected module"
}

// classificationToDst returns the repo-relative destination path
// for a given Classification.
//
//   - High / medium → mibs/<DstDir>/<MODULE-NAME> (extension stripped).
//   - Low           → mibs/unsorted/<original-filename>.
//
// Defense-in-depth: rejects any destination that escapes
// `<root>/mibs/` via `..` or absolute path. Today
// `iana.Slug` and `ValidModuleName` make traversal impossible by
// construction, but the gate closes the surface entirely.
func classificationToDst(cls mibcorpus.Classification, moduleName, srcPath string) (string, error) {
	var dst string
	if cls.Confidence == mibcorpus.ConfidenceLow {
		dst = filepath.Join("mibs", "unsorted", filepath.Base(srcPath))
	} else {
		dst = filepath.Join("mibs", cls.DstDir, moduleName)
	}
	if !filepath.IsLocal(dst) {
		return "", fmt.Errorf("computed destination escapes corpus root: %s", dst)
	}
	return dst, nil
}

// planMoves transforms the classifyFiles output into a list of
// moves (with destination conflicts pre-checked against the
// existing corpus state on disk). Returns the move list, count of
// refusals, count of files left in upload because they aren't MIBs
// (expected; non-actionable), and count of files that hit a real
// parse error (actionable).
func planMoves(results []result, root string) (moves []result, refusedCount, skippedNonMIBCount, parseErrorCount int) {
	for _, r := range results {
		switch r.outcome {
		case outcomeSkippedNonMIB:
			skippedNonMIBCount++
			// Don't spam stderr for the non-MIB case — the summary
			// reports the count, and per-file logs would drown the
			// useful output when an operator drops a vendor archive
			// with hundreds of READMEs.
			moves = append(moves, r)
			continue
		case outcomeParseError:
			parseErrorCount++
			fmt.Fprintf(os.Stderr, "[parse-error] %s — %s\n", r.src, r.reason)
			moves = append(moves, r)
			continue
		}
		// At this point r is a successful classification.
		if r.conf == mibcorpus.ConfidenceLow {
			r.outcome = outcomeRoutedUnsorted
		} else {
			r.outcome = outcomeMoved
		}
		// Refuse if destination already exists.
		fullDst := filepath.Join(root, r.dst)
		if _, err := os.Lstat(fullDst); err == nil {
			r.outcome = outcomeRefused
			r.reason = fmt.Sprintf("destination already exists: %s", r.dst)
			fmt.Fprintf(os.Stderr, "[refuse] %s → %s (%s)\n", r.src, r.dst, r.reason)
			refusedCount++
		}
		moves = append(moves, r)
	}
	return moves, refusedCount, skippedNonMIBCount, parseErrorCount
}

// applyMoves runs the planned moves. Returns the count of files
// successfully moved, the count refused at rename-time (extra
// TOCTOU layer), the count of `git add` failures (only relevant
// when `--git-add` is set), and any fatal error. `root` scopes the
// rename + git-add to the intended repository regardless of the
// caller's CWD.
func applyMoves(moves []result, root string, gitAdd bool) (moved, refusedAtMove, gitAddFailures int, err error) {
	for i, r := range moves {
		switch r.outcome {
		case outcomeMoved, outcomeRoutedUnsorted:
			fullDst := filepath.Join(root, r.dst)
			// Re-check for late-arriving conflicts (a parallel
			// process / earlier file in this same run could have
			// created the destination).
			if _, statErr := os.Lstat(fullDst); statErr == nil {
				moves[i].outcome = outcomeRefused
				moves[i].reason = fmt.Sprintf("destination already exists: %s", r.dst)
				fmt.Fprintf(os.Stderr, "[refuse] %s → %s (%s)\n", r.src, r.dst, moves[i].reason)
				refusedAtMove++
				continue
			}
			if mkErr := os.MkdirAll(filepath.Dir(fullDst), 0o755); mkErr != nil {
				return moved, refusedAtMove, gitAddFailures, fmt.Errorf("mkdir %s: %w", filepath.Dir(fullDst), mkErr)
			}
			if rnErr := os.Rename(r.src, fullDst); rnErr != nil {
				return moved, refusedAtMove, gitAddFailures, fmt.Errorf("rename %s → %s: %w", r.src, fullDst, rnErr)
			}
			moved++
			if gitAdd {
				rel, relErr := filepath.Rel(root, fullDst)
				if relErr != nil {
					rel = r.dst
				}
				cmd := exec.Command("git", "add", "--", rel)
				cmd.Dir = root
				cmd.Stderr = os.Stderr
				if gitErr := cmd.Run(); gitErr != nil {
					gitAddFailures++
					fmt.Fprintf(os.Stderr, "[git-add-fail] %s: %v\n", rel, gitErr)
				}
			}
		}
	}
	return moved, refusedAtMove, gitAddFailures, nil
}

// runMakeIndex shells out to `make index` from the given root.
// Preflights both `make` (must be on PATH) and the existence of a
// Makefile in `root` so the failure mode is a clear message
// instead of a cryptic exec error.
func runMakeIndex(root string) error {
	if _, err := exec.LookPath("make"); err != nil {
		return fmt.Errorf("`make` not on PATH; skip with --no-index or install make")
	}
	if _, err := os.Stat(filepath.Join(root, "Makefile")); err != nil {
		return fmt.Errorf("no Makefile at %s; skip with --no-index", root)
	}
	cmd := exec.Command("make", "index")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func printDryRun(w io.Writer, moves []result) {
	for _, r := range moves {
		switch r.outcome {
		case outcomeMoved, outcomeRoutedUnsorted:
			fmt.Fprintf(w, "  [%-6s] %s → %s\n", r.conf, r.src, r.dst)
		case outcomeRefused:
			fmt.Fprintf(w, "  [refuse]      %s — %s\n", r.src, r.reason)
		case outcomeParseError:
			fmt.Fprintf(w, "  [parse-error] %s — %s\n", r.src, r.reason)
		case outcomeSkippedNonMIB:
			fmt.Fprintf(w, "  [non-mib]     %s — %s\n", r.src, r.reason)
		}
	}
	fmt.Fprintln(w, "(dry-run; no files moved, no INDEX.yaml regen)")
}

// summaryListMax bounds the per-file list of leftover files printed
// after the summary line. A vendor archive can drop hundreds of
// READMEs / Makefiles into upload/; truncating at 20 keeps the
// terminal usable while still answering the common "what are the
// 5 files still sitting in upload?" question for a small drop.
const summaryListMax = 20

func printSummary(w io.Writer, moves []result, moved, refused, skippedNonMIB, parseErrors, gitAddFailures int) {
	var routedUnsorted int
	for _, r := range moves {
		if r.outcome == outcomeRoutedUnsorted {
			routedUnsorted++
		}
	}
	highMedium := moved - routedUnsorted
	if highMedium < 0 {
		highMedium = 0
	}
	fmt.Fprintf(w,
		"ingest: %d moved (%d high/medium → corpus, %d low → unsorted), %d refused, %d non-mib skipped, %d parse errors",
		moved, highMedium, routedUnsorted, refused, skippedNonMIB, parseErrors)
	if gitAddFailures > 0 {
		fmt.Fprintf(w, ", %d git-add failures", gitAddFailures)
	}
	fmt.Fprintln(w)

	// Final per-file rundown of anything still sitting in upload/.
	// This is exactly the set the operator needs to act on (delete,
	// re-classify, or fix the source MIB).
	var leftover []result
	for _, r := range moves {
		switch r.outcome {
		case outcomeSkippedNonMIB, outcomeParseError, outcomeRefused:
			leftover = append(leftover, r)
		}
	}
	if len(leftover) == 0 {
		return
	}
	fmt.Fprintln(w, "left in upload/:")
	for i, r := range leftover {
		if i == summaryListMax {
			fmt.Fprintf(w, "  ...and %d more (use --dry-run to see the full list)\n",
				len(leftover)-summaryListMax)
			break
		}
		var tag string
		switch r.outcome {
		case outcomeSkippedNonMIB:
			tag = "non-mib"
		case outcomeParseError:
			tag = "parse-error"
		case outcomeRefused:
			tag = "refuse"
		}
		fmt.Fprintf(w, "  [%-11s] %s — %s\n", tag, r.src, r.reason)
	}
}
