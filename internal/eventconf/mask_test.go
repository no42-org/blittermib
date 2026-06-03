/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package eventconf

import "testing"

func TestSplitTrapOID(t *testing.T) {
	tests := []struct {
		name           string
		oid            string
		wantEnterprise string
		wantSpecific   string
		wantOK         bool
	}{
		{
			// ZTE worked example: no next-to-last zero, so the
			// enterprise keeps all but the last sub-id.
			name:           "no next-to-last zero",
			oid:            "1.3.6.1.4.1.3902.4101.1.4.1",
			wantEnterprise: ".1.3.6.1.4.1.3902.4101.1.4",
			wantSpecific:   "1",
			wantOK:         true,
		},
		{
			// RFC 3584 §3.2: a next-to-last zero is stripped from
			// the enterprise.
			name:           "next-to-last zero stripped",
			oid:            "1.3.6.1.4.1.9.0.1",
			wantEnterprise: ".1.3.6.1.4.1.9",
			wantSpecific:   "1",
			wantOK:         true,
		},
		{
			name:           "leading dot tolerated",
			oid:            ".1.3.6.1.4.1.9.42",
			wantEnterprise: ".1.3.6.1.4.1.9",
			wantSpecific:   "42",
			wantOK:         true,
		},
		{
			name:   "too short to split",
			oid:    "1",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ent, spec, ok := splitTrapOID(tt.oid)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if ent != tt.wantEnterprise {
				t.Errorf("enterprise = %q, want %q", ent, tt.wantEnterprise)
			}
			if spec != tt.wantSpecific {
				t.Errorf("specific = %q, want %q", spec, tt.wantSpecific)
			}
		})
	}
}
