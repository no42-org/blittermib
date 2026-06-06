/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mibimport

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/store"
)

// snmpv2SMI is a hand-written subset of SNMPv2-SMI carrying just the
// symbols the probe fixtures import (the enterprises OID arc plus the
// MODULE-IDENTITY / OBJECT-IDENTITY macros). It is placed in the
// corpus root so IMPORTS resolve under SMIPATH (which the engine sets
// to the corpus dirs only, bypassing libsmi's compiled-in default
// path — see internal/compile/env.go).
const snmpv2SMI = `SNMPv2-SMI DEFINITIONS ::= BEGIN

org            OBJECT IDENTIFIER ::= { iso 3 }
dod            OBJECT IDENTIFIER ::= { org 6 }
internet       OBJECT IDENTIFIER ::= { dod 1 }
private        OBJECT IDENTIFIER ::= { internet 4 }
enterprises    OBJECT IDENTIFIER ::= { private 1 }

MODULE-IDENTITY MACRO ::= BEGIN END
OBJECT-IDENTITY MACRO ::= BEGIN END

END
`

// probeMIB is a self-contained vendor MIB: MODULE-IDENTITY rooted at
// enterprises 99999, so it classifies under vendors/99999-unknown/.
const probeMIB = `BLITTERMIB-PROBE-MIB DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, OBJECT-IDENTITY, enterprises
        FROM SNMPv2-SMI;

probeRoot MODULE-IDENTITY
    LAST-UPDATED "202605030000Z"
    ORGANIZATION "blittermib probe"
    CONTACT-INFO "test@example.invalid"
    DESCRIPTION  "Probe MIB for the import pipeline test."
    ::= { enterprises 99999 }

probeObject OBJECT-IDENTITY
    STATUS  current
    DESCRIPTION "Probe object."
    ::= { probeRoot 1 }

END
`

// probeMIBv2 declares the same module name as probeMIB but with
// different content (a different enterprise arc + descriptor), to
// exercise the "module already exists; content differs" duplicate.
const probeMIBv2 = `BLITTERMIB-PROBE-MIB DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, OBJECT-IDENTITY, enterprises
        FROM SNMPv2-SMI;

probeRoot MODULE-IDENTITY
    LAST-UPDATED "202605030000Z"
    ORGANIZATION "blittermib probe v2"
    CONTACT-INFO "test@example.invalid"
    DESCRIPTION  "Probe MIB v2 with materially different content."
    ::= { enterprises 88888 }

probeOther OBJECT-IDENTITY
    STATUS  current
    DESCRIPTION "A different probe object."
    ::= { probeRoot 2 }

END
`

// curatedProbeRel is where the probe MIB lands in the curated tree.
const curatedProbeRel = "vendors/99999-unknown/BLITTERMIB-PROBE-MIB"

// newEngine builds a fresh engine over a temp corpus root with the
// bundled SNMPv2-SMI subset already in place, a live store, and lint
// disabled. Skips the whole test when smidump is absent.
func newEngine(t *testing.T) *Engine {
	t.Helper()
	if _, err := exec.LookPath("smidump"); err != nil {
		t.Skip("smidump not on PATH — skipping import pipeline test")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ietf", "core"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ietf", "core", "SNMPv2-SMI"),
		[]byte(snmpv2SMI), 0o640); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	e := New(root, st, mibcorpus.GroupMap{})
	e.Smilint = "" // skip lint in tests
	if err := e.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	return e
}

// drop writes content into the intake dir under the given filename and
// returns its path.
func drop(t *testing.T, e *Engine, name, content string) string {
	t.Helper()
	p := filepath.Join(e.Dir(), name)
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatalf("drop %s: %v", name, err)
	}
	return p
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to exist: %s (%v)", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be absent: %s (err=%v)", path, err)
	}
}

// TestImportValidVendorMIB: a valid vendor MIB dropped in import/ is
// imported, moved to its curated path, removed from import/, and
// recorded in the store (module + source_file row).
func TestImportValidVendorMIB(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p := drop(t, e, "BLITTERMIB-PROBE-MIB", probeMIB)

	outs := e.Import(ctx, []string{p})
	if len(outs) != 1 {
		t.Fatalf("got %d outcomes, want 1: %+v", len(outs), outs)
	}
	oc := outs[0]
	if oc.Status != StatusImported {
		t.Fatalf("status = %s, want imported (reason %q)", oc.Status, oc.Reason)
	}
	if oc.Dest != curatedProbeRel {
		t.Errorf("Dest = %q, want %q", oc.Dest, curatedProbeRel)
	}
	if oc.Module == nil || oc.Module.Name != "BLITTERMIB-PROBE-MIB" {
		t.Errorf("Module = %+v, want name BLITTERMIB-PROBE-MIB", oc.Module)
	}

	// File at the curated path, gone from import/.
	mustExist(t, filepath.Join(e.Root, curatedProbeRel))
	mustNotExist(t, p)

	// Store has the module and a source_file row keyed by the rel path.
	if _, err := e.Store.GetModule(ctx, "BLITTERMIB-PROBE-MIB"); err != nil {
		t.Errorf("GetModule: %v", err)
	}
	files, err := e.Store.ListSourceFiles(ctx)
	if err != nil {
		t.Fatalf("ListSourceFiles: %v", err)
	}
	if _, ok := files[curatedProbeRel]; !ok {
		t.Errorf("no source_file row for %q; have %v", curatedProbeRel, keys(files))
	}

	// And an outcome row.
	rows, err := e.Store.ListImportOutcomes(ctx, 10)
	if err != nil {
		t.Fatalf("ListImportOutcomes: %v", err)
	}
	if len(rows) == 0 || rows[0].Status != string(StatusImported) {
		t.Errorf("outcome rows = %+v, want a recent imported row", rows)
	}
}

// TestImportBrokenMIB: a file with the DEFINITIONS marker but garbage
// content that yields no module identity fails and quarantines in
// import/failed/ with a reason sidecar.
func TestImportBrokenMIB(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	// Marker present (passes the sniffer) but no parseable module —
	// smidump emits no <module> element ("unable to determine SMI
	// version"), so the engine fails it.
	broken := "random junk line\nmore junk DEFINITIONS ::= BEGIN garbage\n????\n"
	drop(t, e, "BROKEN-MIB", broken)

	outs := e.Import(ctx, e.mustPending(t))
	oc := single(t, outs)
	if oc.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", oc.Status)
	}
	if oc.Reason == "" {
		t.Error("failed outcome has no reason")
	}

	failed := filepath.Join(e.FailedDir(), "BROKEN-MIB")
	mustExist(t, failed)
	mustExist(t, failed+sidecarSuffix)

	var sc Sidecar
	readSidecar(t, failed, &sc)
	if sc.Status != StatusFailed || sc.Reason == "" {
		t.Errorf("sidecar = %+v, want failed status with reason", sc)
	}
}

// TestImportNonMIB: a file without the DEFINITIONS marker fails with a
// "not a MIB" reason.
func TestImportNonMIB(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	drop(t, e, "README.txt", "This is just documentation, not a MIB.\n")

	oc := single(t, e.Import(ctx, e.mustPending(t)))
	if oc.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", oc.Status)
	}
	if !strings.HasPrefix(oc.Reason, "not a MIB") {
		t.Errorf("reason = %q, want prefix %q", oc.Reason, "not a MIB")
	}
	mustExist(t, filepath.Join(e.FailedDir(), "README.txt"))
}

// TestImportByteIdenticalDuplicate: a byte-identical copy under a
// different filename quarantines as a duplicate, with Existing set to
// the curated rel path of the original.
func TestImportByteIdenticalDuplicate(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	first := drop(t, e, "BLITTERMIB-PROBE-MIB", probeMIB)
	if oc := single(t, e.Import(ctx, []string{first})); oc.Status != StatusImported {
		t.Fatalf("first import status = %s, want imported (reason %q)", oc.Status, oc.Reason)
	}

	dupName := "PROBE-COPY.mib"
	drop(t, e, dupName, probeMIB) // identical bytes, different name
	oc := single(t, e.Import(ctx, e.mustPending(t)))
	if oc.Status != StatusDuplicate {
		t.Fatalf("status = %s, want duplicate (reason %q)", oc.Status, oc.Reason)
	}
	if oc.Existing != curatedProbeRel {
		t.Errorf("Existing = %q, want %q", oc.Existing, curatedProbeRel)
	}
	mustExist(t, filepath.Join(e.DuplicateDir(), dupName))
	mustExist(t, filepath.Join(e.DuplicateDir(), dupName)+sidecarSuffix)
}

// TestImportSameNameDifferentContent: a second module with the same
// name but different content quarantines as a duplicate with a
// "content differs" reason.
func TestImportSameNameDifferentContent(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	first := drop(t, e, "BLITTERMIB-PROBE-MIB", probeMIB)
	if oc := single(t, e.Import(ctx, []string{first})); oc.Status != StatusImported {
		t.Fatalf("first import status = %s, want imported (reason %q)", oc.Status, oc.Reason)
	}

	drop(t, e, "PROBE-V2.mib", probeMIBv2) // same module name, different bytes
	oc := single(t, e.Import(ctx, e.mustPending(t)))
	if oc.Status != StatusDuplicate {
		t.Fatalf("status = %s, want duplicate (reason %q)", oc.Status, oc.Reason)
	}
	if !strings.Contains(oc.Reason, "content differs") {
		t.Errorf("reason = %q, want it to mention content differs", oc.Reason)
	}
}

// TestImportReplacing: re-running a quarantined duplicate with
// replacement allowed imports it and updates the curated file.
func TestImportReplacing(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	first := drop(t, e, "BLITTERMIB-PROBE-MIB", probeMIB)
	if oc := single(t, e.Import(ctx, []string{first})); oc.Status != StatusImported {
		t.Fatalf("first import status = %s, want imported (reason %q)", oc.Status, oc.Reason)
	}

	dup := drop(t, e, "PROBE-V2.mib", probeMIBv2)
	if oc := single(t, e.Import(ctx, []string{dup})); oc.Status != StatusDuplicate {
		t.Fatalf("dup import status = %s, want duplicate", oc.Status)
	}
	quarantined := filepath.Join(e.DuplicateDir(), "PROBE-V2.mib")
	mustExist(t, quarantined)

	oc := e.ImportReplacing(ctx, quarantined)
	if oc.Status != StatusImported {
		t.Fatalf("replace status = %s, want imported (reason %q)", oc.Status, oc.Reason)
	}
	// v2 routes to the 88888 vendor dir; the curated file is the
	// updated content.
	curated := filepath.Join(e.Root, oc.Dest)
	mustExist(t, curated)
	body, err := os.ReadFile(curated)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "probe v2") {
		t.Errorf("curated file not updated to v2 content; got %q", truncate(string(body)))
	}
	mustNotExist(t, quarantined)
}

// TestImportIgnoresSubdir: a file nested in a subdirectory of import/
// is never listed by Pending and so never processed.
func TestImportIgnoresSubdir(t *testing.T) {
	e := newEngine(t)
	sub := filepath.Join(e.Dir(), "somedir")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(sub, "BLITTERMIB-PROBE-MIB")
	if err := os.WriteFile(nested, []byte(probeMIB), 0o640); err != nil {
		t.Fatal(err)
	}

	pending, err := e.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	for _, p := range pending {
		if strings.Contains(p, "somedir") {
			t.Errorf("Pending listed a nested file: %s", p)
		}
	}
	// The nested file is left untouched.
	mustExist(t, nested)
}

// TestSweepTmp: SweepTmp removes regular files staged in import/.tmp/.
func TestSweepTmp(t *testing.T) {
	e := newEngine(t)
	for _, name := range []string{"a.upload", "b.upload"} {
		if err := os.WriteFile(filepath.Join(e.TmpDir(), name), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	n, err := e.SweepTmp()
	if err != nil {
		t.Fatalf("SweepTmp: %v", err)
	}
	if n != 2 {
		t.Errorf("removed = %d, want 2", n)
	}
	entries, _ := os.ReadDir(e.TmpDir())
	if len(entries) != 0 {
		t.Errorf("tmp dir not empty after sweep: %v", entries)
	}
}

// --- small helpers -------------------------------------------------

func (e *Engine) mustPending(t *testing.T) []string {
	t.Helper()
	p, err := e.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	return p
}

func single(t *testing.T, outs []Outcome) Outcome {
	t.Helper()
	if len(outs) != 1 {
		t.Fatalf("got %d outcomes, want 1: %+v", len(outs), outs)
	}
	return outs[0]
}

func readSidecar(t *testing.T, file string, sc *Sidecar) {
	t.Helper()
	b, err := os.ReadFile(file + sidecarSuffix)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if err := json.Unmarshal(b, sc); err != nil {
		t.Fatalf("unmarshal sidecar: %v", err)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
