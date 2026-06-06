/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mibimport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/no42-org/blittermib/internal/store"
)

// placeCurated writes content directly into the curated tree (no
// import pipeline), mirroring a tree that was populated out of band —
// the state SyncCorpus must reconcile.
func placeCurated(t *testing.T, e *Engine, rel, content string) string {
	t.Helper()
	abs := filepath.Join(e.Root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestSyncCorpusCompilesThenNoRecompile: a curated MIB with no
// fingerprint is compiled on the first sync; the second sync trusts
// the fingerprint and compiles nothing. This is the no-recompile
// property the persisted cache rests on.
func TestSyncCorpusCompilesThenNoRecompile(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	placeCurated(t, e, curatedProbeRel, probeMIB)

	// First sync builds the index from scratch: it compiles both the
	// curated probe and the bundled SNMPv2-SMI fixture sitting in the
	// corpus root.
	compiled, removed, err := e.SyncCorpus(ctx)
	if err != nil {
		t.Fatalf("SyncCorpus #1: %v", err)
	}
	if compiled != 2 {
		t.Fatalf("first sync compiled = %d, want 2 (probe + SNMPv2-SMI)", compiled)
	}
	if removed != 0 {
		t.Errorf("first sync removed = %d, want 0", removed)
	}
	if _, err := e.Store.GetModule(ctx, "BLITTERMIB-PROBE-MIB"); err != nil {
		t.Errorf("GetModule after first sync: %v", err)
	}

	compiled, removed, err = e.SyncCorpus(ctx)
	if err != nil {
		t.Fatalf("SyncCorpus #2: %v", err)
	}
	if compiled != 0 {
		t.Errorf("second sync compiled = %d, want 0 (fingerprint match)", compiled)
	}
	if removed != 0 {
		t.Errorf("second sync removed = %d, want 0", removed)
	}
}

// TestSyncCorpusRemovesVanishedSource: deleting the curated file out
// of band makes the next sync drop the fingerprint and the module.
func TestSyncCorpusRemovesVanishedSource(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	abs := placeCurated(t, e, curatedProbeRel, probeMIB)

	// Seed: probe + SNMPv2-SMI fixture.
	if c, _, err := e.SyncCorpus(ctx); err != nil || c != 2 {
		t.Fatalf("seed sync compiled = %d (err %v), want 2", c, err)
	}

	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	compiled, removed, err := e.SyncCorpus(ctx)
	if err != nil {
		t.Fatalf("SyncCorpus after delete: %v", err)
	}
	if compiled != 0 {
		t.Errorf("compiled = %d, want 0", compiled)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := e.Store.GetModule(ctx, "BLITTERMIB-PROBE-MIB"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetModule = %v, want ErrNotFound", err)
	}
}

// TestSyncCorpusRecompilesOnModification: an out-of-band edit (append
// a comment, bump mtime) makes the next sync recompile the file.
func TestSyncCorpusRecompilesOnModification(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	abs := placeCurated(t, e, curatedProbeRel, probeMIB)

	// Seed: probe + SNMPv2-SMI fixture.
	if c, _, err := e.SyncCorpus(ctx); err != nil || c != 2 {
		t.Fatalf("seed sync compiled = %d (err %v), want 2", c, err)
	}

	// Append a comment line and push the mtime forward so the
	// stat-walk diff sees drift (size + mtime both change).
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n-- out-of-band edit\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(abs, future, future); err != nil {
		t.Fatal(err)
	}

	compiled, _, err := e.SyncCorpus(ctx)
	if err != nil {
		t.Fatalf("SyncCorpus after edit: %v", err)
	}
	if compiled != 1 {
		t.Errorf("compiled = %d, want 1 (modified file recompiled)", compiled)
	}
}
