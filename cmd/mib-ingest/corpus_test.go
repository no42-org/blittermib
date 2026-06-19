/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCorpusIndex(t *testing.T) {
	t.Run("missing file returns nil", func(t *testing.T) {
		root := t.TempDir() // no mibs/INDEX.yaml
		got := LoadCorpusIndex(root)
		if got != nil {
			t.Errorf("missing INDEX.yaml should return nil, got %v", got)
		}
	})

	t.Run("malformed YAML returns nil", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "mibs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "mibs", "INDEX.yaml"),
			[]byte("not: valid: yaml: at: all: ::: \xff\xfe"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := LoadCorpusIndex(root)
		if got != nil {
			t.Errorf("malformed INDEX.yaml should return nil, got %v", got)
		}
	})

	t.Run("well-formed YAML parses correctly", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "mibs"), 0o755); err != nil {
			t.Fatal(err)
		}
		body := `mibs:
  - file: ietf/core/SNMPv2-SMI
    module: SNMPv2-SMI
    license: rfc-editor
    imports: []
    status: current
    last_updated: 199912100000Z
    added_in: 2026-05-07
  - file: ietf/other/IF-MIB
    module: IF-MIB
    license: rfc-editor
    imports: [SNMPv2-SMI]
    status: current
    last_updated: 200702221500Z
    added_in: 2026-05-07
  - file: ietf/other/RFC-1212
    module: RFC-1212
    license: unknown
    imports: []
    status: current
    added_in: 2026-05-07
`
		if err := os.WriteFile(filepath.Join(root, "mibs", "INDEX.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		got := LoadCorpusIndex(root)
		if got == nil {
			t.Fatal("expected non-nil map")
		}
		if v := got["SNMPv2-SMI"]; v != "199912100000Z" {
			t.Errorf("SNMPv2-SMI last_updated = %q", v)
		}
		if v := got["IF-MIB"]; v != "200702221500Z" {
			t.Errorf("IF-MIB last_updated = %q", v)
		}
		// Module with no last_updated → empty string in map.
		if v, ok := got["RFC-1212"]; !ok || v != "" {
			t.Errorf("RFC-1212 should map to empty string, got %q (present=%v)", v, ok)
		}
	})

	t.Run("empty mibs list returns empty map", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "mibs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "mibs", "INDEX.yaml"),
			[]byte("mibs: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := LoadCorpusIndex(root)
		if got == nil {
			t.Fatal("expected non-nil empty map")
		}
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})
}

func TestFindCorpusCollisions(t *testing.T) {
	t.Run("nil index → empty findings", func(t *testing.T) {
		parsed := []result{r("a", "X-MIB", "", "202205101200Z", "sha-1")}
		got := findCorpusCollisions(parsed, nil)
		if len(got) != 0 {
			t.Errorf("nil index must yield no findings, got %d", len(got))
		}
	})

	t.Run("no name match → no finding", func(t *testing.T) {
		parsed := []result{r("a", "X-MIB", "", "202205101200Z", "sha-1")}
		idx := map[string]string{"Y-MIB": "201901010000Z"}
		got := findCorpusCollisions(parsed, idx)
		if len(got) != 0 {
			t.Errorf("no name match must yield no finding, got %d", len(got))
		}
	})

	t.Run("upload newer than corpus → corpus-older / warn", func(t *testing.T) {
		parsed := []result{r("a", "MY-VENDOR-MIB", "", "202408300000Z", "sha-new")}
		idx := map[string]string{"MY-VENDOR-MIB": "201901010000Z"}
		got := findCorpusCollisions(parsed, idx)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(got))
		}
		if got[0].Severity != SeverityWarn {
			t.Errorf("severity = %s", got[0].Severity)
		}
		if got[0].Detail["label"] != "corpus-older" {
			t.Errorf("label = %v", got[0].Detail["label"])
		}
	})

	t.Run("corpus newer than upload → corpus-newer / warn", func(t *testing.T) {
		parsed := []result{r("a", "X-MIB", "", "201803191200Z", "sha-old")}
		idx := map[string]string{"X-MIB": "202205101200Z"}
		got := findCorpusCollisions(parsed, idx)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding")
		}
		if got[0].Detail["label"] != "corpus-newer" || got[0].Severity != SeverityWarn {
			t.Errorf("label/severity = %v / %s", got[0].Detail["label"], got[0].Severity)
		}
	})

	t.Run("equal non-empty → same-revision / info", func(t *testing.T) {
		parsed := []result{r("a", "X-MIB", "", "202205101200Z", "sha-1")}
		idx := map[string]string{"X-MIB": "202205101200Z"}
		got := findCorpusCollisions(parsed, idx)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding")
		}
		if got[0].Detail["label"] != "same-revision" || got[0].Severity != SeverityInfo {
			t.Errorf("label/severity = %v / %s", got[0].Detail["label"], got[0].Severity)
		}
	})

	t.Run("either side empty → unknown / warn", func(t *testing.T) {
		// Upload empty (SMIv1 module without MODULE-IDENTITY).
		parsed := []result{r("a", "X-MIB", "", "", "sha-1")}
		idx := map[string]string{"X-MIB": "202205101200Z"}
		got := findCorpusCollisions(parsed, idx)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding")
		}
		if got[0].Detail["label"] != "unknown" || got[0].Severity != SeverityWarn {
			t.Errorf("upload-empty: label/severity = %v / %s", got[0].Detail["label"], got[0].Severity)
		}

		// Corpus empty (stale corpus entry).
		parsed2 := []result{r("a", "X-MIB", "", "202205101200Z", "sha-1")}
		idx2 := map[string]string{"X-MIB": ""}
		got2 := findCorpusCollisions(parsed2, idx2)
		if got2[0].Detail["label"] != "unknown" {
			t.Errorf("corpus-empty: label = %v", got2[0].Detail["label"])
		}
	})

	t.Run("SMIv1 upload normalises before comparing with SMIv2 corpus", func(t *testing.T) {
		// 9908311200Z (SMIv1, year 99) → 199908311200Z, vs corpus
		// 200101011200Z → upload is older → label = corpus-newer.
		parsed := []result{r("a", "X-MIB", "", "9908311200Z", "sha-1")}
		idx := map[string]string{"X-MIB": "200101011200Z"}
		got := findCorpusCollisions(parsed, idx)
		if len(got) != 1 {
			t.Fatalf("expected 1 finding")
		}
		if got[0].Detail["label"] != "corpus-newer" {
			t.Errorf("SMIv1→SMIv2 promotion failed; label = %v (upload_norm=%v corpus_norm=%v)",
				got[0].Detail["label"],
				got[0].Detail["upload_last_updated"],
				got[0].Detail["corpus_last_updated"])
		}
		if got[0].Detail["upload_last_updated"] != "199908311200Z" {
			t.Errorf("upload normalised = %v", got[0].Detail["upload_last_updated"])
		}
		if got[0].Detail["upload_last_updated_raw"] != "9908311200Z" {
			t.Errorf("upload raw lost; got %v", got[0].Detail["upload_last_updated_raw"])
		}
	})

	t.Run("empty module name skipped", func(t *testing.T) {
		parsed := []result{r("a", "", "", "202205101200Z", "sha-1")}
		idx := map[string]string{"X-MIB": "202205101200Z"}
		got := findCorpusCollisions(parsed, idx)
		if len(got) != 0 {
			t.Errorf("empty moduleName must yield no finding, got %d", len(got))
		}
	})
}

func TestCorpusCollisionLabel(t *testing.T) {
	cases := []struct {
		name, upload, corpus, wantLabel, wantSev string
	}{
		{"both empty", "", "", "unknown", SeverityWarn},
		{"upload empty", "", "202205101200Z", "unknown", SeverityWarn},
		{"corpus empty", "202205101200Z", "", "unknown", SeverityWarn},
		{"equal", "202205101200Z", "202205101200Z", "same-revision", SeverityInfo},
		{"upload < corpus", "201803191200Z", "202205101200Z", "corpus-newer", SeverityWarn},
		{"upload > corpus", "202408300000Z", "201901010000Z", "corpus-older", SeverityWarn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			label, sev := corpusCollisionLabel(c.upload, c.corpus)
			if label != c.wantLabel || sev != c.wantSev {
				t.Errorf("corpusCollisionLabel(%q,%q) = (%s,%s), want (%s,%s)",
					c.upload, c.corpus, label, sev, c.wantLabel, c.wantSev)
			}
		})
	}
}
