package mibsbundle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageReadmeIsSkipped(t *testing.T) {
	dir := t.TempDir()
	staged, err := Stage(dir)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	for _, p := range staged {
		if filepath.Base(p) == "README.md" {
			t.Errorf("README.md should be skipped, but it was staged at %q", p)
		}
	}
}

func TestStageIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	// Drop a stub MIB into the directory so we can prove Stage
	// doesn't overwrite existing files.
	stub := filepath.Join(dir, "USER-OVERRIDE-MIB")
	if err := os.WriteFile(stub, []byte("user-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Stage(dir); err != nil {
		t.Fatalf("first Stage: %v", err)
	}
	if _, err := Stage(dir); err != nil {
		t.Fatalf("second Stage: %v", err)
	}

	got, err := os.ReadFile(stub)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user-content" {
		t.Errorf("Stage clobbered an existing file: %q", got)
	}
}

func TestCountIsConsistent(t *testing.T) {
	// Count and Stage should report the same number of files
	// after a fresh Stage into an empty directory.
	dir := t.TempDir()
	staged, err := Stage(dir)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if got, want := Count(), len(staged); got != want {
		t.Errorf("Count() = %d, staged = %d", got, want)
	}
}
