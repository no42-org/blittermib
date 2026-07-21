/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package walk

import (
	"reflect"
	"strings"
	"testing"
)

// FuzzParse drives the walk-capture parser with arbitrary bytes. Parse
// takes text pasted verbatim into /walk — genuinely untrusted input
// reachable over HTTP — so the bar is not just "doesn't panic" but that
// every structural contract the resolver downstream relies on holds for
// ANY input.
//
// Run locally: `make fuzz` (or `go test ./internal/walk -run x -fuzz
// FuzzParse`). CI runs a bounded smoke via `make fuzz-smoke`.
func FuzzParse(f *testing.F) {
	// Seeds: realistic captures first, adversarial shapes after. The
	// fuzzer mutates from these, so covering both real formats and the
	// parser's known edge branches gives it a head start.
	seeds := []string{
		// Numeric -On form.
		".1.3.6.1.2.1.1.1.0 = STRING: Linux host 4.19\n" +
			".1.3.6.1.2.1.1.3.0 = Timeticks: (12345) 0:02:03.45\n",
		// Name-prefixed form.
		"SNMPv2-MIB::sysDescr.0 = STRING: hello\n" +
			"IF-MIB::ifPhysAddress.1 = Hex-STRING: 00 11 22 33 44 55\n",
		// Hex-STRING with a wrapped continuation line (no ` = `).
		".1.3.6.1.2.1.2.2.1.6.1 = Hex-STRING: 00 11 22 33 44 55\n" +
			"66 77 88 99 AA BB CC DD EE FF 00 11 22 33 44 55\n",
		// Not-present markers.
		".1.3.6.1.2.1.1.9.0 = No Such Instance currently exists at this OID\n" +
			".1.3.6.1 = No Such Object available on this agent at this OID\n",
		// Ignored cruft + a value that itself contains " = ".
		"# a comment\n\n" +
			"End of MIB\n" +
			"SNMPv2-MIB::sysDescr.0 = STRING: a = b = c\n",
		// Junk / prose that must be skipped, and malformed OIDs.
		"count = 3\n.1.3..6 = STRING: bad oid\n:: = STRING: empty module\n",
		// Adversarial: no newline, embedded NULs, colons, lone dots.
		"\x00\x00 = : \x00\n.=.=.\nA::B.:::",
		// Value-only RHS (no colon) and a bare not-present tail.
		"1.2.3 = plainvalue\n1.2.4 = \n",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, text string) {
		w := Parse(text)

		// Determinism: a pure function of its input. A non-deterministic
		// parser would make the /walk decode unreproducible.
		if w2 := Parse(text); !reflect.DeepEqual(w, w2) {
			t.Fatalf("Parse is non-deterministic for %q", text)
		}

		var prevLine int
		for i, e := range w.Entries {
			// The core contract: parseLine only emits an Entry when the
			// left-hand side is a shape the resolver can act on. If a junk
			// identifier ever reaches Entries, the resolver would try to
			// look it up as an OID/symbol — so no admitted entry may fail
			// validIdent.
			if !validIdent(e.Ident) {
				t.Fatalf("entry %d has ident %q that fails validIdent", i, e.Ident)
			}
			// A numeric ident (no MODULE::) must be a well-formed dotted
			// OID — the resolver splits it on '.' and parses segments.
			if !strings.Contains(e.Ident, "::") && !validNumericOID(e.Ident) {
				t.Fatalf("entry %d numeric ident %q is not a valid numeric OID", i, e.Ident)
			}
			// Line numbers are 1-based and non-decreasing in scan order;
			// the UI indexes back into the paste by them.
			if e.LineNumber < 1 {
				t.Fatalf("entry %d has non-positive line number %d", i, e.LineNumber)
			}
			if e.LineNumber < prevLine {
				t.Fatalf("entry %d line %d < previous %d (out of order)", i, e.LineNumber, prevLine)
			}
			prevLine = e.LineNumber

			// Hex-STRING values are normalised to colon-joined bytes with
			// no residual runs of spaces — the resolver's index decoder
			// depends on that shape.
			if e.Type == "Hex-STRING" && strings.Contains(e.Value, " ") {
				t.Fatalf("entry %d Hex-STRING value %q still contains a space", i, e.Value)
			}
		}
	})
}
