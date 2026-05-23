/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// writeFile is a tiny helper for the auto-collapse tests; each
// case sets up a tempdir of MIB-shaped files and inspects what
// remains after collapse.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestAutoCollapseIdentical(t *testing.T) {
	t.Run("five identical files collapse to lex-first", func(t *testing.T) {
		dir := t.TempDir()
		body := "X-MIB DEFINITIONS ::= BEGIN\nEND\n"
		// Intentionally unordered file names so the kept-lex-first
		// rule is exercised.
		names := []string{"e.mib", "a.mib", "c.mib", "b.mib", "d.mib"}
		var files []string
		for _, n := range names {
			p := filepath.Join(dir, n)
			writeFile(t, p, body)
			files = append(files, p)
		}
		kept, collapsed, err := autoCollapseIdentical(files, false)
		if err != nil {
			t.Fatal(err)
		}
		if collapsed != 4 {
			t.Errorf("collapsed = %d, want 4", collapsed)
		}
		if len(kept) != 1 {
			t.Errorf("kept = %v, want one entry", kept)
		}
		if kept[0] != filepath.Join(dir, "a.mib") {
			t.Errorf("kept lex-first = %s, want %s", kept[0], filepath.Join(dir, "a.mib"))
		}
		// On disk: only a.mib survives.
		entries, _ := os.ReadDir(dir)
		var onDisk []string
		for _, e := range entries {
			onDisk = append(onDisk, e.Name())
		}
		sort.Strings(onDisk)
		if !reflect.DeepEqual(onDisk, []string{"a.mib"}) {
			t.Errorf("filesystem residue = %v, want [a.mib]", onDisk)
		}
	})

	t.Run("idempotent: second run deletes zero", func(t *testing.T) {
		dir := t.TempDir()
		body := "X-MIB DEFINITIONS ::= BEGIN\nEND\n"
		var files []string
		for _, n := range []string{"a.mib", "b.mib", "c.mib"} {
			p := filepath.Join(dir, n)
			writeFile(t, p, body)
			files = append(files, p)
		}
		// First run collapses 2.
		_, c1, err := autoCollapseIdentical(files, false)
		if err != nil {
			t.Fatal(err)
		}
		if c1 != 2 {
			t.Errorf("first run collapsed = %d, want 2", c1)
		}
		// Re-walk and second run.
		files2, err := walkUpload(dir)
		if err != nil {
			t.Fatal(err)
		}
		_, c2, err := autoCollapseIdentical(files2, false)
		if err != nil {
			t.Fatal(err)
		}
		if c2 != 0 {
			t.Errorf("second run collapsed = %d, want 0 (idempotent)", c2)
		}
	})

	t.Run("dry-run reports count without deleting", func(t *testing.T) {
		dir := t.TempDir()
		body := "X-MIB DEFINITIONS ::= BEGIN\nEND\n"
		var files []string
		for _, n := range []string{"a.mib", "b.mib", "c.mib"} {
			p := filepath.Join(dir, n)
			writeFile(t, p, body)
			files = append(files, p)
		}
		_, collapsed, err := autoCollapseIdentical(files, true)
		if err != nil {
			t.Fatal(err)
		}
		if collapsed != 2 {
			t.Errorf("dry-run collapsed-count = %d, want 2", collapsed)
		}
		// All three files still on disk.
		for _, n := range []string{"a.mib", "b.mib", "c.mib"} {
			if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
				t.Errorf("dry-run deleted %s; should still exist (err=%v)", n, err)
			}
		}
	})

	t.Run("distinct hashes → nothing collapsed", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "a.mib"), "A-MIB DEFINITIONS ::= BEGIN\nEND\n")
		writeFile(t, filepath.Join(dir, "b.mib"), "B-MIB DEFINITIONS ::= BEGIN\nEND\n")
		files, _ := walkUpload(dir)
		kept, collapsed, err := autoCollapseIdentical(files, false)
		if err != nil {
			t.Fatal(err)
		}
		if collapsed != 0 {
			t.Errorf("distinct shas should collapse 0; got %d", collapsed)
		}
		if len(kept) != 2 {
			t.Errorf("kept = %d files, want 2", len(kept))
		}
	})

	t.Run("unreadable files pass through", func(t *testing.T) {
		dir := t.TempDir()
		// Create one real file plus a dangling symlink (skipped
		// by hashFile via os.Open error).
		realPath := filepath.Join(dir, "real.mib")
		writeFile(t, realPath, "REAL-MIB DEFINITIONS ::= BEGIN\nEND\n")
		brokenPath := filepath.Join(dir, "broken.mib")
		if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), brokenPath); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
		kept, collapsed, err := autoCollapseIdentical([]string{realPath, brokenPath}, false)
		if err != nil {
			t.Fatal(err)
		}
		if collapsed != 0 {
			t.Errorf("expected 0 collapses with one real + one unreadable; got %d", collapsed)
		}
		// Both paths must be in kept — broken so classifyFiles
		// can surface a non-mib finding for it; assert by content,
		// not just count, so a future regression that drops
		// unreadable paths can't silently pass this test.
		want := map[string]bool{realPath: true, brokenPath: true}
		got := map[string]bool{}
		for _, p := range kept {
			got[p] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("kept = %v, want both %s and %s preserved", kept, realPath, brokenPath)
		}
	})

	t.Run("idempotent across vanished file (concurrent process race)", func(t *testing.T) {
		// Spec contract: re-running collapse against a `--src`
		// where one of the dup paths has already been deleted (by
		// a prior partial run or a concurrent process) MUST NOT
		// fail. Simulate by giving autoCollapseIdentical a path
		// list that references a non-existent file.
		dir := t.TempDir()
		body := "X-MIB DEFINITIONS ::= BEGIN\nEND\n"
		realPath := filepath.Join(dir, "a.mib")
		writeFile(t, realPath, body)
		ghost := filepath.Join(dir, "b.mib")
		writeFile(t, ghost, body)
		// Pre-delete the ghost so the collapse pass attempts an
		// os.Remove on a non-existent path.
		if err := os.Remove(ghost); err != nil {
			t.Fatal(err)
		}
		// Pass the original two paths anyway — they hash the same
		// because b.mib used to exist with identical content; but
		// since b.mib is gone, hashFile will fail for it and it
		// becomes "unhashable" rather than entering the dup
		// group. Add a third real dup so the collapse loop has a
		// concrete delete target whose target also vanishes.
		dup := filepath.Join(dir, "c.mib")
		writeFile(t, dup, body)
		// Now: a.mib (hashable), b.mib (unhashable — vanished),
		// c.mib (hashable, same hash as a.mib). The collapse
		// should keep a.mib (lex-first) and delete c.mib.
		_, collapsed, err := autoCollapseIdentical([]string{realPath, ghost, dup}, false)
		if err != nil {
			t.Fatalf("vanished-file race made the collapse abort: %v", err)
		}
		if collapsed != 1 {
			t.Errorf("collapsed = %d, want 1 (c.mib deleted)", collapsed)
		}
	})

	t.Run("kept slice is lex-sorted", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "zzz.mib"), "Z-MIB DEFINITIONS ::= BEGIN\nEND\n")
		writeFile(t, filepath.Join(dir, "aaa.mib"), "A-MIB DEFINITIONS ::= BEGIN\nEND\n")
		files, _ := walkUpload(dir)
		kept, _, err := autoCollapseIdentical(files, false)
		if err != nil {
			t.Fatal(err)
		}
		if !sort.StringsAreSorted(kept) {
			t.Errorf("kept not lex-sorted: %v", kept)
		}
	})
}

func TestIngestCmdMutexReportAndAutoCollapse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "X-MIB.mib"), "X-MIB DEFINITIONS ::= BEGIN\nEND\n")
	// Run ingestCmd with both --report and --auto-collapse-identical.
	// Must exit non-zero with a clear error BEFORE walking files.
	err := ingestCmd([]string{
		"--src", dir,
		"--root", dir, // doesn't matter; we shouldn't get that far
		"--report",
		"--auto-collapse-identical",
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in error; got %v", err)
	}
	// The file should still be intact (no walk, no delete).
	if _, statErr := os.Stat(filepath.Join(dir, "X-MIB.mib")); statErr != nil {
		t.Errorf("X-MIB.mib was touched despite mutex refusal; err=%v", statErr)
	}
}
