package server

import (
	"strconv"
	"strings"

	"github.com/no42-org/blittermib/internal/model"
)

// extractSizeConstraint parses an SMI syntax string for a SIZE
// constraint and reports the (lo, hi) bounds. `ok` is true only
// when a parseable, well-formed constraint was found.
//
// Recognised shapes (whitespace-tolerant):
//
//	OCTET STRING (SIZE(6))               → 6, 6, true
//	OCTET STRING (SIZE(0..255))          → 0, 255, true
//	OCTET STRING (SIZE(1..256))          → 1, 256, true
//	OCTET STRING (SIZE(0..255 | 65535))  → 0, 0, false  (alternation)
//	OCTET STRING                         → 0, 0, false  (unbounded)
//	MacAddress                           → 6, 6, true   (TC lookup)
//	MacAddress (SIZE(8))                 → 8, 8, true   (refinement wins)
//
// Alternation bodies (`SIZE(a..b | c..d)`) and inverted ranges
// (`SIZE(10..3)`) are deliberately rejected — the trap-simulator
// modal can't render either sensibly, so the classifier degrades
// to raw-suffix mode.
//
// When a TC name carries an explicit SIZE refinement (e.g.
// `MacAddress (SIZE(8))`), the explicit bounds take precedence
// over the TC default. Falling back to the TC default would
// silently override a deliberate refinement, which would be a
// hard-to-spot misclassification.
//
// Returned identifiers are `lo, hi` rather than `min, max` to
// avoid shadowing Go 1.21+ predeclared builtins inside the
// function body.
func extractSizeConstraint(syntax string) (lo, hi int, ok bool) {
	if body, found := findSizeBody(syntax); found {
		if strings.ContainsRune(body, '|') {
			return 0, 0, false
		}
		if i := strings.Index(body, ".."); i >= 0 {
			a, err1 := strconv.Atoi(strings.TrimSpace(body[:i]))
			b, err2 := strconv.Atoi(strings.TrimSpace(body[i+2:]))
			if err1 != nil || err2 != nil {
				return 0, 0, false
			}
			if a > b {
				return 0, 0, false
			}
			return a, b, true
		}
		n, err := strconv.Atoi(strings.TrimSpace(body))
		if err != nil {
			return 0, 0, false
		}
		return n, n, true
	}
	if size, fixed := tcFixedSize(syntax); fixed {
		return size, size, true
	}
	return 0, 0, false
}

// findSizeBody returns the inside of a `SIZE(...)` clause within
// a syntax string. Mirrors `parseConstraintBody` in
// internal/web/helpers.go but stays in the server package to keep
// this classifier self-contained — that helper renders for the
// type-defs pill and intentionally returns prose, not numbers.
func findSizeBody(syntax string) (string, bool) {
	i := strings.Index(syntax, "SIZE(")
	if i < 0 {
		return "", false
	}
	rest := syntax[i+len("SIZE("):]
	depth := 1
	for j := 0; j < len(rest); j++ {
		switch rest[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return strings.TrimSpace(rest[:j]), true
			}
		}
	}
	return "", false
}

// tcFixedSize reports the byte length of OCTET STRING / address
// Textual Conventions whose underlying SIZE is fixed by the TC
// definition itself. The compile layer emits the TC name verbatim
// as the column's syntax (rather than chasing through to the
// underlying base type), so the classifier has to know the
// well-known names.
//
// `PhysAddress` and `DateAndTime` are intentionally excluded:
// their underlying SIZE is variable, so they belong to the
// variable-length path that lands with IMPLIED-aware composers
// in a follow-on commit.
func tcFixedSize(syntax string) (int, bool) {
	t := strings.TrimSpace(syntax)
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch t {
	case "MacAddress":
		return 6, true
	case "InetAddressIPv4":
		return 4, true
	case "InetAddressIPv6":
		return 16, true
	}
	return 0, false
}

// isOctetStringSyntax reports whether `s` resolves to an SMI
// `OCTET STRING` base type, ignoring any SIZE constraint.
// Recognises the canonical SMI spelling, the smidump XML
// basetype spelling (`OctetString`), fixed-size TCs (covered by
// `tcFixedSize`), and the well-known variable-size TCs
// `PhysAddress`, `DateAndTime`, plus the RFC 4001 generic
// `InetAddress` and `InetAddressDNS` (the typed-variant TCs
// `InetAddressIPv4`/`IPv6` are fixed-size and live in
// `tcFixedSize`). Variable-size TCs round-trip through the
// indexed-mode path with IsImplied determining length-prefix vs
// bare-bytes composition.
func isOctetStringSyntax(s string) bool {
	if _, ok := tcFixedSize(s); ok {
		return true
	}
	t := strings.TrimSpace(s)
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch t {
	case "OCTET STRING", "OctetString",
		"PhysAddress", "DateAndTime",
		"InetAddress", "InetAddressDNS":
		return true
	}
	return false
}

// isInetAddressTypeSyntax reports whether `s` is the SMIv2 RFC
// 4001 `InetAddressType` Textual Convention. It's an enumerated
// integer (`unknown(0), ipv4(1), ipv6(2), ipv4z(3), ipv6z(4),
// dns(16)`) used as the FIRST column of the discriminator pair
// `INDEX { InetAddressType, InetAddress* }`. The trap-simulator
// modal renders a `<select>` for this syntax with the standard
// enum options hardcoded — RFC 4001 freezes the set, so a
// per-MIB lookup of the underlying `EnumValues` would be wasted
// work.
func isInetAddressTypeSyntax(s string) bool {
	t := strings.TrimSpace(s)
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t == "InetAddressType"
}

// isOIDSyntax reports whether `s` resolves to an SMI
// `OBJECT IDENTIFIER` base type. Recognises the canonical SMI
// spelling and the smidump XML basetype spelling
// (`ObjectIdentifier`). Constraints on OID columns are
// vanishingly rare and not parsed here.
func isOIDSyntax(s string) bool {
	t := strings.TrimSpace(s)
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch t {
	case "OBJECT IDENTIFIER", "ObjectIdentifier":
		return true
	}
	return false
}

// isBitsSyntax reports whether `s` resolves to a `BITS` base
// type. Recognises the canonical SMI spelling and the smidump
// XML basetype spelling (`Bits`). Strips any trailing inline
// `{ name(n), …  }` body or constraint group before matching.
func isBitsSyntax(s string) bool {
	t := strings.TrimSpace(s)
	if i := strings.IndexByte(t, '{'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch t {
	case "BITS", "Bits":
		return true
	}
	return false
}

// bitsBytes returns the byte length of a BITS-typed value given
// its named-bits list. The wire encoding of BITS is a fixed-
// length OCTET STRING whose length covers the highest-numbered
// bit — `ceil((maxBit + 1) / 8)`.
//
// Returns 0 in three degenerate cases that all drop the column
// to raw-suffix mode:
//   - empty bit list (malformed BITS definition)
//   - negative bit numbers (illegal per SMI; defensive guard)
//   - max bit ≥ `bitsMaxBit` (sanity cap — see below)
//
// `bitsMaxBit` is set so the derived size never exceeds 64 bytes.
// Real-world BITS index columns have at most a few dozen named
// bits; a vendor MIB (or a corrupted import) emitting `BITS {
// x(2147483647) }` would otherwise derive `SizeMin=268435456`,
// and the trap-simulator modal would loop 268M times building
// the placeholder string and hang the browser. 64 bytes is
// generous (512 named bits) without enabling the DoS shape.
const bitsMaxBit = 512

func bitsBytes(enums []model.EnumValue) int {
	if len(enums) == 0 {
		return 0
	}
	// `-1` is a sentinel meaning "no valid bit seen yet" so the
	// all-negative degenerate case is distinguishable from a
	// legitimate `maxBit == 0` (a single bit at position zero,
	// which is `BITS { x(0) }` and resolves to 1 byte).
	maxBit := int64(-1)
	for _, e := range enums {
		if e.Number < 0 {
			continue
		}
		if e.Number > maxBit {
			maxBit = e.Number
		}
	}
	if maxBit < 0 || maxBit >= bitsMaxBit {
		return 0
	}
	return int(maxBit/8) + 1
}
