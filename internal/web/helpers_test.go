package web

import (
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

func TestOIDUnderPrefix(t *testing.T) {
	cases := []struct {
		name   string
		oid    string
		prefix string
		want   bool
	}{
		{"empty prefix matches anything", "1.3.6.1", "", true},
		{"empty oid matches empty prefix", "", "", true},
		{"empty oid does not match non-empty prefix", "", "1.3", false},
		{"exact match counts as under", "1.3", "1.3", true},
		{"strict prefix with dot boundary", "1.3.6", "1.3", true},
		{"deep prefix match", "1.3.6.1.2.1.2.2.1.10", "1.3.6.1.2.1.2.2.1", true},
		// The classic substring-of-different-OID trap. `1.3` must
		// not match `1.30.6` even though it's a byte-prefix.
		{"numeric substring trap rejected", "1.30.6", "1.3", false},
		{"numeric substring trap rejected (single segment)", "10.20", "1", false},
		// Prefix longer than oid is never a match.
		{"prefix longer than oid", "1", "1.3", false},
		// First-segment differences.
		{"different first segment", "2.3.6", "1.3.6", false},
		// Prefix with trailing dot is malformed but should not match.
		{"trailing-dot prefix does not match", "1.3.6", "1.3.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := OIDUnderPrefix(c.oid, c.prefix)
			if got != c.want {
				t.Errorf("OIDUnderPrefix(%q, %q) = %v, want %v", c.oid, c.prefix, got, c.want)
			}
		})
	}
}

func TestIsEmptyCounts(t *testing.T) {
	if !isEmptyCounts(nil) {
		t.Error("nil FamilyCounts should be empty")
	}
	if !isEmptyCounts(&model.FamilyCounts{}) {
		t.Error("zero-value FamilyCounts should be empty")
	}
	if isEmptyCounts(&model.FamilyCounts{Counters: 1}) {
		t.Error("FamilyCounts with one Counter should not be empty")
	}
	if isEmptyCounts(&model.FamilyCounts{Structs: 1}) {
		t.Error("FamilyCounts with one Struct should not be empty")
	}
}

func TestTrapTypeLetterCommonTCs(t *testing.T) {
	cases := []struct {
		syntax string
		want   string
	}{
		// Base types
		{"INTEGER", "i"},
		{"Integer32", "i"},
		{"Unsigned32", "u"},
		{"Gauge32", "u"},
		{"Counter32", "c"},
		{"Counter64", "C"},
		{"TimeTicks", "t"},
		{"OCTET STRING", "s"},
		{"OBJECT IDENTIFIER", "o"},
		{"BITS", "b"},

		// Common Textual Conventions
		{"IpAddress", "a"},
		{"MacAddress", "x"},
		{"PhysAddress", "x"},
		{"DisplayString", "s"},
		{"SnmpAdminString", "s"},
		{"TimeStamp", "t"},
		{"DateAndTime", "s"},
		// TruthValue and RowStatus are INTEGER subtypes per RFCs
		// 1903 / 2579; the spec mandates "underlying base type's
		// letter", so they map to `i`. Modal UX surfaces an
		// inline hint reminding the user to type the integer.
		{"TruthValue", "i"},
		{"RowStatus", "i"},

		// Inline enum bodies stripped — INTEGER {up(1), down(2)}
		// is an INTEGER for snmptrap purposes.
		{"INTEGER {up(1), down(2)}", "i"},
		{"INTEGER { up(1), down(2), testing(3) }", "i"},

		// Size / range constraints stripped.
		{"Integer32 (1..2147483647)", "i"},
		{"OCTET STRING (SIZE(0..255))", "s"},

		// Whitespace tolerant
		{"  INTEGER  ", "i"},
		{"\tCounter32\n", "c"},

		// Defensive default for unknown vendor TCs
		{"SomeVendorSpecificType", "s"},
		{"", "s"},

		// Compile-layer expansions that show up as varbind syntax
		// in the wild. `Enumeration` is what the smidump-derived
		// model emits for INTEGER-subtype TCs with a named-number
		// list (IfAdminStatus, IfOperStatus, etc.).
		{"Enumeration", "i"},

		// Common integer-subtype TCs — exact TC names as they
		// appear in the syntax field after the compile layer.
		{"InterfaceIndex", "i"},
		{"InterfaceIndexOrZero", "i"},
		{"InetPortNumber", "i"},
		{"InetVersion", "i"},
		{"IANAifType", "i"},
	}
	for _, c := range cases {
		t.Run(c.syntax, func(t *testing.T) {
			if got := TrapTypeLetter(c.syntax); got != c.want {
				t.Errorf("TrapTypeLetter(%q) = %q, want %q", c.syntax, got, c.want)
			}
		})
	}
}
