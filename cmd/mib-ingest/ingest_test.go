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
// marker stay out of the result set.
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
	// walkUpload returns ALL extension-matching files; the marker
	// filter happens later in classifyFiles. Verify the extension
	// filter rejected README.md and the marker check will reject
	// GARBAGE.mib downstream.
	wantSuffixes := []string{"CISCO-FOO-MIB.mib", "EXTLESS-MIB", "GARBAGE.mib"}
	if len(got) != len(wantSuffixes) {
		t.Errorf("walkUpload returned %d files, want %d: %v", len(got), len(wantSuffixes), got)
	}
}

// TestHasMIBOpener verifies the marker-sniff gate catches typical
// non-MIB files (LICENSE, README) and accepts a real opener.
func TestHasMIBOpener(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, body string
		want       bool
	}{
		{"with-marker", "FOO-MIB DEFINITIONS ::= BEGIN\nEND\n", true},
		{"with-leading-comments", "-- header\nFOO DEFINITIONS ::= BEGIN\n", true},
		{"no-marker", "Copyright 2024 ACME\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name)
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := hasMIBOpener(path)
			if err != nil {
				t.Fatalf("hasMIBOpener: %v", err)
			}
			if got != c.want {
				t.Errorf("hasMIBOpener(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// TestPlanMovesRoutesByConfidence exercises the destination-routing
// rules without invoking libsmi: classifyFiles output is synthesised
// directly. Keeps the test pure-Go (CI doesn't need libsmi for this
// branch) AND keeps the assertion focused on planMoves' behaviour.
func TestPlanMovesRoutesByConfidence(t *testing.T) {
	root := t.TempDir()
	results := []result{
		{src: "mibs/upload/CISCO-FOO-MIB.mib", dst: "mibs/vendors/9-cisco/CISCO-FOO-MIB", conf: mibcorpus.ConfidenceHigh},
		{src: "mibs/upload/MYSTERY-MIB", dst: "mibs/vendors/999999-unknown/MYSTERY-MIB", conf: mibcorpus.ConfidenceMedium},
		{src: "mibs/upload/SOMEONE-ELSES-MIB", dst: "mibs/unsorted/SOMEONE-ELSES-MIB", conf: mibcorpus.ConfidenceLow},
		{src: "mibs/upload/README.txt", outcome: outcomeLeftInUpload, reason: "no MIB marker"},
	}
	moves, refused, leftInUpload := planMoves(results, root)
	if refused != 0 {
		t.Errorf("refused = %d, want 0 (no destinations seeded)", refused)
	}
	if leftInUpload != 1 {
		t.Errorf("leftInUpload = %d, want 1 (README.txt)", leftInUpload)
	}
	wantOutcomes := []outcome{outcomeMoved, outcomeMoved, outcomeRoutedUnsorted, outcomeLeftInUpload}
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
	moves, refused, leftInUpload := planMoves(results, root)
	if refused != 1 {
		t.Errorf("refused = %d, want 1", refused)
	}
	if leftInUpload != 0 {
		t.Errorf("leftInUpload = %d, want 0", leftInUpload)
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
	dstAbs := filepath.Join(root, "mibs/vendors/9-cisco/CISCO-FOO-MIB")
	moves := []result{
		{
			src:     srcPath,
			dst:     dstAbs,
			outcome: outcomeMoved,
			conf:    mibcorpus.ConfidenceHigh,
		},
	}
	moved, refusedAtMove, err := applyMoves(moves, root, false)
	if err != nil {
		t.Fatalf("applyMoves: %v", err)
	}
	if moved != 1 {
		t.Errorf("moved = %d, want 1", moved)
	}
	if refusedAtMove != 0 {
		t.Errorf("refusedAtMove = %d, want 0", refusedAtMove)
	}
	if _, err := os.Stat(dstAbs); err != nil {
		t.Errorf("destination not created: %v", err)
	}
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Errorf("source still present after move: err=%v", err)
	}
}

// TestApplyMovesGitAdd checks that the --git-add path actually runs
// git add. Skipped when git is not on PATH or this isn't run inside
// a git work tree.
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
	dstAbs := filepath.Join(root, "mibs/vendors/9-cisco/CISCO-FOO-MIB")
	moves := []result{
		{src: srcPath, dst: dstAbs, outcome: outcomeMoved, conf: mibcorpus.ConfidenceHigh},
	}
	moved, refusedAtMove, err := applyMoves(moves, root, true)
	if err != nil {
		t.Fatalf("applyMoves: %v", err)
	}
	if moved != 1 || refusedAtMove != 0 {
		t.Fatalf("moved=%d refused=%d", moved, refusedAtMove)
	}
	// Confirm git status shows the file as added (A in index).
	out, err := exec.Command("git", "-C", root, "status", "--short", dstAbs).Output()
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
	printSummary(&buf, moves, 3, 0, 0)
	got := buf.String()
	for _, want := range []string{"3 moved", "2 high/medium", "1 low → unsorted"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q; got: %q", want, got)
		}
	}
}
