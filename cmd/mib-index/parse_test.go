package main

import (
	"reflect"
	"testing"
)

func TestExtractModuleName(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"plain opener", "CISCO-RTTMON-MIB DEFINITIONS ::= BEGIN\n", "CISCO-RTTMON-MIB"},
		{"leading whitespace", "  IF-MIB DEFINITIONS ::= BEGIN\n", "IF-MIB"},
		{"comments before opener", "-- Some\n-- header lines\nIF-MIB DEFINITIONS ::= BEGIN\n", "IF-MIB"},
		{"extra whitespace in opener", "IF-MIB    DEFINITIONS  ::=  BEGIN\n", "IF-MIB"},
		{"no opener", "-- just comments\nfoo bar\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractModuleName([]byte(c.src))
			if got != c.want {
				t.Errorf("extractModuleName(%q) = %q, want %q", c.src, got, c.want)
			}
		})
	}
}

func TestExtractImports(t *testing.T) {
	src := `IF-MIB DEFINITIONS ::= BEGIN
IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, Counter32
        FROM SNMPv2-SMI
    DisplayString, TruthValue, RowStatus
        FROM SNMPv2-TC
    InterfaceIndex
        FROM IF-MIB
    -- ordering shouldn't matter; SNMPv2-SMI appears twice (deduped)
    Counter64
        FROM SNMPv2-SMI;

END
`
	got := extractImports([]byte(src))
	want := []string{"IF-MIB", "SNMPv2-SMI", "SNMPv2-TC"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractImports = %v, want %v", got, want)
	}
}

func TestExtractImportsNoneFound(t *testing.T) {
	// SNMPv2-SMI itself has no IMPORTS.
	src := "SNMPv2-SMI DEFINITIONS ::= BEGIN\n\nEND\n"
	if got := extractImports([]byte(src)); got != nil {
		t.Errorf("extractImports on no-imports module = %v, want nil", got)
	}
}

func TestPENFromPath(t *testing.T) {
	cases := []struct {
		path     string
		wantPEN  uint32
		wantSlug string
		wantOK   bool
	}{
		{"vendors/9-cisco/CISCO-RTTMON-MIB", 9, "cisco", true},
		{"vendors/22610-a10/A10-AX-MIB", 22610, "a10", true},
		{"vendors/61509-no42/NO42-EXAMPLE-MIB", 61509, "no42", true},
		{"ietf/interfaces/IF-MIB", 0, "", false},
		{"iana/IANAifType-MIB", 0, "", false},
		{"vendors/missing-slug/", 0, "", false},
		{"vendors/9/CISCO", 0, "", false},                       // no dash
		{"vendors/-cisco/CISCO", 0, "", false},                  // empty PEN
		{"vendors/09-cisco/CISCO-MIB", 0, "", false},            // leading-zero PEN rejected
		{"vendors/0-reserved/X", 0, "", false},                  // PEN 0 reserved
		{"vendors/9-cisco-routers/X", 9, "cisco-routers", true}, // multi-dash slug
	}
	for _, c := range cases {
		gotPEN, gotSlug, gotOK := penFromPath(c.path)
		if gotPEN != c.wantPEN || gotSlug != c.wantSlug || gotOK != c.wantOK {
			t.Errorf("penFromPath(%q) = (%d, %q, %v), want (%d, %q, %v)",
				c.path, gotPEN, gotSlug, gotOK, c.wantPEN, c.wantSlug, c.wantOK)
		}
	}
}
