package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedCorpus writes a small fixture corpus into a tempdir and returns
// the path. The corpus has one Cisco vendor MIB, one IETF MIB, one
// MIB without a copyright header (→ unknown license), and an
// `_overrides.yaml` that pins the third entry.
func seedCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cisco := `-- Copyright (c) 2024 Cisco Systems, Inc.

CISCO-EXAMPLE-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE
        FROM SNMPv2-SMI
    CISCO-SMI
        FROM CISCO-SMI;

END
`
	mustWrite(t, filepath.Join(dir, "vendors/9-cisco/CISCO-EXAMPLE-MIB"), cisco)

	ifmib := `-- Copyright (c) 2009 The Internet Society

IF-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, Integer32
        FROM SNMPv2-SMI
    DisplayString
        FROM SNMPv2-TC;

END
`
	mustWrite(t, filepath.Join(dir, "ietf/interfaces/IF-MIB"), ifmib)

	bare := `BARE-VENDOR-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY
        FROM SNMPv2-SMI;

END
`
	mustWrite(t, filepath.Join(dir, "vendors/61509-no42/BARE-VENDOR-MIB"), bare)

	overrides := `licenses:
  BARE-VENDOR-MIB: vendor-public
`
	mustWrite(t, filepath.Join(dir, "_overrides.yaml"), overrides)

	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestIndexGenerationDeterministic seeds a tiny corpus, runs the
// generator twice with the same fixed --date, and asserts byte-for-
// byte identical output. This is the determinism-on-stable-input
// guarantee from the spec.
func TestIndexGenerationDeterministic(t *testing.T) {
	dir := seedCorpus(t)
	out1 := filepath.Join(t.TempDir(), "INDEX.yaml")
	out2 := filepath.Join(t.TempDir(), "INDEX.yaml")
	args := func(out string) []string {
		return []string{
			"--root", dir,
			"--out", out,
			"--overrides", filepath.Join(dir, "_overrides.yaml"),
			"--date", "2026-05-06",
		}
	}
	if err := indexCmd(args(out1)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := indexCmd(args(out2)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	a, _ := os.ReadFile(out1)
	b, _ := os.ReadFile(out2)
	if !bytes.Equal(a, b) {
		t.Errorf("two runs produced different output\n--- run 1 ---\n%s\n--- run 2 ---\n%s", a, b)
	}
}

// TestIndexEntryFields verifies the per-entry shape against the
// fixture corpus: PEN/vendor for Cisco, override-wins for the third
// MIB, IETF entry has no pen/vendor, imports are sorted/deduped.
func TestIndexEntryFields(t *testing.T) {
	dir := seedCorpus(t)
	out := filepath.Join(t.TempDir(), "INDEX.yaml")
	if err := indexCmd([]string{
		"--root", dir,
		"--out", out,
		"--overrides", filepath.Join(dir, "_overrides.yaml"),
		"--date", "2026-05-06",
	}); err != nil {
		t.Fatalf("indexCmd: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	// File-sort order: ietf/interfaces/IF-MIB < vendors/61509-* < vendors/9-cisco/*
	// (lexicographic puts "61509" < "9" because '6' < '9').
	wantOrder := []string{
		"file: ietf/interfaces/IF-MIB",
		"file: vendors/61509-no42/BARE-VENDOR-MIB",
		"file: vendors/9-cisco/CISCO-EXAMPLE-MIB",
	}
	prev := -1
	for _, marker := range wantOrder {
		idx := strings.Index(got, marker)
		if idx < 0 {
			t.Errorf("missing entry %q\noutput:\n%s", marker, got)
			continue
		}
		if idx <= prev {
			t.Errorf("entries out of order: %q at %d, prev %d", marker, idx, prev)
		}
		prev = idx
	}

	// Cisco entry has pen + vendor.
	must := []string{
		"file: vendors/9-cisco/CISCO-EXAMPLE-MIB",
		"module: CISCO-EXAMPLE-MIB",
		"pen: 9",
		"vendor: cisco",
		"license: cisco",
	}
	for _, m := range must {
		if !strings.Contains(got, m) {
			t.Errorf("Cisco entry missing %q\noutput:\n%s", m, got)
		}
	}

	// Override wins for BARE-VENDOR-MIB.
	if !strings.Contains(got, "license: vendor-public") {
		t.Errorf("override license not applied; output:\n%s", got)
	}

	// IETF MIB has no pen/vendor lines.
	ifSection := sectionByMarker(got, "file: ietf/interfaces/IF-MIB")
	if strings.Contains(ifSection, "pen:") {
		t.Errorf("IETF entry should not have pen: line; section:\n%s", ifSection)
	}
	if strings.Contains(ifSection, "vendor:") {
		t.Errorf("IETF entry should not have vendor: line; section:\n%s", ifSection)
	}
	if !strings.Contains(ifSection, "license: rfc-editor") {
		t.Errorf("IETF entry license should be rfc-editor; section:\n%s", ifSection)
	}

	// Imports are emitted in flow style and sorted/deduped.
	if !strings.Contains(got, "imports: [SNMPv2-SMI, SNMPv2-TC]") {
		t.Errorf("IETF entry imports not as expected; output:\n%s", got)
	}
}

// sectionByMarker returns the substring from `marker` to the next
// `  - file:` line (or end of input).
func sectionByMarker(s, marker string) string {
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	tail := s[i:]
	j := strings.Index(tail[len(marker):], "  - file:")
	if j < 0 {
		return tail
	}
	return tail[:len(marker)+j]
}

// TestIndexAddedInPreserved asserts that re-running the generator
// after a new MIB lands keeps existing entries' added_in dates
// unchanged.
func TestIndexAddedInPreserved(t *testing.T) {
	dir := seedCorpus(t)
	out := filepath.Join(t.TempDir(), "INDEX.yaml")

	// First run: today = 2026-01-01.
	if err := indexCmd([]string{
		"--root", dir, "--out", out,
		"--overrides", filepath.Join(dir, "_overrides.yaml"),
		"--date", "2026-01-01",
	}); err != nil {
		t.Fatal(err)
	}

	// Add a new MIB.
	mustWrite(t, filepath.Join(dir, "ietf/core/SNMPv2-SMI"),
		"-- Copyright (c) 2009 The Internet Society\nSNMPv2-SMI DEFINITIONS ::= BEGIN\nEND\n")

	// Second run: today = 2026-05-06 (different date).
	if err := indexCmd([]string{
		"--root", dir, "--out", out,
		"--overrides", filepath.Join(dir, "_overrides.yaml"),
		"--date", "2026-05-06",
	}); err != nil {
		t.Fatal(err)
	}

	body, _ := os.ReadFile(out)
	got := string(body)

	// IF-MIB existed in run 1 → keeps 2026-01-01.
	if !strings.Contains(got, "file: ietf/interfaces/IF-MIB\n    module: IF-MIB\n    license: rfc-editor\n    imports: [SNMPv2-SMI, SNMPv2-TC]\n    status: current\n    added_in: 2026-01-01") {
		t.Errorf("IF-MIB added_in not preserved across runs:\n%s", got)
	}
	// SNMPv2-SMI is new → gets 2026-05-06.
	if !strings.Contains(got, "file: ietf/core/SNMPv2-SMI") || !strings.Contains(got, "added_in: 2026-05-06") {
		t.Errorf("SNMPv2-SMI new entry missing or has wrong added_in:\n%s", got)
	}
}

// TestLoadOverridesMissing covers the missing-file path: returns an
// empty map without error.
func TestLoadOverridesMissing(t *testing.T) {
	o, err := LoadOverrides(filepath.Join(t.TempDir(), "no.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := o.LicenseFor("ANY"); got != "" {
		t.Errorf("missing overrides should yield empty license, got %q", got)
	}
}
