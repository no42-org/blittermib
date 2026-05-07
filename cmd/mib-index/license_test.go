package main

import (
	"strings"
	"testing"
)

func TestLicenseDetector(t *testing.T) {
	cases := []struct {
		name, header, want string
	}{
		{"cisco", "-- Copyright (c) 2024 Cisco Systems, Inc.\n", "cisco"},
		{"juniper", "-- Copyright (c) 2024 Juniper Networks, Inc.\n", "juniper"},
		{"hpe (hyphen)", "-- Copyright 2024 Hewlett-Packard Enterprise\n", "hpe"},
		{"hpe (space)", "-- Copyright 2024 Hewlett Packard Enterprise\n", "hpe"},
		{"hp (hyphen)", "-- Copyright 2024 Hewlett-Packard Company\n", "hp"},
		{"hp (space)", "-- Copyright 2024 Hewlett Packard Co\n", "hp"},
		{"aruba", "-- Copyright (c) 2024 Aruba Networks\n", "aruba"},
		{"huawei", "-- Copyright 2024 Huawei Technologies Co.,Ltd\n", "huawei"},
		{"a10", "-- Copyright (c) 2024 A10 Networks\n", "a10"},
		{"mellanox", "-- Copyright Mellanox Technologies\n", "mellanox"},
		{"brocade", "-- Copyright Brocade Communications Systems\n", "brocade"},
		{"extreme", "-- Copyright Extreme Networks\n", "extreme"},
		{"rfc-editor (Internet Society)", "-- Copyright (c) 2009 The Internet Society\n", "rfc-editor"},
		{"rfc-editor (IETF Trust)", "-- Copyright (c) 2024 IETF Trust and the persons identified\n", "rfc-editor"},
		{"unknown (no copyright)", "-- A header without any copyright line\n", "unknown"},
		{"unknown (different vendor)", "-- Copyright Some Random Vendor LLC\n", "unknown"},
		{"empty", "", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectLicense(strings.NewReader(c.header))
			if got != c.want {
				t.Errorf("detectLicense(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}

// TestLicenseDetectorHPEPrecedence pins the rule that HPE's pattern
// is matched before HP's — order matters in the table since HP would
// otherwise greedy-match HPE strings.
func TestLicenseDetectorHPEPrecedence(t *testing.T) {
	got := detectLicense(strings.NewReader("-- Copyright Hewlett-Packard Enterprise Co\n"))
	if got != "hpe" {
		t.Errorf("HPE precedence broken: detectLicense returned %q, want hpe", got)
	}
}

// TestLicenseDetectorBoundedScan asserts the detector doesn't read
// past the configured line cap — a "Copyright Cisco Systems" line
// buried below 200 lines should NOT match.
func TestLicenseDetectorBoundedScan(t *testing.T) {
	var buf strings.Builder
	for i := 0; i < licenseScanLines+10; i++ {
		buf.WriteString("-- filler\n")
	}
	buf.WriteString("-- Copyright Cisco Systems\n")
	if got := detectLicense(strings.NewReader(buf.String())); got != "unknown" {
		t.Errorf("scan exceeded %d-line cap: got %q, want unknown", licenseScanLines, got)
	}
}
