package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// outcome encodes what happened to a single import-folder file.
//
// outcomeUnknown is the zero value so a `result{}` literal that
// forgets to set the field doesn't accidentally classify as
// "moved" — defensive default flagged in the post-merge review.
//
// outcomeSkippedNonMIB and outcomeParseError both leave the file in
// import/, but only the latter is an actionable failure. A file
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
	// outcomeBudgetExhausted marks files the compile bound cut off
	// before they were processed — incomplete work, not broken MIBs.
	// Reported as one rollup, never as per-file parse errors.
	outcomeBudgetExhausted
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
	src := flags.String("src", "mibs/import", "drop directory to walk")
	root := flags.String("root", ".", "repository root (corpus lives at <root>/mibs/)")
	groupsPath := flags.String("groups", "mibs/_groups.yaml", "IETF groups map (read-only; missing OK)")
	smidump := flags.String("smidump", "smidump", "smidump binary path")
	smilint := flags.String("smilint", "smilint", "smilint binary path; pass '' to skip")
	dryRun := flags.Bool("dry-run", false, "print planned moves without touching files")
	gitAdd := flags.Bool("git-add", false, "after a successful move, run `git add <dst>`")
	noIndex := flags.Bool("no-index", false, "skip the post-ingest `make index` step")
	report := flags.Bool("report", false, "run a read-only triage report against import/ instead of moving files")
	reportFormat := flags.String("report-format", "text", "report output format: text or json (only with --report)")
	autoCollapse := flags.Bool("auto-collapse-identical", false, "delete byte-identical duplicates from --src before classification (mutually exclusive with --report)")
	compileTimeout := flags.Duration("compile-timeout", 0, "compile-pass bound; 0 disables; unset = max(5m, 1s per file)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	// flag.Duration can't distinguish "unset" from an explicit `0`
	// (which means unbounded), so detect explicit use via Visit.
	compileTimeoutSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "compile-timeout" {
			compileTimeoutSet = true
		}
	})
	if *compileTimeout < 0 {
		return fmt.Errorf("--compile-timeout must be >= 0, got %v", *compileTimeout)
	}
	// Beyond ~1 year, time.Now().Add(timeout) overflows into the past
	// and the context is born expired — the whole batch would come
	// back budget-exhausted. The disable value is 0, not "huge".
	const maxCompileTimeout = 8760 * time.Hour
	if *compileTimeout > maxCompileTimeout {
		return fmt.Errorf("--compile-timeout %v exceeds the %v maximum; pass 0 to disable the bound", *compileTimeout, maxCompileTimeout)
	}
	// Only validate --report-format when --report is set; the flag
	// is a no-op without --report, so a stray --report-format=yaml
	// in a non-report invocation should be tolerated rather than
	// rejecting the entire run.
	if *report && *reportFormat != "text" && *reportFormat != "json" {
		return fmt.Errorf("--report-format must be 'text' or 'json', got %q", *reportFormat)
	}
	// --report is read-only; --auto-collapse-identical mutates --src.
	// Running both at once is ambiguous (does the report reflect
	// pre-collapse or post-collapse state?), so we refuse before
	// touching anything per the spec contract.
	if *report && *autoCollapse {
		return fmt.Errorf("--report and --auto-collapse-identical are mutually exclusive: --report is read-only, --auto-collapse-identical mutates --src")
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

	// --auto-collapse-identical: pre-pass that hashes every walked
	// file, groups by sha, keeps the lex-first source path per
	// group, and deletes the rest from --src. Under --dry-run the
	// deletion is suppressed but the count is still surfaced.
	// classifyFiles runs against the reduced set.
	if *autoCollapse {
		kept, collapsed, err := autoCollapseIdentical(files, *dryRun)
		if err != nil {
			return fmt.Errorf("auto-collapse: %w", err)
		}
		if collapsed > 0 {
			verb := "auto-collapsed"
			if *dryRun {
				verb = "would auto-collapse"
			}
			fmt.Fprintf(os.Stderr, "%s %d byte-identical duplicate(s)\n", verb, collapsed)
		}
		files = kept
	}

	if len(files) == 0 {
		if *report {
			// Spec scenario: "Empty report emits an empty JSON
			// array". Route through the renderer so JSON mode
			// produces `[]\n` and text mode produces "no findings"
			// rather than the operator-facing stderr summary.
			return renderReport(os.Stdout, *reportFormat, nil)
		}
		fmt.Fprintf(os.Stderr, "ingest: no MIB-shaped files in %s\n", *src)
		return nil
	}

	results, parseErrors := classifyFiles(*smidump, *smilint, *src, *root, files, groups, *compileTimeout, compileTimeoutSet)

	if *report {
		// Read-only triage mode: skip the move/index pipeline
		// entirely. collectReportFindings runs every grouping pass
		// (including the corpus cross-check that loads
		// <root>/mibs/INDEX.yaml) and renderReport writes to stdout
		// in the requested format. errReportActionable signals the
		// exit policy: 0 when only info findings, non-zero
		// otherwise (main.go translates the sentinel to silent
		// exit 1 so stdout stays clean for jq pipelines).
		findings := collectReportFindings(*src, *root, results, parseErrors)
		rerr := renderReport(os.Stdout, *reportFormat, findings)
		// Budget-exhausted files are deliberately NOT `broken`
		// findings (a truncated report claiming N broken files would
		// lie); surface the truncation on stderr and force the
		// actionable exit so a clean-looking report can't mask
		// unanalyzed files.
		if n := countBudgetExhausted(parseErrors); n > 0 {
			fmt.Fprintf(os.Stderr, "warning: compile budget exhausted — %d file(s) not analyzed; re-run with a higher --compile-timeout\n", n)
			if rerr == nil {
				rerr = errReportActionable
			}
		}
		return rerr
	}

	results = append(results, parseErrors...)
	moves, refusedCount, skippedNonMIB, parseErrorCount, budgetExhaustedCount := planMoves(results, *root)

	if *dryRun {
		printDryRun(os.Stdout, moves)
		return nil
	}

	movedCount, refusedAtMove, gitAddFailures, err := applyMoves(moves, *root, *gitAdd)
	if err != nil {
		printSummary(os.Stdout, moves, movedCount, refusedCount+refusedAtMove, skippedNonMIB, parseErrorCount, budgetExhaustedCount, gitAddFailures)
		return err
	}

	if !*noIndex && movedCount > 0 {
		if err := runMakeIndex(*root); err != nil {
			fmt.Fprintf(os.Stderr, "ingest: make index failed: %v\n", err)
			// Continue to summary — the moves still happened.
		}
	}

	printSummary(os.Stdout, moves, movedCount, refusedCount+refusedAtMove, skippedNonMIB, parseErrorCount, budgetExhaustedCount, gitAddFailures)

	// Non-zero exit only on actionable failures. Non-MIB files
	// dropped in import/ are EXPECTED collateral when an operator
	// drops a vendor's archive (READMEs, LICENSEs, partial files);
	// they don't fail the run. Refusals, parse errors, budget
	// exhaustion (the batch is incomplete), and `git add`
	// failures DO.
	totalRefused := refusedCount + refusedAtMove
	if totalRefused > 0 || parseErrorCount > 0 || budgetExhaustedCount > 0 || gitAddFailures > 0 {
		budgetSuffix := ""
		if budgetExhaustedCount > 0 {
			budgetSuffix = fmt.Sprintf(", %d cut off by the compile budget", budgetExhaustedCount)
		}
		return fmt.Errorf("%d refused, %d parse errors, %d git-add failures%s",
			totalRefused, parseErrorCount, gitAddFailures, budgetSuffix)
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

// autoCollapseIdentical hashes every file in `files`, groups by
// sha256, and (when dryRun is false) deletes all but the
// lexicographically-first source path per group. The returned
// `kept` slice contains the surviving paths sorted
// lexicographically (matching walkUpload's existing output
// ordering); it is the input to classifyFiles after the collapse.
//
// Files that cannot be opened/hashed are not deduplicated but stay
// in `kept` so classifyFiles can surface them as non-mib findings.
//
// Idempotent: re-running against a freshly-collapsed `--src`
// produces zero further deletions because every hash group has
// size 1.
func autoCollapseIdentical(files []string, dryRun bool) (kept []string, collapsed int, err error) {
	groups := make(map[string][]string, len(files))
	// unhashable collects paths that hashFile rejected (broken
	// symlinks, permission errors, etc.). They're not eligible for
	// dedup but classifyFiles needs to see them to emit non-mib
	// findings.
	var unhashable []string
	for _, f := range files {
		sha, _, herr := hashFile(f)
		if herr != nil {
			unhashable = append(unhashable, f)
			continue
		}
		groups[sha] = append(groups[sha], f)
	}
	kept = append(kept, unhashable...)
	for _, group := range groups {
		// Lexicographic-first wins per spec — deterministic so a
		// re-run against the same input produces an identical kept
		// set even though map iteration is randomized.
		sort.Strings(group)
		kept = append(kept, group[0])
		for _, dup := range group[1:] {
			collapsed++
			if dryRun {
				continue
			}
			if rmErr := os.Remove(dup); rmErr != nil {
				// A vanished file is the desired post-state; if a
				// concurrent process (or a prior partial run)
				// already deleted the dup, the goal of "this path
				// is no longer in --src" is met. Treat ENOENT as
				// success so the pre-pass remains idempotent under
				// concurrent or restart scenarios.
				if os.IsNotExist(rmErr) {
					continue
				}
				return nil, collapsed, fmt.Errorf("remove %s: %w", dup, rmErr)
			}
		}
	}
	sort.Strings(kept)
	return kept, collapsed, nil
}

// hashFile streams the file at path through sha256 and returns the
// lowercase-hex digest plus the byte length. Streaming via io.Copy
// keeps memory bounded for large MIBs. Errors from Open or read
// propagate so callers can surface the file as a "non-mib" finding
// without a hash.
func hashFile(path string) (string, int64, error) {
	// #nosec G304 -- path is from the operator-supplied --src walk in mib-ingest; no untrusted input.
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// budgetExhausted reports whether a compile error is the batch bound
// (`ctxErr`) firing rather than a real parse failure. The bound
// produces two distinct error shapes (verified empirically — see the
// change's design doc):
//
//   - Expired before the file's smidump started: exec returns
//     ctx.Err() directly, so the chain carries
//     context.DeadlineExceeded and errors.Is matches.
//   - Expired while smidump was in flight: CommandContext kills the
//     child, and Wait PREFERS the child's *ExitError
//     ("signal: killed") over the context error — the chain carries
//     no context error at all. A signal-terminated child
//     (ExitCode -1) under an expired bound is attributed to the
//     bound.
//
// The second shape can also swallow a smidump that crashed by signal
// just as the bound fired — acceptable: the file stays in import/
// and a re-run with a fresh budget surfaces the real failure.
func budgetExhausted(ctxErr, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if !errors.Is(ctxErr, context.DeadlineExceeded) {
		return false
	}
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == -1
}

// countBudgetExhausted counts outcomeBudgetExhausted entries — the
// rollup figure for summaries and the report-mode stderr warning.
func countBudgetExhausted(rs []result) int {
	n := 0
	for _, r := range rs {
		if r.outcome == outcomeBudgetExhausted {
			n++
		}
	}
	return n
}

// classifyFiles runs the lexical-marker check + libsmi parse +
// mibcorpus.Classify pipeline for every input file. Files that fail
// the marker check or libsmi parse are returned as parseErrors
// with outcome=outcomeLeftInUpload. Compile is bounded by a
// per-batch timeout so a hung smidump can't hang the ingest forever;
// the bound scales with the batch size unless `--compile-timeout`
// pins it explicitly (`timeoutSet`), and files the bound cut off are
// classified outcomeBudgetExhausted, not parse errors.
func classifyFiles(smidumpPath, smilintPath, srcDir, root string, files []string, groups mibcorpus.GroupMap, compileTimeout time.Duration, timeoutSet bool) (parsed []result, parseErrors []result) {
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
	// smidump shouldn't hang ingest indefinitely. The bound is a
	// hang backstop, not a throughput ceiling: the default scales
	// with the batch (~25 ms/file measured vs 1 s/file budgeted) and
	// honors BLITTERMIB_COMPILE_TIMEOUT, the same as the import engine.
	timeout := compileTimeout
	if !timeoutSet {
		timeout = compile.ScaledTimeout(len(keep))
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	results := c.Compile(ctx, keep)

	for _, r := range results {
		meta := hashes[r.Target] // zero value if absent; only happens for path mismatches
		if budgetExhausted(ctx.Err(), r.Err) {
			// The bound cut this file off (queued or in flight) —
			// incomplete work, not a broken MIB. The file stays in
			// import/; a re-run picks it up (already-moved
			// destinations refuse, so re-running is idempotent).
			parseErrors = append(parseErrors, result{
				src:     r.Target,
				outcome: outcomeBudgetExhausted,
				reason:  "compile budget exhausted before this file was processed",
				sha:     meta.sha,
				size:    meta.size,
			})
			continue
		}
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
// (expected; non-actionable), count of files that hit a real parse
// error (actionable), and count the compile bound cut off
// (incomplete — reported as one rollup, not per-file).
func planMoves(results []result, root string) (moves []result, refusedCount, skippedNonMIBCount, parseErrorCount, budgetExhaustedCount int) {
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
		case outcomeBudgetExhausted:
			budgetExhaustedCount++
			// No per-file stderr — exhaustion hits the batch tail en
			// masse; the summary's single rollup line carries it.
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
	return moves, refusedCount, skippedNonMIBCount, parseErrorCount, budgetExhaustedCount
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
			if mkErr := os.MkdirAll(filepath.Dir(fullDst), 0o750); mkErr != nil {
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
				// #nosec G204 -- offline CLI; rel is the relative destination computed by ingest under the operator-supplied --root, passed after a `--` separator.
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
			_, _ = fmt.Fprintf(w, "  [%-6s] %s → %s\n", r.conf, r.src, r.dst)
		case outcomeRefused:
			_, _ = fmt.Fprintf(w, "  [refuse]      %s — %s\n", r.src, r.reason)
		case outcomeParseError:
			_, _ = fmt.Fprintf(w, "  [parse-error] %s — %s\n", r.src, r.reason)
		case outcomeSkippedNonMIB:
			_, _ = fmt.Fprintf(w, "  [non-mib]     %s — %s\n", r.src, r.reason)
		case outcomeBudgetExhausted:
			_, _ = fmt.Fprintf(w, "  [budget]      %s — %s\n", r.src, r.reason)
		}
	}
	// Dry-run keeps its exit-0 contract (parse errors don't fail it
	// either), but the truncation still deserves the rollup guidance.
	if n := countBudgetExhausted(moves); n > 0 {
		_, _ = fmt.Fprintf(w, "compile budget exhausted — %d file(s) unprocessed; re-run, or raise --compile-timeout\n", n)
	}
	_, _ = fmt.Fprintln(w, "(dry-run; no files moved, no INDEX.yaml regen)")
}

// summaryListMax bounds the per-file list of leftover files printed
// after the summary line. A vendor archive can drop hundreds of
// READMEs / Makefiles into import/; truncating at 20 keeps the
// terminal usable while still answering the common "what are the
// 5 files still sitting in upload?" question for a small drop.
const summaryListMax = 20

func printSummary(w io.Writer, moves []result, moved, refused, skippedNonMIB, parseErrors, budgetExhausted, gitAddFailures int) {
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
	_, _ = fmt.Fprintf(w,
		"ingest: %d moved (%d high/medium → corpus, %d low → unsorted), %d refused, %d non-mib skipped, %d parse errors",
		moved, highMedium, routedUnsorted, refused, skippedNonMIB, parseErrors)
	if gitAddFailures > 0 {
		_, _ = fmt.Fprintf(w, ", %d git-add failures", gitAddFailures)
	}
	_, _ = fmt.Fprintln(w)
	if budgetExhausted > 0 {
		// One rollup, not per-file noise: exhaustion hits the batch
		// tail en masse, and the cause is the bound, not the files.
		_, _ = fmt.Fprintf(w,
			"compile budget exhausted — %d file(s) unprocessed; they remain in import/ — re-run, or raise --compile-timeout\n",
			budgetExhausted)
	}

	// Final per-file rundown of anything still sitting in import/.
	// This is exactly the set the operator needs to act on (delete,
	// re-classify, or fix the source MIB).
	var leftover []result
	for _, r := range moves {
		switch r.outcome {
		case outcomeSkippedNonMIB, outcomeParseError, outcomeRefused, outcomeBudgetExhausted:
			leftover = append(leftover, r)
		}
	}
	if len(leftover) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "left in import/:")
	for i, r := range leftover {
		if i == summaryListMax {
			_, _ = fmt.Fprintf(w, "  ...and %d more (use --dry-run to see the full list)\n",
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
		case outcomeBudgetExhausted:
			tag = "budget"
		}
		_, _ = fmt.Fprintf(w, "  [%-11s] %s — %s\n", tag, r.src, r.reason)
	}
}
