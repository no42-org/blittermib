package server

import "testing"

// TestExtractSizeConstraint pins the regex-style parser's contract
// for every recognised SIZE-constraint shape plus the rejection
// paths (alternation, unbounded, malformed). Adding a new
// recognised TC or shape means adding a row here too.
func TestExtractSizeConstraint(t *testing.T) {
	cases := []struct {
		syntax  string
		wantMin int
		wantMax int
		wantOk  bool
	}{
		// Fixed SIZE(N).
		{"OCTET STRING (SIZE(6))", 6, 6, true},
		{"OCTET STRING (SIZE(0))", 0, 0, true},
		{"OctetString (SIZE(6))", 6, 6, true},

		// Range SIZE(min..max).
		{"OCTET STRING (SIZE(0..255))", 0, 255, true},
		{"OCTET STRING (SIZE(1..256))", 1, 256, true},
		{"OctetString (SIZE(0..255))", 0, 255, true},

		// Alternation — rejected; classifier degrades to raw-suffix.
		{"OCTET STRING (SIZE(0..255 | 65535))", 0, 0, false},

		// Inverted ranges — rejected. SMI permits `SIZE(min..max)`
		// only when `min <= max`; an inverted spelling is malformed
		// and the classifier must fall through to raw-suffix.
		{"OCTET STRING (SIZE(10..3))", 0, 0, false},
		{"OCTET STRING (SIZE(255..0))", 0, 0, false},

		// Numeric overflow — rejected. `strconv.Atoi` returns
		// `ErrRange` on values that exceed the platform `int`
		// width, so the parser refuses rather than wrapping into
		// a garbage bound.
		{"OCTET STRING (SIZE(99999999999999999999))", 0, 0, false},

		// TC name with explicit SIZE refinement — the explicit
		// constraint MUST win over the TC default, otherwise a
		// vendor MIB that legitimately refines a TC's size would
		// be silently misclassified.
		{"MacAddress (SIZE(8))", 8, 8, true},
		{"InetAddressIPv4 (SIZE(4..16))", 4, 16, true},

		// Unbounded — no SIZE clause.
		{"OCTET STRING", 0, 0, false},
		{"OBJECT IDENTIFIER", 0, 0, false},

		// TC lookup — fixed-size TCs resolve without an explicit
		// SIZE clause on the syntax string.
		{"MacAddress", 6, 6, true},
		{"InetAddressIPv4", 4, 4, true},
		{"InetAddressIPv6", 16, 16, true},

		// PhysAddress / DateAndTime are deliberately variable and
		// must NOT resolve as fixed — they belong to the
		// IMPLIED-aware variable path that lands in a follow-on.
		{"PhysAddress", 0, 0, false},
		{"DateAndTime", 0, 0, false},

		// Malformed bodies — rejected.
		{"OCTET STRING (SIZE())", 0, 0, false},
		{"OCTET STRING (SIZE(abc))", 0, 0, false},
		{"OCTET STRING (SIZE(1..foo))", 0, 0, false},
	}

	for _, c := range cases {
		gotMin, gotMax, gotOk := extractSizeConstraint(c.syntax)
		if gotOk != c.wantOk || gotMin != c.wantMin || gotMax != c.wantMax {
			t.Errorf("extractSizeConstraint(%q) = (%d, %d, %v); want (%d, %d, %v)",
				c.syntax, gotMin, gotMax, gotOk, c.wantMin, c.wantMax, c.wantOk)
		}
	}
}

// TestIsOctetStringSyntax pins the helper's classification —
// SMI canonical, smidump XML basetype, and known fixed-size TCs
// all classify as OCTET STRING; everything else does not.
func TestIsOctetStringSyntax(t *testing.T) {
	yes := []string{
		"OCTET STRING",
		"OctetString",
		"OCTET STRING (SIZE(6))",
		"OCTET STRING (SIZE(0..255))",
		"MacAddress",
		"InetAddressIPv4",
		"InetAddressIPv6",
	}
	no := []string{
		"INTEGER",
		"Integer32",
		"IpAddress",
		"OBJECT IDENTIFIER",
		"BITS",
		"Counter32",
	}
	for _, s := range yes {
		if !isOctetStringSyntax(s) {
			t.Errorf("isOctetStringSyntax(%q) = false; want true", s)
		}
	}
	for _, s := range no {
		if isOctetStringSyntax(s) {
			t.Errorf("isOctetStringSyntax(%q) = true; want false", s)
		}
	}
}
