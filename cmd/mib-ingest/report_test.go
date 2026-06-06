/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// r is a small builder for the test fixtures so each test case
// reads as a flat list of (src, moduleName, oidRoot, lastUpdated, sha).
func r(src, mod, oid, lu, sha string) result {
	return result{
		src:         src,
		sha:         sha,
		size:        1024,
		moduleName:  mod,
		oidRoot:     oid,
		lastUpdated: lu,
	}
}

func TestFindByteIdentical(t *testing.T) {
	t.Run("two identical files in different archives", func(t *testing.T) {
		srcRoot := filepath.FromSlash("mibs/import")
		parsed := []result{
			r(filepath.FromSlash("mibs/import/archive-a/SNMPv2-SMI.txt"), "SNMPv2-SMI", "", "", "sha-abc"),
			r(filepath.FromSlash("mibs/import/archive-b/SNMPv2-SMI.txt"), "SNMPv2-SMI", "", "", "sha-abc"),
		}
		got := findByteIdentical(parsed, srcRoot)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(got))
		}
		f := got[0]
		if f.Category != CategoryByteIdentical || f.Severity != SeverityInfo {
			t.Errorf("category/severity = %s/%s", f.Category, f.Severity)
		}
		if got, want := f.Detail["cross_directory"], true; got != want {
			t.Errorf("cross_directory = %v, want %v", got, want)
		}
		if f.Detail["hash"] != "sha-abc" {
			t.Errorf("hash = %v, want sha-abc", f.Detail["hash"])
		}
		// Sources must be lex-sorted.
		want := []string{
			filepath.FromSlash("mibs/import/archive-a/SNMPv2-SMI.txt"),
			filepath.FromSlash("mibs/import/archive-b/SNMPv2-SMI.txt"),
		}
		if !reflect.DeepEqual(f.Sources, want) {
			t.Errorf("sources = %v, want %v", f.Sources, want)
		}
	})

	t.Run("same-parent duplicates → cross_directory false", func(t *testing.T) {
		srcRoot := filepath.FromSlash("mibs/import")
		parsed := []result{
			r(filepath.FromSlash("mibs/import/foo.mib"), "X-MIB", "", "", "sha-x"),
			r(filepath.FromSlash("mibs/import/bar.mib"), "X-MIB", "", "", "sha-x"),
		}
		got := findByteIdentical(parsed, srcRoot)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding")
		}
		if got[0].Detail["cross_directory"] != false {
			t.Errorf("cross_directory should be false for same-parent")
		}
	})

	t.Run("singletons emit nothing", func(t *testing.T) {
		parsed := []result{
			r("a", "A", "", "", "sha-1"),
			r("b", "B", "", "", "sha-2"),
		}
		got := findByteIdentical(parsed, "")
		if len(got) != 0 {
			t.Errorf("expected 0 findings, got %d", len(got))
		}
	})

	t.Run("empty sha is skipped", func(t *testing.T) {
		parsed := []result{
			r("a", "A", "", "", ""),
			r("b", "B", "", "", ""),
		}
		got := findByteIdentical(parsed, "")
		if len(got) != 0 {
			t.Errorf("empty-sha entries must not group; got %d findings", len(got))
		}
	})
}

func TestFindModuleNameCollisions(t *testing.T) {
	t.Run("two revisions of CISCO-FOO-MIB → one warn finding", func(t *testing.T) {
		parsed := []result{
			r("archive-a/CISCO-FOO-MIB.mib", "CISCO-FOO-MIB", "1.3.6.1.4.1.9.99.42", "201803191200Z", "sha-old"),
			r("archive-b/CISCO-FOO-MIB.mib", "CISCO-FOO-MIB", "1.3.6.1.4.1.9.99.42", "202205101200Z", "sha-new"),
		}
		got := findModuleNameCollisions(parsed)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(got))
		}
		f := got[0]
		if f.Category != CategoryModuleNameCollision || f.Severity != SeverityWarn {
			t.Errorf("category/severity = %s/%s", f.Category, f.Severity)
		}
		if f.ModuleName != "CISCO-FOO-MIB" {
			t.Errorf("module_name = %s", f.ModuleName)
		}
		candidates, ok := f.Detail["candidates"].([]map[string]any)
		if !ok || len(candidates) != 2 {
			t.Fatalf("candidates = %v", f.Detail["candidates"])
		}
		// Lexicographic ordering by source.
		if candidates[0]["source"] != "archive-a/CISCO-FOO-MIB.mib" {
			t.Errorf("first candidate source = %v", candidates[0]["source"])
		}
		if candidates[0]["last_updated_raw"] != "201803191200Z" {
			t.Errorf("first candidate raw = %v", candidates[0]["last_updated_raw"])
		}
		if candidates[0]["last_updated_normalised"] != "201803191200Z" {
			t.Errorf("first candidate normalised = %v", candidates[0]["last_updated_normalised"])
		}
	})

	t.Run("overlap suppression: all share one sha → finding suppressed", func(t *testing.T) {
		parsed := []result{
			r("a", "X-MIB", "", "", "sha-same"),
			r("b", "X-MIB", "", "", "sha-same"),
		}
		got := findModuleNameCollisions(parsed)
		if len(got) != 0 {
			t.Errorf("expected suppression; got %d findings", len(got))
		}
	})

	t.Run("mixed hashes → finding emitted", func(t *testing.T) {
		parsed := []result{
			r("a", "Y-MIB", "", "", "sha-1"),
			r("b", "Y-MIB", "", "", "sha-2"),
		}
		got := findModuleNameCollisions(parsed)
		if len(got) != 1 {
			t.Errorf("mixed hashes must emit; got %d findings", len(got))
		}
	})

	t.Run("singleton emits nothing", func(t *testing.T) {
		parsed := []result{r("a", "Z-MIB", "", "", "sha-1")}
		if got := findModuleNameCollisions(parsed); len(got) != 0 {
			t.Errorf("singleton must not emit; got %d", len(got))
		}
	})
}

func TestFindOIDArcSharing(t *testing.T) {
	t.Run("vendor rename: OLD-FOO and NEW-FOO share one OID", func(t *testing.T) {
		parsed := []result{
			r("OLD-FOO-MIB.mib", "OLD-FOO-MIB", "1.3.6.1.4.1.9.99.42", "201803191200Z", "sha-1"),
			r("NEW-FOO-MIB.mib", "NEW-FOO-MIB", "1.3.6.1.4.1.9.99.42", "202301010000Z", "sha-2"),
		}
		got := findOIDArcSharing(parsed)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(got))
		}
		f := got[0]
		if f.Category != CategoryOIDArcSharing || f.Severity != SeverityWarn {
			t.Errorf("category/severity = %s/%s", f.Category, f.Severity)
		}
		if f.Detail["oid"] != "1.3.6.1.4.1.9.99.42" {
			t.Errorf("oid = %v", f.Detail["oid"])
		}
		names := f.Detail["module_names"].([]string)
		sort.Strings(names)
		if !reflect.DeepEqual(names, []string{"NEW-FOO-MIB", "OLD-FOO-MIB"}) {
			t.Errorf("module_names = %v", names)
		}
	})

	t.Run("same module name on same OID → no oid-arc finding", func(t *testing.T) {
		parsed := []result{
			r("a", "CISCO-FOO", "1.3.6.1.4.1.9.99.42", "", "sha-1"),
			r("b", "CISCO-FOO", "1.3.6.1.4.1.9.99.42", "", "sha-2"),
		}
		if got := findOIDArcSharing(parsed); len(got) != 0 {
			t.Errorf("single-name OID group must not emit; got %d", len(got))
		}
	})

	t.Run("empty OIDRoot does not produce a spurious finding", func(t *testing.T) {
		parsed := []result{
			r("a", "TC-1", "", "", "sha-1"),
			r("b", "TC-2", "", "", "sha-2"),
			r("c", "TC-3", "", "", "sha-3"),
		}
		if got := findOIDArcSharing(parsed); len(got) != 0 {
			t.Errorf("empty OIDRoot group must be excluded; got %d findings", len(got))
		}
	})
}

func TestFindDivergentIdentity(t *testing.T) {
	t.Run("same name + same LAST-UPDATED + different bytes → error finding", func(t *testing.T) {
		parsed := []result{
			r("a", "CISCO-FOO", "", "202205101200Z", "sha-a"),
			r("b", "CISCO-FOO", "", "202205101200Z", "sha-b"),
		}
		got := findDivergentIdentity(parsed)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(got))
		}
		if got[0].Severity != SeverityError {
			t.Errorf("severity = %s, want %s", got[0].Severity, SeverityError)
		}
	})

	t.Run("same name + same LAST-UPDATED + identical bytes → no finding", func(t *testing.T) {
		parsed := []result{
			r("a", "X", "", "202205101200Z", "sha-same"),
			r("b", "X", "", "202205101200Z", "sha-same"),
		}
		if got := findDivergentIdentity(parsed); len(got) != 0 {
			t.Errorf("identical bytes must not be flagged; got %d", len(got))
		}
	})

	t.Run("empty LAST-UPDATED → no finding (SMIv1 guard)", func(t *testing.T) {
		parsed := []result{
			r("a", "Y", "", "", "sha-1"),
			r("b", "Y", "", "", "sha-2"),
		}
		if got := findDivergentIdentity(parsed); len(got) != 0 {
			t.Errorf("empty LU must not be flagged; got %d", len(got))
		}
	})

	t.Run("SMIv1 LU promoted via normaliser groups with SMIv2 form", func(t *testing.T) {
		parsed := []result{
			// 9908311200Z (SMIv1) normalises to 199908311200Z; pair against
			// a real SMIv2 199908311200Z: same after normalisation, diff bytes.
			r("a", "X", "", "9908311200Z", "sha-a"),
			r("b", "X", "", "199908311200Z", "sha-b"),
		}
		got := findDivergentIdentity(parsed)
		if len(got) != 1 {
			t.Errorf("expected 1 finding (normalisation should bucket them); got %d", len(got))
		}
	})
}

func TestFindBrokenAndNonMIB(t *testing.T) {
	t.Run("parse error → broken / warn", func(t *testing.T) {
		input := []result{
			{
				src:     "mibs/import/BROKEN.mib",
				outcome: outcomeParseError,
				reason:  "smidump failed: syntax error at line 47",
			},
		}
		got := findBrokenAndNonMIB(input)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(got))
		}
		f := got[0]
		if f.Category != CategoryBroken || f.Severity != SeverityWarn {
			t.Errorf("category/severity = %s/%s", f.Category, f.Severity)
		}
		if len(f.Sources) != 1 || f.Sources[0] != "mibs/import/BROKEN.mib" {
			t.Errorf("sources = %v", f.Sources)
		}
		if f.Detail["reason"] != "smidump failed: syntax error at line 47" {
			t.Errorf("reason = %v", f.Detail["reason"])
		}
	})

	t.Run("non-mib skip → non-mib / info", func(t *testing.T) {
		input := []result{
			{
				src:     "mibs/import/README.txt",
				outcome: outcomeSkippedNonMIB,
				reason:  "no MIB marker (DEFINITIONS ::= BEGIN absent in first 32 KB)",
			},
		}
		got := findBrokenAndNonMIB(input)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding")
		}
		f := got[0]
		if f.Category != CategoryNonMIB || f.Severity != SeverityInfo {
			t.Errorf("category/severity = %s/%s", f.Category, f.Severity)
		}
		if f.Detail["reason"] != "no MIB marker (DEFINITIONS ::= BEGIN absent in first 32 KB)" {
			t.Errorf("reason = %v", f.Detail["reason"])
		}
	})

	t.Run("mixed input → both categories", func(t *testing.T) {
		input := []result{
			{src: "mibs/import/BROKEN.mib", outcome: outcomeParseError, reason: "smidump failed: X"},
			{src: "mibs/import/README.txt", outcome: outcomeSkippedNonMIB, reason: "no MIB marker"},
			{src: "mibs/import/dangling", outcome: outcomeSkippedNonMIB, reason: "read failed: EISDIR"},
		}
		got := findBrokenAndNonMIB(input)
		if len(got) != 3 {
			t.Fatalf("expected 3 findings, got %d", len(got))
		}
		// sortFindings orders by category alphabetically →
		// "broken" < "non-mib".
		if got[0].Category != CategoryBroken {
			t.Errorf("first finding category = %s, want %s", got[0].Category, CategoryBroken)
		}
		if got[1].Category != CategoryNonMIB || got[2].Category != CategoryNonMIB {
			t.Errorf("expected two non-mib findings after broken; got %s,%s",
				got[1].Category, got[2].Category)
		}
		// Within non-mib: lex-sorted by source. "dangling" < "README.txt".
		if got[1].Sources[0] >= got[2].Sources[0] {
			t.Errorf("non-mib findings not lex-sorted: %s vs %s",
				got[1].Sources[0], got[2].Sources[0])
		}
	})

	t.Run("other outcomes are skipped", func(t *testing.T) {
		// outcomeMoved / outcomeRoutedUnsorted / outcomeRefused
		// don't belong in the broken/non-mib rollup — they're
		// successful or refused-collision cases.
		input := []result{
			{src: "x", outcome: outcomeMoved},
			{src: "y", outcome: outcomeRefused, reason: "destination already exists"},
		}
		got := findBrokenAndNonMIB(input)
		if len(got) != 0 {
			t.Errorf("non-error outcomes should be skipped; got %d findings", len(got))
		}
	})

	t.Run("empty input emits no findings", func(t *testing.T) {
		got := findBrokenAndNonMIB(nil)
		if len(got) != 0 {
			t.Errorf("nil input must yield no findings, got %d", len(got))
		}
	})
}

func TestFindingJSONShape(t *testing.T) {
	// Round-trip a finding through encoding/json to verify the wire
	// shape is what the spec mandates (flat object with the named
	// top-level fields).
	f := Finding{
		Category:   CategoryByteIdentical,
		Severity:   SeverityInfo,
		ModuleName: "",
		Sources:    []string{"a", "b"},
		Detail:     map[string]any{"hash": "sha-1", "size": int64(42), "cross_directory": true},
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"category", "severity", "module_name", "sources", "detail"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing top-level key %q in JSON output", key)
		}
	}
}
