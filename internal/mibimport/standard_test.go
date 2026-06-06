/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mibimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/store"
)

func newStandardFixture(t *testing.T) (src, root string, eng *Engine) {
	t.Helper()
	src = t.TempDir()
	root = t.TempDir()
	mustWrite(t, filepath.Join(src, "ietf", "core", "STD-A"), "STD-A v1\n")
	mustWrite(t, filepath.Join(src, "iana", "STD-B"), "STD-B v1\n")
	mustWrite(t, filepath.Join(src, "_groups.yaml"), "groups: {}\n")
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return src, root, New(root, st, mibcorpus.GroupMap{})
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestSyncStandardFirstBootPopulates(t *testing.T) {
	src, root, eng := newStandardFixture(t)
	copied, removed, err := eng.SyncStandard(src)
	if err != nil || copied != 3 || removed != 0 {
		t.Fatalf("first sync: copied=%d removed=%d err=%v", copied, removed, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "ietf", "core", "STD-A")); string(b) != "STD-A v1\n" {
		t.Fatalf("STD-A not mirrored: %q", b)
	}
	// Second sync: nothing changed → zero copies, fingerprints
	// (size+mtime) untouched.
	st1, _ := os.Stat(filepath.Join(root, "ietf", "core", "STD-A"))
	copied, removed, err = eng.SyncStandard(src)
	if err != nil || copied != 0 || removed != 0 {
		t.Fatalf("idempotent sync: copied=%d removed=%d err=%v", copied, removed, err)
	}
	st2, _ := os.Stat(filepath.Join(root, "ietf", "core", "STD-A"))
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("unchanged file's mtime was touched — would churn fingerprints")
	}
}

func TestSyncStandardNeverTouchesOperatorPaths(t *testing.T) {
	src, root, eng := newStandardFixture(t)
	mustWrite(t, filepath.Join(root, "vendors", "9999-acme", "ACME-MIB"), "operator content\n")
	mustWrite(t, filepath.Join(root, "unsorted", "ODD-MIB"), "odd\n")
	mustWrite(t, filepath.Join(root, "import", "failed", "BROKEN"), "broken\n")
	mustWrite(t, filepath.Join(root, "import", "failed", "BROKEN.reason.json"), "{}\n")

	if _, _, err := eng.SyncStandard(src); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(root, "vendors", "9999-acme", "ACME-MIB"):       "operator content\n",
		filepath.Join(root, "unsorted", "ODD-MIB"):                    "odd\n",
		filepath.Join(root, "import", "failed", "BROKEN"):             "broken\n",
		filepath.Join(root, "import", "failed", "BROKEN.reason.json"): "{}\n",
	} {
		b, err := os.ReadFile(path)
		if err != nil || string(b) != want {
			t.Fatalf("operator file %s changed: %q err=%v", path, b, err)
		}
	}
}

func TestSyncStandardUpgradeRefreshesAndPrunes(t *testing.T) {
	src, root, eng := newStandardFixture(t)
	if _, _, err := eng.SyncStandard(src); err != nil {
		t.Fatal(err)
	}
	// New image: STD-A revised, STD-B dropped, STD-C added.
	mustWrite(t, filepath.Join(src, "ietf", "core", "STD-A"), "STD-A v2\n")
	if err := os.Remove(filepath.Join(src, "iana", "STD-B")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "iana", "STD-C"), "STD-C v1\n")

	copied, removed, err := eng.SyncStandard(src)
	if err != nil || copied != 2 || removed != 1 {
		t.Fatalf("upgrade sync: copied=%d removed=%d err=%v", copied, removed, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "ietf", "core", "STD-A")); string(b) != "STD-A v2\n" {
		t.Fatalf("STD-A not refreshed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "iana", "STD-B")); !os.IsNotExist(err) {
		t.Fatal("dropped standard file not pruned")
	}
}

func TestSyncStandardMissingSourceIsNoop(t *testing.T) {
	_, _, eng := newStandardFixture(t)
	copied, removed, err := eng.SyncStandard(filepath.Join(t.TempDir(), "absent"))
	if err != nil || copied != 0 || removed != 0 {
		t.Fatalf("missing src must be a no-op: %d %d %v", copied, removed, err)
	}
}

// TestSyncStandardPreservesOperatorFilesInStandardTrees: the import
// pipeline routes IETF-classified custom MIBs into ietf/{group}/ —
// the manifest-based prune must never touch files the sync didn't
// place, even inside image-owned subtrees.
func TestSyncStandardPreservesOperatorFilesInStandardTrees(t *testing.T) {
	src, root, eng := newStandardFixture(t)
	if _, _, err := eng.SyncStandard(src); err != nil {
		t.Fatal(err)
	}
	// Pipeline-imported file co-located in the standard tree.
	mustWrite(t, filepath.Join(root, "ietf", "core", "CUSTOM-ROUTED-MIB"), "operator import\n")

	copied, removed, err := eng.SyncStandard(src)
	if err != nil || copied != 0 || removed != 0 {
		t.Fatalf("re-sync: copied=%d removed=%d err=%v", copied, removed, err)
	}
	b, err := os.ReadFile(filepath.Join(root, "ietf", "core", "CUSTOM-ROUTED-MIB"))
	if err != nil || string(b) != "operator import\n" {
		t.Fatalf("operator file inside ietf/ was pruned or changed: %q err=%v", b, err)
	}
}

// TestSyncStandardNoManifestPrunesNothing: pre-manifest upgrade —
// without ownership records the prune must not guess.
func TestSyncStandardNoManifestPrunesNothing(t *testing.T) {
	src, root, eng := newStandardFixture(t)
	// Simulate an old deployment: standard files present, no manifest.
	mustWrite(t, filepath.Join(root, "ietf", "core", "STALE-STD"), "old standard\n")
	if _, removed, err := eng.SyncStandard(src); err != nil || removed != 0 {
		t.Fatalf("first manifest-less sync must prune nothing: removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "ietf", "core", "STALE-STD")); err != nil {
		t.Fatal("manifest-less file was pruned")
	}
}
