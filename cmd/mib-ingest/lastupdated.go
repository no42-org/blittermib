/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import "regexp"

// SMIv2 LAST-UPDATED is `YYYYMMDDHHMMZ` (12 digits + 'Z'); the spec
// allows extra precision so we accept 12 or more digits.
var smiv2LastUpdatedRE = regexp.MustCompile(`^[0-9]{12,}Z$`)

// SMIv1 LAST-UPDATED is `YYMMDDHHMMZ` (10 digits + 'Z' — YY+MM+DD+HH+MM).
var smiv1LastUpdatedRE = regexp.MustCompile(`^[0-9]{10}Z$`)

// smidump emits MODULE-IDENTITY revision dates in human form:
// `YYYY-MM-DD HH:MM` or `YYYY-MM-DD HH:MM:SS` (with optional `T`
// separator and optional `Z` suffix). internal/compile sources
// LastUpdated from `<revision date="…">` attributes, so this
// pattern is the canonical "upload-side" shape that needs
// normalising into the digits+Z form the corpus side stores.
var smidumpLastUpdatedRE = regexp.MustCompile(
	`^([0-9]{4})-([0-9]{2})-([0-9]{2})[ T]([0-9]{2}):([0-9]{2})(?::[0-9]{2})?Z?$`)

// normalizeLastUpdated returns the canonical 4-digit-year ISO-8601
// form (`YYYYMMDDHHMMZ`) of a LAST-UPDATED value.
//
// Rules (see openspec/changes/ingest-triage-report design decision 8):
//   - SMIv2 form (12+ digits + Z): returned unchanged.
//   - SMIv1 form (10 digits + Z): 2-digit year promoted via a
//     50-year pivot — years 00-49 → 2000-2049,
//     years 50-99 → 1950-1999.
//   - smidump human form (`YYYY-MM-DD HH:MM[:SS][Z]`): compacted
//     to `YYYYMMDDHHMMZ`. internal/compile sources LastUpdated
//     from smidump XML, so this is the canonical upload-side
//     shape that must reduce to the same form as the corpus side
//     (which stores raw source digits+Z via mib-index).
//   - Anything else: empty string. Empty inputs and unparseable
//     values normalise the same way so callers can use one
//     "is this comparable?" check.
func normalizeLastUpdated(raw string) string {
	switch {
	case smiv2LastUpdatedRE.MatchString(raw):
		return raw
	case smiv1LastUpdatedRE.MatchString(raw):
		century := "20"
		if raw[0] >= '5' {
			century = "19"
		}
		return century + raw
	}
	if m := smidumpLastUpdatedRE.FindStringSubmatch(raw); m != nil {
		// m[1..5] = year(4), month(2), day(2), hour(2), minute(2).
		return m[1] + m[2] + m[3] + m[4] + m[5] + "Z"
	}
	return ""
}
