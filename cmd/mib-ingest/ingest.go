package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/mibcorpus"
)

// definitionsBeginMarker mirrors the loader's lexical-marker gate so
// non-MIB files in the upload directory (LICENSE, README, partial
// downloads) are skipped without an expensive libsmi parse.
var definitionsBeginMarker = []byte("DEFINITIONS ::= BEGIN")

// validModuleName is the conservative character set we accept for a
// MODULE-IDENTITY name when synthesising a destination filename. Same
// rule the migrate tool uses — rejects path separators / `..` / shell-
// active characters.
var validModuleName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// outcome encodes what happened to a single upload-folder file.
type outcome int

const (
	outcomeMoved          outcome = iota // moved to canonical destination
	outcomeRoutedUnsorted                // low confidence → mibs/unsorted/
	outcomeRefused                       // destination already exists
	outcomeLeftInUpload                  // parse failed / no marker / etc.
)

type result struct {
	src     string
	dst     string
	outcome outcome
	conf    mibcorpus.Confidence
	reason  string
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

	groups, err := mibcorpus.LoadGroups(filepath.Join(*root, *groupsPath))
	if err != nil {
		// Try the path as given (in case --root is "." and groupsPath
		// is already relative to caller's cwd).
		groups, err = mibcorpus.LoadGroups(*groupsPath)
		if err != nil {
			return fmt.Errorf("load groups: %w", err)
		}
	}

	files, err := walkUpload(*src)
	if err != nil {
		return fmt.Errorf("walk %s: %w", *src, err)
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "ingest: no MIB-shaped files in %s\n", *src)
		return nil
	}

	results, parseErrors := classifyFiles(*smidump, *smilint, *src, files, groups)
	results = append(results, parseErrors...)
	moves, refusedCount, leftCount := planMoves(results, *root)

	if *dryRun {
		printDryRun(os.Stdout, moves, refusedCount, leftCount)
		return nil
	}

	movedCount, refusedAtMove, err := applyMoves(moves, *root, *gitAdd)
	if err != nil {
		// Even on partial failure, surface the summary before
		// returning.
		printSummary(os.Stdout, moves, movedCount, refusedCount+refusedAtMove, leftCount)
		return err
	}

	if !*noIndex && movedCount > 0 {
		if err := runMakeIndex(*root); err != nil {
			fmt.Fprintf(os.Stderr, "ingest: make index failed: %v\n", err)
			// Continue to summary — the moves still happened.
		}
	}

	printSummary(os.Stdout, moves, movedCount, refusedCount+refusedAtMove, leftCount)

	totalRefused := refusedCount + refusedAtMove
	if totalRefused > 0 || leftCount > 0 {
		// Non-zero exit if anything didn't make it through cleanly,
		// so CI / scripted runs detect partial-success.
		return fmt.Errorf("%d refused, %d left in upload", totalRefused, leftCount)
	}
	return nil
}

// walkUpload returns the MIB-shaped files in dir (single level — the
// drop folder isn't expected to be nested). Filename heuristics:
// `.mib`, `.txt`, `.my`, or no extension. Hidden files and the
// `.gitkeep` placeholder are skipped, as are symlinks and irregular
// file types.
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

// classifyFiles runs the lexical-marker check + libsmi parse +
// mibcorpus.Classify pipeline for every input file. Files that fail
// the marker check or libsmi parse are returned as parseErrors with
// outcome=outcomeLeftInUpload.
func classifyFiles(smidumpPath, smilintPath, srcDir string, files []string, groups mibcorpus.GroupMap) (parsed []result, parseErrors []result) {
	// Filter to only files that pass the lexical-marker check —
	// avoids feeding LICENSE / README / partial downloads to libsmi.
	var keep []string
	for _, f := range files {
		ok, err := hasMIBOpener(f)
		if err != nil {
			parseErrors = append(parseErrors, result{
				src:     f,
				outcome: outcomeLeftInUpload,
				reason:  fmt.Sprintf("read failed: %v", err),
			})
			continue
		}
		if !ok {
			parseErrors = append(parseErrors, result{
				src:     f,
				outcome: outcomeLeftInUpload,
				reason:  "no MIB marker (DEFINITIONS ::= BEGIN absent in first 32 KB)",
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
	results := c.Compile(context.Background(), keep)

	for _, r := range results {
		if r.Err != nil || r.Module == nil || r.Module.Name == "" || r.Module.OIDRoot == "" {
			parseErrors = append(parseErrors, result{
				src:     r.Target,
				outcome: outcomeLeftInUpload,
				reason:  parseFailReason(r),
			})
			continue
		}
		if !validModuleName.MatchString(r.Module.Name) {
			parseErrors = append(parseErrors, result{
				src:     r.Target,
				outcome: outcomeLeftInUpload,
				reason:  fmt.Sprintf("module name %q contains characters disallowed in a corpus filename", r.Module.Name),
			})
			continue
		}
		cls := mibcorpus.Classify(r.Module.OIDRoot, r.Module.Name, groups, nil)
		parsed = append(parsed, result{
			src:  r.Target,
			conf: cls.Confidence,
			dst:  classificationToDst(cls, r.Module.Name, r.Target),
		})
	}
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
	return "smidump produced incomplete module (no MODULE-IDENTITY OID)"
}

// classificationToDst returns the destination relpath under <root>
// for a given Classification.
//
//   - High / medium → mibs/<DstDir>/<MODULE-NAME> (extension stripped).
//   - Low           → mibs/unsorted/<original-filename>.
func classificationToDst(cls mibcorpus.Classification, moduleName, srcPath string) string {
	if cls.Confidence == mibcorpus.ConfidenceLow {
		return filepath.Join("mibs", "unsorted", filepath.Base(srcPath))
	}
	return filepath.Join("mibs", cls.DstDir, moduleName)
}

// planMoves transforms the classifyFiles output into a list of moves
// (with destination conflicts pre-checked against the existing corpus
// state on disk). Returns the move list, count of refusals, count of
// left-in-upload entries.
func planMoves(results []result, root string) (moves []result, refusedCount int, leftInUploadCount int) {
	for _, r := range results {
		if r.outcome == outcomeLeftInUpload {
			leftInUploadCount++
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
			refusedCount++
		}
		moves = append(moves, r)
	}
	return moves, refusedCount, leftInUploadCount
}

// applyMoves runs the planned moves. Returns the number of files
// successfully moved, the number that turned out to be refused at
// rename-time (extra TOCTOU layer), and any fatal error. `root`
// scopes the optional `git add` so it operates on the intended
// repository regardless of the caller's CWD.
func applyMoves(moves []result, root string, gitAdd bool) (moved int, refusedAtMove int, err error) {
	for i, r := range moves {
		switch r.outcome {
		case outcomeMoved, outcomeRoutedUnsorted:
			fullDst := r.dst
			// Re-check for late-arriving conflicts (a parallel
			// process / earlier file in this same run could have
			// created the destination).
			if _, statErr := os.Lstat(fullDst); statErr == nil {
				moves[i].outcome = outcomeRefused
				moves[i].reason = fmt.Sprintf("destination already exists: %s", r.dst)
				refusedAtMove++
				continue
			}
			if mkErr := os.MkdirAll(filepath.Dir(fullDst), 0o755); mkErr != nil {
				return moved, refusedAtMove, fmt.Errorf("mkdir %s: %w", filepath.Dir(fullDst), mkErr)
			}
			if rnErr := os.Rename(r.src, fullDst); rnErr != nil {
				return moved, refusedAtMove, fmt.Errorf("rename %s → %s: %w", r.src, fullDst, rnErr)
			}
			moved++
			if gitAdd {
				// Pass a repo-relative path so `git add` works
				// regardless of the caller's cwd (the test sets
				// up its own git repo under root).
				rel, relErr := filepath.Rel(root, fullDst)
				if relErr != nil {
					rel = fullDst
				}
				cmd := exec.Command("git", "add", "--", rel)
				cmd.Dir = root
				cmd.Stderr = os.Stderr
				if gitErr := cmd.Run(); gitErr != nil {
					fmt.Fprintf(os.Stderr, "git add %s: %v\n", rel, gitErr)
				}
			}
		}
	}
	return moved, refusedAtMove, nil
}

// runMakeIndex shells out to `make index` from the given root. Keeps
// mibs/INDEX.yaml the canonical post-ingest source of truth.
func runMakeIndex(root string) error {
	cmd := exec.Command("make", "index")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func printDryRun(w io.Writer, moves []result, refused, leftInUpload int) {
	for _, r := range moves {
		switch r.outcome {
		case outcomeMoved, outcomeRoutedUnsorted:
			fmt.Fprintf(w, "  [%-6s] %s → %s\n", r.conf, r.src, r.dst)
		case outcomeRefused:
			fmt.Fprintf(w, "  [refuse] %s — %s\n", r.src, r.reason)
		case outcomeLeftInUpload:
			fmt.Fprintf(w, "  [skip ] %s — %s\n", r.src, r.reason)
		}
	}
	fmt.Fprintln(w, "(dry-run; no files moved, no INDEX.yaml regen)")
}

func printSummary(w io.Writer, moves []result, moved, refused, leftInUpload int) {
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
	fmt.Fprintf(w, "ingest: %d moved (%d high/medium → corpus, %d low → unsorted), %d refused, %d left in upload\n",
		moved, highMedium, routedUnsorted, refused, leftInUpload)
}

// hasMIBOpener returns true when the first 32 KB of the file
// contains the `DEFINITIONS ::= BEGIN` marker. Mirrors the loader's
// bounded-read sniff.
func hasMIBOpener(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	const sniffBytes = 32 * 1024
	buf := make([]byte, sniffBytes+len(definitionsBeginMarker)-1)
	n, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return bytes.Contains(buf[:n], definitionsBeginMarker), nil
	}
	if err != nil {
		return false, err
	}
	return bytes.Contains(buf[:n], definitionsBeginMarker), nil
}
