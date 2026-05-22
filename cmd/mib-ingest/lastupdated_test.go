/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import "testing"

func TestNormalizeLastUpdated(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"smiv2_canonical", "202205101200Z", "202205101200Z"},
		{"smiv2_extra_precision_14digits", "20220510120030Z", "20220510120030Z"},
		{"smiv1_year_99", "9908311200Z", "199908311200Z"},
		// Pivot boundary: year 49 is the last 20xx year.
		{"smiv1_pivot_49_high", "4912311200Z", "204912311200Z"},
		// Pivot boundary: year 50 is the first 19xx year.
		{"smiv1_pivot_50_low", "5001011200Z", "195001011200Z"},
		{"smiv1_year_00", "0001011200Z", "200001011200Z"},
		{"empty", "", ""},
		{"garbage_alpha", "never", ""},
		{"missing_z_suffix", "202205101200", ""},
		{"too_short", "20220510Z", ""},
		{"non_digit_inside", "2022xx101200Z", ""},
		{"trailing_text", "202205101200Zsomething", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeLastUpdated(tc.in)
			if got != tc.want {
				t.Errorf("normalizeLastUpdated(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
