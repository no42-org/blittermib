package iana

import "testing"

func TestResolveCanonical(t *testing.T) {
	// A management OID resolves the full iso/org/dod/internet/mgmt/
	// mib-2/system/sysDescr breadcrumb.
	steps := ResolveCanonical(".1.3.6.1.2.1.1.1.0")
	wantNames := []string{"iso", "org", "dod", "internet", "mgmt", "mib-2", "system", "sysDescr"}
	if len(steps) != len(wantNames) {
		t.Fatalf("mgmt walk: got %d steps %v, want %d", len(steps), steps, len(wantNames))
	}
	for i, w := range wantNames {
		if steps[i].Name != w {
			t.Errorf("step %d: got %q, want %q", i, steps[i].Name, w)
		}
	}
	if last := steps[len(steps)-1]; last.OID != "1.3.6.1.2.1.1.1" {
		t.Errorf("deepest step OID = %q, want 1.3.6.1.2.1.1.1", last.OID)
	}

	// A vendor OID stops at enterprises; nothing under the PEN is in
	// Canonical, so the breadcrumb's last named arc is enterprises and
	// the PEN is surfaced via LookupPEN.
	vendor := ResolveCanonical("1.3.6.1.4.1.9.2.1.58.0")
	last := vendor[len(vendor)-1]
	if last.Name != "enterprises" || last.OID != "1.3.6.1.4.1" {
		t.Fatalf("vendor walk should stop at enterprises, got %+v", last)
	}
	if org, ok := LookupPEN(9); !ok || org != "ciscoSystems" {
		t.Fatalf("PEN 9 lookup = (%q, %v), want (ciscoSystems, true)", org, ok)
	}

	if got := ResolveCanonical("   "); got != nil {
		t.Errorf("blank input: got %v, want nil", got)
	}
}
