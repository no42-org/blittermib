package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/mibcorpus"
)

// TestWalkUploadFiltersNonMIBs covers the upload-folder gate: hidden
// files (.gitkeep), wrong extensions, and files lacking the MIB
// marker stay out of the result set's downstream pipeline. (walkUpload
// itself returns extension-matching files; the marker filter happens
// later in classifyFiles.)
func TestWalkUploadFiltersNonMIBs(t *testing.T) {
	dir := t.TempDir()
	mib := func(name string) string { return name + " DEFINITIONS ::= BEGIN\nEND\n" }
	cases := []struct {
		path, body string
	}{
		{filepath.Join(dir, ".gitkeep"), ""},
		{filepath.Join(dir, "README.md"), "# notes\n"},
		{filepath.Join(dir, "CISCO-FOO-MIB.mib"), mib("CISCO-FOO-MIB")},
		{filepath.Join(dir, "EXTLESS-MIB"), mib("EXTLESS-MIB")},
		{filepath.Join(dir, "GARBAGE.mib"), "no marker here\n"},
	}
	for _, c := range cases {
		if err := os.WriteFile(c.path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := walkUpload(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffixes := []string{"CISCO-FOO-MIB.mib", "EXTLESS-MIB", "GARBAGE.mib"}
	if len(got) != len(wantSuffixes) {
		t.Errorf("walkUpload returned %d files, want %d: %v", len(got), len(wantSuffixes), got)
	}
}

// TestPlanMovesRoutesByConfidence exercises the destination-routing
// rules without invoking libsmi: classifyFiles output is synthesised
// directly. Keeps the test pure-Go AND keeps the assertion focused
// on planMoves' behaviour.
func TestPlanMovesRoutesByConfidence(t *testing.T) {
	root := t.TempDir()
	results := []result{
		{src: "mibs/upload/CISCO-FOO-MIB.mib", dst: "mibs/vendors/9-cisco/CISCO-FOO-MIB", conf: mibcorpus.ConfidenceHigh},
		{src: "mibs/upload/MYSTERY-MIB", dst: "mibs/vendors/999999-unknown/MYSTERY-MIB", conf: mibcorpus.ConfidenceMedium},
		{src: "mibs/upload/SOMEONE-ELSES-MIB", dst: "mibs/unsorted/SOMEONE-ELSES-MIB", conf: mibcorpus.ConfidenceLow},
		{src: "mibs/upload/README.txt", outcome: outcomeSkippedNonMIB, reason: "no MIB marker"},
		{src: "mibs/upload/BROKEN-MIB", outcome: outcomeParseError, reason: "smidump failed"},
	}
	moves, refused, skippedNonMIB, parseErrors, budgetExhausted := planMoves(results, root)
	_ = budgetExhausted
	if refused != 0 {
		t.Errorf("refused = %d, want 0 (no destinations seeded)", refused)
	}
	if skippedNonMIB != 1 {
		t.Errorf("skippedNonMIB = %d, want 1 (README.txt)", skippedNonMIB)
	}
	if parseErrors != 1 {
		t.Errorf("parseErrors = %d, want 1 (BROKEN-MIB)", parseErrors)
	}
	wantOutcomes := []outcome{outcomeMoved, outcomeMoved, outcomeRoutedUnsorted, outcomeSkippedNonMIB, outcomeParseError}
	for i, r := range moves {
		if r.outcome != wantOutcomes[i] {
			t.Errorf("moves[%d] outcome = %v, want %v", i, r.outcome, wantOutcomes[i])
		}
	}
}

// TestPlanMovesRefusesOnExistingDst seeds a destination file then
// asserts the corresponding upload row is marked refused (not moved).
func TestPlanMovesRefusesOnExistingDst(t *testing.T) {
	root := t.TempDir()
	dstRel := "mibs/vendors/9-cisco/CISCO-FOO-MIB"
	if err := os.MkdirAll(filepath.Join(root, "mibs/vendors/9-cisco"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, dstRel), []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := []result{
		{src: "mibs/upload/CISCO-FOO-MIB.mib", dst: dstRel, conf: mibcorpus.ConfidenceHigh},
	}
	moves, refused, skippedNonMIB, parseErrors, budgetExhausted := planMoves(results, root)
	_ = budgetExhausted
	if refused != 1 {
		t.Errorf("refused = %d, want 1", refused)
	}
	if skippedNonMIB != 0 || parseErrors != 0 {
		t.Errorf("skippedNonMIB=%d parseErrors=%d, want 0/0", skippedNonMIB, parseErrors)
	}
	if moves[0].outcome != outcomeRefused {
		t.Errorf("outcome = %v, want refused", moves[0].outcome)
	}
	if !strings.Contains(moves[0].reason, "destination already exists") {
		t.Errorf("reason = %q, want it to mention 'destination already exists'", moves[0].reason)
	}
}

// TestApplyMovesRenames seeds a real upload file + result slice and
// asserts the os.Rename happens for high-confidence rows. Uses
// synthesised result slices so it stays libsmi-free.
//
// Note: r.dst is REPO-RELATIVE — applyMoves joins with root.
func TestApplyMovesRenames(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "mibs/upload")
	if err := os.MkdirAll(upload, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(upload, "CISCO-FOO-MIB.mib")
	if err := os.WriteFile(srcPath, []byte("CISCO-FOO-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dstRel := "mibs/vendors/9-cisco/CISCO-FOO-MIB"
	moves := []result{
		{
			src:     srcPath,
			dst:     dstRel,
			outcome: outcomeMoved,
			conf:    mibcorpus.ConfidenceHigh,
		},
	}
	moved, refusedAtMove, gitFails, err := applyMoves(moves, root, false)
	if err != nil {
		t.Fatalf("applyMoves: %v", err)
	}
	if moved != 1 || refusedAtMove != 0 || gitFails != 0 {
		t.Errorf("moved=%d refused=%d gitFails=%d", moved, refusedAtMove, gitFails)
	}
	dstAbs := filepath.Join(root, dstRel)
	if _, err := os.Stat(dstAbs); err != nil {
		t.Errorf("destination not created: %v", err)
	}
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Errorf("source still present after move: err=%v", err)
	}
}

// TestApplyMovesGitAdd checks that the --git-add path actually runs
// git add. Skipped when git isn't on PATH.
func TestApplyMovesGitAdd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	upload := filepath.Join(root, "mibs/upload")
	if err := os.MkdirAll(upload, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(upload, "CISCO-FOO-MIB.mib")
	if err := os.WriteFile(srcPath, []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dstRel := "mibs/vendors/9-cisco/CISCO-FOO-MIB"
	moves := []result{
		{src: srcPath, dst: dstRel, outcome: outcomeMoved, conf: mibcorpus.ConfidenceHigh},
	}
	moved, refusedAtMove, gitFails, err := applyMoves(moves, root, true)
	if err != nil {
		t.Fatalf("applyMoves: %v", err)
	}
	if moved != 1 || refusedAtMove != 0 || gitFails != 0 {
		t.Fatalf("moved=%d refused=%d gitFails=%d", moved, refusedAtMove, gitFails)
	}
	out, err := exec.Command("git", "-C", root, "status", "--short", filepath.Join(root, dstRel)).Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if !bytes.Contains(out, []byte("A ")) {
		t.Errorf("git status doesn't show added entry; output:\n%s", out)
	}
}

// TestPrintSummary spot-checks the summary line format.
func TestPrintSummary(t *testing.T) {
	var buf bytes.Buffer
	moves := []result{
		{outcome: outcomeMoved, conf: mibcorpus.ConfidenceHigh},
		{outcome: outcomeMoved, conf: mibcorpus.ConfidenceMedium},
		{outcome: outcomeRoutedUnsorted, conf: mibcorpus.ConfidenceLow},
	}
	printSummary(&buf, moves, 3, 0, 0, 0, 0, 0)
	got := buf.String()
	for _, want := range []string{"3 moved", "2 high/medium", "1 low → unsorted"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q; got: %q", want, got)
		}
	}
}

// TestPrintSummaryGitAddFailures asserts the summary surfaces the
// `git add` failure count when --git-add was set and one or more
// `git add` invocations failed.
func TestPrintSummaryGitAddFailures(t *testing.T) {
	var buf bytes.Buffer
	moves := []result{{outcome: outcomeMoved, conf: mibcorpus.ConfidenceHigh}}
	printSummary(&buf, moves, 1, 0, 0, 0, 0, 2)
	if !strings.Contains(buf.String(), "2 git-add failures") {
		t.Errorf("summary missing git-add failures count; got %q", buf.String())
	}
}

// TestPrintSummaryLeftoverListing asserts the summary lists every
// file still sitting in upload/ (non-MIB skipped, parse errors,
// refused) so the operator can see what to act on.
func TestPrintSummaryLeftoverListing(t *testing.T) {
	var buf bytes.Buffer
	moves := []result{
		{outcome: outcomeMoved, conf: mibcorpus.ConfidenceHigh},
		{outcome: outcomeSkippedNonMIB, src: "mibs/upload/README.txt", reason: "no MIB marker"},
		{outcome: outcomeParseError, src: "mibs/upload/BROKEN-MIB", reason: "smidump rejected module"},
		{outcome: outcomeRefused, src: "mibs/upload/CISCO-FOO-MIB", reason: "destination already exists: mibs/vendors/9-cisco/CISCO-FOO-MIB"},
	}
	printSummary(&buf, moves, 1, 1, 1, 1, 0, 0)
	got := buf.String()
	for _, want := range []string{
		"left in upload/:",
		"[non-mib    ] mibs/upload/README.txt",
		"[parse-error] mibs/upload/BROKEN-MIB",
		"[refuse     ] mibs/upload/CISCO-FOO-MIB",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q; got:\n%s", want, got)
		}
	}
}

// TestPrintSummaryLeftoverTruncated asserts the per-file list is
// bounded so a vendor archive with hundreds of READMEs doesn't
// drown the terminal.
func TestPrintSummaryLeftoverTruncated(t *testing.T) {
	var buf bytes.Buffer
	var moves []result
	for i := 0; i < summaryListMax+5; i++ {
		moves = append(moves, result{
			outcome: outcomeSkippedNonMIB,
			src:     "mibs/upload/junk",
			reason:  "no MIB marker",
		})
	}
	printSummary(&buf, moves, 0, 0, len(moves), 0, 0, 0)
	got := buf.String()
	if !strings.Contains(got, "...and 5 more") {
		t.Errorf("summary missing truncation hint; got:\n%s", got)
	}
}

// TestPrintSummaryNoLeftover asserts the trailing block is omitted
// when nothing is left in upload/ — a clean run prints just the
// summary line.
func TestPrintSummaryNoLeftover(t *testing.T) {
	var buf bytes.Buffer
	moves := []result{{outcome: outcomeMoved, conf: mibcorpus.ConfidenceHigh}}
	printSummary(&buf, moves, 1, 0, 0, 0, 0, 0)
	if strings.Contains(buf.String(), "left in upload") {
		t.Errorf("clean run should not print 'left in upload' block; got:\n%s", buf.String())
	}
}

// TestIngestDryRun asserts --dry-run touches no files. Drops a
// no-marker file in upload (which the lexical-marker gate handles
// without libsmi), runs ingest with --dry-run, and verifies the
// file is still there. Independently tests that --no-index doesn't
// regen INDEX.yaml.
func TestIngestDryRun(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "mibs/upload")
	if err := os.MkdirAll(upload, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(upload, "GARBAGE.mib")
	if err := os.WriteFile(src, []byte("not a mib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ingestCmd([]string{
		"--src", upload,
		"--root", root,
		"--dry-run",
	}); err != nil {
		t.Fatalf("ingestCmd dry-run: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source file disappeared during dry-run: %v", err)
	}
}

// TestIngestExitCode asserts the binary returns nil on a clean
// ingest. A non-MIB file (no marker) is EXPECTED collateral when an
// operator drops a vendor archive alongside their MIBs and does NOT
// produce a non-zero exit — only refusals, parse errors, and
// `git add` failures do.
func TestIngestExitCode(t *testing.T) {
	root := t.TempDir()
	upload := filepath.Join(root, "mibs/upload")
	if err := os.MkdirAll(upload, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty upload — clean exit.
	if err := ingestCmd([]string{
		"--src", upload,
		"--root", root,
		"--no-index",
	}); err != nil {
		t.Errorf("empty upload: got error %v, want nil", err)
	}

	// Drop a non-MIB file → it stays in upload as a "non-mib
	// skipped" outcome. Clean exit (operators routinely drop
	// READMEs alongside real MIBs and shouldn't see exit 1).
	if err := os.WriteFile(filepath.Join(upload, "GARBAGE.mib"), []byte("not a mib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ingestCmd([]string{
		"--src", upload,
		"--root", root,
		"--no-index",
	}); err != nil {
		t.Errorf("non-MIB file: got error %v, want nil (non-MIB skips don't fail the run)", err)
	}
}
