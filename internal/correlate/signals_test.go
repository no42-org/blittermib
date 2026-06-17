/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package correlate

import (
	"reflect"
	"testing"
)

// TestTokenize covers the camelCase + acronym-run boundary handling
// (Story 3.1 AC1). The acronym cases are the regression that orphaned
// thousands of vendor pairs: a directional token glued to an all-caps
// run (catmIntfPvc...OAMFailureTrap) must surface as its own token.
func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"linkDown", []string{"link", "down"}},
		{"bgpBackwardTrans", []string{"bgp", "backward", "trans"}},
		// Acronym run that butts against a word: split before the last cap.
		{"catmIntfPvcAISRDIOAMFailureTrap", []string{"catm", "intf", "pvc", "aisrdioam", "failure", "trap"}},
		{"HTTPServerError", []string{"http", "server", "error"}},
		{"cceAlarmCriticalRaised", []string{"cce", "alarm", "critical", "raised"}},
		// Separators and all-caps-only.
		{"some_trap-NAME", []string{"some", "trap", "name"}},
		{"OAM", []string{"oam"}},
	}
	for _, c := range cases {
		if got := tokenize(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestDescriptionDirectionClearFirst locks the "clear matched first"
// ordering (Story 3.1 AC2): a recovery worded around the fault state
// must not be misread as a raise.
func TestDescriptionDirectionClearFirst(t *testing.T) {
	cases := []struct {
		desc string
		want direction
	}{
		{"the alarm has been cleared (Cleared)", dirClear},
		{"the fault condition was deasserted", dirClear},
		{"the threshold has been exceeded (Generated)", dirRaise},
		{"the board is faulty", dirRaise},
		{"a neutral informational event", dirNone},
	}
	for _, c := range cases {
		if got, _ := descriptionDirection(c.desc, ""); got != c.want {
			t.Errorf("descriptionDirection(%q) = %v, want %v", c.desc, got, c.want)
		}
	}
}
