package iana

import (
	"strings"
	"testing"
)

// TestPENRegistryWellKnown anchors the embedded registry against
// vendors whose PENs are widely cited in MIB literature. Failure
// suggests the embed got truncated, replaced, or out of sync with
// upstream IANA in a way that drops well-known entries.
func TestPENRegistryWellKnown(t *testing.T) {
	cases := []struct {
		pen  uint32
		want string
	}{
		{9, "Cisco Systems, Inc."},
		{311, "Microsoft Corporation"},
		{2636, "Juniper Networks, Inc."},
		{8072, "Net-SNMP"},
		{22610, "A10 Networks"},
		{61509, "no42.org"},
	}
	for _, c := range cases {
		got, ok := LookupPEN(c.pen)
		if !ok {
			t.Errorf("LookupPEN(%d): not found", c.pen)
			continue
		}
		if got != c.want {
			t.Errorf("LookupPEN(%d) = %q, want %q", c.pen, got, c.want)
		}
	}
}

func TestLookupPENMissing(t *testing.T) {
	// A deliberately implausible value (above any allocation we'd
	// vendor in the curated set, below the uint32 ceiling).
	if got, ok := LookupPEN(0xFFFFFFF0); ok {
		t.Errorf("LookupPEN(huge) = %q, ok=true; want not-found", got)
	}
}

// TestParsePENCRLF asserts the parser tolerates CRLF line endings —
// the upstream IANA file is sometimes served with Windows endings via
// proxies, and a future contributor editing the curated snapshot on
// Windows would otherwise silently break parsing.
func TestParsePENCRLF(t *testing.T) {
	in := "9\r\n  Cisco Systems, Inc.\r\n11\r\n  Hewlett-Packard Company\r\n"
	m, err := parsePEN(in)
	if err != nil {
		t.Fatalf("parsePEN err: %v", err)
	}
	if got := m[9]; got != "Cisco Systems, Inc." {
		t.Errorf("CRLF input: m[9] = %q, want Cisco Systems, Inc.", got)
	}
	if got := m[11]; got != "Hewlett-Packard Company" {
		t.Errorf("CRLF input: m[11] = %q, want Hewlett-Packard Company", got)
	}
}

// TestParsePENBOM asserts the parser ignores a UTF-8 byte-order mark
// at the start of the file. A refreshed snapshot saved with one would
// otherwise misclassify the first PEN line as junk.
func TestParsePENBOM(t *testing.T) {
	in := "\ufeff9\n  Cisco Systems, Inc.\n"
	m, err := parsePEN(in)
	if err != nil {
		t.Fatalf("parsePEN err: %v", err)
	}
	if got := m[9]; got != "Cisco Systems, Inc." {
		t.Errorf("BOM input: m[9] = %q, want Cisco Systems, Inc.", got)
	}
}

// TestParsePENDuplicate documents the last-wins semantics for
// duplicate PEN entries — pinning the behaviour so a future change to
// "first-wins" or "warn on duplicate" is a deliberate decision.
func TestParsePENDuplicate(t *testing.T) {
	in := "9\n  First Org\n9\n  Second Org\n"
	m, err := parsePEN(in)
	if err != nil {
		t.Fatalf("parsePEN err: %v", err)
	}
	if got := m[9]; got != "Second Org" {
		t.Errorf("dup PEN: m[9] = %q, want Second Org (last-wins)", got)
	}
}

// TestParsePENMissingOrg asserts that a column-0 PEN with no indented
// org line is dropped cleanly — the next stanza's content must not
// be silently bound to the orphaned PEN.
func TestParsePENMissingOrg(t *testing.T) {
	// PEN 9 has no org; PEN 11 follows with its org. The org line
	// must bind to 11, not 9.
	in := "9\n11\n  Hewlett-Packard Company\n"
	m, err := parsePEN(in)
	if err != nil {
		t.Fatalf("parsePEN err: %v", err)
	}
	if _, ok := m[9]; ok {
		t.Errorf("dangling PEN 9 should not appear in map: got %q", m[9])
	}
	if got := m[11]; got != "Hewlett-Packard Company" {
		t.Errorf("m[11] = %q, want Hewlett-Packard Company (binding leaked from PEN 9?)", got)
	}
}

func TestSlugRules(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Suffix-word stripping (rules only, trailing-only).
		{"Cisco Systems, Inc.", "cisco"},
		{"Juniper Networks, Inc.", "juniper"},
		{"Juniper Networks", "juniper"},
		{"A10 Networks", "a10"},
		{"Microsoft Corporation", "microsoft"},
		{"Fortinet, Inc.", "fortinet"},
		{"Net-SNMP", "net-snmp"},
		{"Arista Networks, Inc.", "arista"},
		{"Mellanox Technologies", "mellanox"},
		{"Huawei Technologies Co.,Ltd", "huawei"},

		// Trailing-only: mid-token suffix words are preserved so
		// product/qualifier words don't get eaten.
		{"Cisco Systems Routing", "cisco-systems-routin"},
		{"Aruba Wireless Networks", "aruba-wireless"},

		// Override map (case-insensitive lookup).
		{"Hewlett-Packard Company", "hp"},
		{"Hewlett Packard Enterprise", "hp-enterprise"},
		{"hewlett packard enterprise", "hp-enterprise"},
		{"no42.org", "no42"},

		// All-suffix-words → fall back to original tokens so the slug
		// isn't empty.
		{"Networks Inc", "networks-inc"},

		// Truncation: rule-derived slug is cut at 20 runes, trailing
		// '-' trimmed.
		{"Some Very Long Company Name Here", "some-very-long-compa"},

		// Allowed-rune filter: punctuation and non-ASCII become spaces.
		{"AT&T", "at-t"},
		{"Société Générale", "soci-t-g-n-rale"},

		// Empty / whitespace.
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		got := Slug(c.in)
		if got != c.want {
			t.Errorf("Slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugMaxLen(t *testing.T) {
	in := "AAAA BBBB CCCC DDDD EEEE FFFF GGGG"
	got := Slug(in)
	if l := len(got); l > 20 {
		t.Errorf("Slug returned %d chars, want <=20: %q", l, got)
	}
	// A non-empty input must not collapse to an empty slug — guards
	// against a regression where the rule path returns "".
	if got == "" {
		t.Errorf("Slug(%q) = empty; want non-empty for non-empty input", in)
	}
}

// TestSlugMultiByteTruncation guards against byte-slicing a multi-
// byte rune in half. Today Slug filters non-ASCII to spaces before
// truncation so the bug can't surface, but the test pins the
// invariant in case the filter is ever loosened.
func TestSlugMultiByteTruncation(t *testing.T) {
	// 21 'a's followed by an emoji that the filter will replace with
	// a space — even if the filter changed, the truncation must not
	// split a rune.
	in := strings.Repeat("a", 21) + "\U0001F98A"
	got := Slug(in)
	if !isASCIIPrintable(got) {
		t.Errorf("Slug(%q) = %q; non-ASCII or non-printable in slug", in, got)
	}
	if len([]rune(got)) > 20 {
		t.Errorf("Slug(%q) = %q (%d runes); want <=20", in, got, len([]rune(got)))
	}
}

func isASCIIPrintable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7E {
			return false
		}
	}
	return true
}

func TestCanonicalLookup(t *testing.T) {
	cases := []struct {
		oid, want string
	}{
		{"1", "iso"},
		{"1.3.6.1", "internet"},
		{"1.3.6.1.2.1", "mib-2"},
		{"1.3.6.1.4.1", "enterprises"},
		{"1.3.6.1.2.1.1.5", "sysName"},
	}
	for _, c := range cases {
		got, ok := LookupCanonical(c.oid)
		if !ok {
			t.Errorf("LookupCanonical(%q): not found", c.oid)
			continue
		}
		if got != c.want {
			t.Errorf("LookupCanonical(%q) = %q, want %q", c.oid, got, c.want)
		}
	}
}
