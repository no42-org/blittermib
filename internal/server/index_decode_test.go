package server

import (
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

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
// SMI canonical, smidump XML basetype, fixed-size TCs, and the
// well-known variable-size TCs (PhysAddress, DateAndTime) all
// classify as OCTET STRING; everything else does not.
func TestIsOctetStringSyntax(t *testing.T) {
	yes := []string{
		"OCTET STRING",
		"OctetString",
		"OCTET STRING (SIZE(6))",
		"OCTET STRING (SIZE(0..255))",
		"MacAddress",
		"InetAddressIPv4",
		"InetAddressIPv6",
		"PhysAddress",
		"DateAndTime",
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

// TestIsInetAddressTypeSyntax pins the RFC 4001
// `InetAddressType` recognition — exact-match on the TC name
// after stripping any trailing constraint group. The
// classifier surfaces this as a distinct `Syntax` value so the
// modal can render an enum-aware `<select>` instead of a plain
// numeric input.
func TestIsInetAddressTypeSyntax(t *testing.T) {
	yes := []string{
		"InetAddressType",
		"  InetAddressType  ",
		"InetAddressType (1..16)",
	}
	no := []string{
		"INTEGER",
		"InetAddress",
		"InetAddressIPv4",
		"InetAddressIPv6",
		"IpAddress",
	}
	for _, s := range yes {
		if !isInetAddressTypeSyntax(s) {
			t.Errorf("isInetAddressTypeSyntax(%q) = false; want true", s)
		}
	}
	for _, s := range no {
		if isInetAddressTypeSyntax(s) {
			t.Errorf("isInetAddressTypeSyntax(%q) = true; want false", s)
		}
	}
}

// TestIsOctetStringSyntaxRecognisesInetAddressFamily pins the
// extension that covers RFC 4001's variable-size address TCs.
// `InetAddress` (any-family) and `InetAddressDNS` are
// variable-length OCTET STRING-shaped on the wire; the
// classifier routes them through the variable-OCTET-STRING
// path.
func TestIsOctetStringSyntaxRecognisesInetAddressFamily(t *testing.T) {
	yes := []string{
		"InetAddress",
		"InetAddressDNS",
	}
	for _, s := range yes {
		if !isOctetStringSyntax(s) {
			t.Errorf("isOctetStringSyntax(%q) = false; want true", s)
		}
	}
}

// TestIsBitsSyntax pins the BITS classifier — canonical SMI
// spelling, smidump basetype spelling, and trailing `{ name(n),
// … }` bodies all classify as BITS; everything else falls through.
func TestIsBitsSyntax(t *testing.T) {
	yes := []string{
		"BITS",
		"Bits",
		"BITS { red(0), green(1), blue(2) }",
		"  BITS  ",
	}
	no := []string{
		"INTEGER",
		"OCTET STRING",
		"IpAddress",
		"OBJECT IDENTIFIER",
		"MacAddress",
	}
	for _, s := range yes {
		if !isBitsSyntax(s) {
			t.Errorf("isBitsSyntax(%q) = false; want true", s)
		}
	}
	for _, s := range no {
		if isBitsSyntax(s) {
			t.Errorf("isBitsSyntax(%q) = true; want false", s)
		}
	}
}

// TestBitsBytes pins the named-bits → byte-count derivation.
// Wire encoding of BITS is a fixed-length OCTET STRING covering
// the highest-numbered named bit, so size = ceil((maxBit+1)/8).
func TestBitsBytes(t *testing.T) {
	cases := []struct {
		name  string
		enums []model.EnumValue
		want  int
	}{
		{
			name:  "empty bit list yields zero (caller drops to raw-suffix)",
			enums: nil,
			want:  0,
		},
		{
			name: "single bit zero needs one byte",
			enums: []model.EnumValue{
				{Name: "flag", Number: 0},
			},
			want: 1,
		},
		{
			name: "max bit 7 fits in one byte",
			enums: []model.EnumValue{
				{Name: "a", Number: 0},
				{Name: "h", Number: 7},
			},
			want: 1,
		},
		{
			name: "max bit 8 spills to two bytes",
			enums: []model.EnumValue{
				{Name: "low", Number: 0},
				{Name: "ninth", Number: 8},
			},
			want: 2,
		},
		{
			name: "max bit 15 fills two bytes exactly",
			enums: []model.EnumValue{
				{Name: "msb", Number: 15},
			},
			want: 2,
		},
		{
			name: "max bit 16 spills to three bytes",
			enums: []model.EnumValue{
				{Name: "edge", Number: 16},
			},
			want: 3,
		},
		{
			name: "out-of-order bit numbers — max wins",
			enums: []model.EnumValue{
				{Name: "high", Number: 23},
				{Name: "low", Number: 1},
				{Name: "mid", Number: 9},
			},
			want: 3,
		},
		{
			// Negative bit numbers are illegal per SMI; defensive
			// guard. Returning 0 drops the column to raw-suffix
			// rather than fabricating a size from corrupt data.
			name: "all-negative bit numbers — rejected as 0",
			enums: []model.EnumValue{
				{Name: "broken", Number: -1},
			},
			want: 0,
		},
		{
			// Last legal value just under the cap (511 → 64 bytes).
			name: "max bit 511 still inside the sanity cap",
			enums: []model.EnumValue{
				{Name: "edge", Number: 511},
			},
			want: 64,
		},
		{
			// First value the cap rejects. Without the cap the
			// modal would build a 64-pair hex placeholder string
			// — already sizable but tractable. The cap protects
			// against pathological inputs (e.g. `BITS { x(2147483647) }`)
			// that would derive a 268M-byte size and hang the browser
			// in the placeholder loop.
			name: "max bit 512 rejected by sanity cap",
			enums: []model.EnumValue{
				{Name: "huge", Number: 512},
			},
			want: 0,
		},
		{
			// Pathological case from the corpus-validation review.
			// Without the cap this would derive a 268,435,456-byte
			// size and the JS placeholder builder would hang.
			name: "max bit 2_147_483_647 rejected (DoS shape)",
			enums: []model.EnumValue{
				{Name: "attack", Number: 2147483647},
			},
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bitsBytes(c.enums); got != c.want {
				t.Errorf("bitsBytes(%v) = %d; want %d", c.enums, got, c.want)
			}
		})
	}
}

// TestIsOIDSyntax pins the OBJECT IDENTIFIER classifier — the
// canonical SMI spelling and smidump's basetype spelling both
// classify as OID; everything else falls through.
func TestIsOIDSyntax(t *testing.T) {
	yes := []string{
		"OBJECT IDENTIFIER",
		"ObjectIdentifier",
		"  OBJECT IDENTIFIER  ",
	}
	no := []string{
		"INTEGER",
		"OCTET STRING",
		"IpAddress",
		"BITS",
		"MacAddress",
	}
	for _, s := range yes {
		if !isOIDSyntax(s) {
			t.Errorf("isOIDSyntax(%q) = false; want true", s)
		}
	}
	for _, s := range no {
		if isOIDSyntax(s) {
			t.Errorf("isOIDSyntax(%q) = true; want false", s)
		}
	}
}
