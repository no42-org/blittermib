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

// normalizeLastUpdated returns the canonical 4-digit-year ISO-8601
// form of an SMI LAST-UPDATED value.
//
// Rules (see openspec/changes/ingest-triage-report design decision 8):
//   - SMIv2 form (12+ digits + Z): returned unchanged.
//   - SMIv1 form (10 digits + Z): 2-digit year promoted via a
//     50-year pivot — years 00-49 → 2000-2049,
//     years 50-99 → 1950-1999.
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
	default:
		return ""
	}
}
