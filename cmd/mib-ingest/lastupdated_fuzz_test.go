/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"regexp"
	"testing"
)

// canonicalLastUpdated is the shape normalizeLastUpdated promises for
// every non-empty result: 12-or-more digits followed by Z. Callers
// compare two normalised values for equality to decide whether an
// upload is newer than the corpus copy, so a result that ISN'T in this
// canonical form would make two dates that mean the same thing compare
// unequal (or vice versa).
var canonicalLastUpdated = regexp.MustCompile(`^[0-9]{12,}Z$`)

// FuzzNormalizeLastUpdated drives the LAST-UPDATED normaliser with
// arbitrary strings. The raw value comes from an uploaded MIB's
// MODULE-IDENTITY (via smidump), so it is untrusted, and the whole
// point of the function is to reduce every accepted format to one
// comparable shape.
//
// Run locally: `make fuzz` / CI smoke: `make fuzz-smoke`.
func FuzzNormalizeLastUpdated(f *testing.F) {
	for _, s := range []string{
		"202401011200Z",        // SMIv2, canonical already
		"2401011200Z",          // SMIv1, 2-digit year 00-49 -> 20xx
		"9901011200Z",          // SMIv1, 2-digit year 50-99 -> 19xx
		"2024-01-01 12:00",     // smidump human form
		"2024-01-01T12:00:30Z", // smidump, T separator + seconds + Z
		"9999-99-99 99:99",     // shape-valid, range-nonsense
		"not a date", "", "Z", "000000000000Z",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got := normalizeLastUpdated(raw)

		// Postcondition: empty (rejected/incomparable) or canonical.
		if got != "" && !canonicalLastUpdated.MatchString(got) {
			t.Fatalf("normalizeLastUpdated(%q) = %q, not empty and not canonical", raw, got)
		}

		// Determinism: it feeds an equality comparison; must be stable.
		if got2 := normalizeLastUpdated(raw); got != got2 {
			t.Fatalf("non-deterministic: %q -> %q then %q", raw, got, got2)
		}

		// Idempotence: a canonical result fed back in is already the
		// comparable form and must survive unchanged — otherwise
		// "corpus already normalised" vs "upload normalised" could
		// disagree by an extra pass.
		if got != "" {
			if again := normalizeLastUpdated(got); again != got {
				t.Fatalf("not idempotent: %q -> %q -> %q", raw, got, again)
			}
		}
	})
}
