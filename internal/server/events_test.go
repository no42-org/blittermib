/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// eventsTestServer seeds TEST-MIB (one notification with a scalar and
// a columnar object) and EMPTY-MIB (no notifications).
func eventsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	syms := []model.Symbol{
		{ModuleName: "TEST-MIB", Name: "alarmRaised", OID: "1.3.6.1.4.1.99.0.1", Kind: model.KindNotificationType, Description: "An alarm was raised."},
		{ModuleName: "TEST-MIB", Name: "alarmId", OID: "1.3.6.1.4.1.99.1.1", Kind: model.KindScalar, Access: model.AccessAccessibleNotify},
		{ModuleName: "TEST-MIB", Name: "alarmCol", OID: "1.3.6.1.4.1.99.2.1.1", Kind: model.KindColumn, Access: model.AccessReadOnly},
	}
	refs := []model.Reference{
		{SourceModule: "TEST-MIB", SourceName: "alarmRaised", TargetModule: "TEST-MIB", TargetName: "alarmId", Kind: model.RefNotificationObject, Position: 0},
		{SourceModule: "TEST-MIB", SourceName: "alarmRaised", TargetModule: "TEST-MIB", TargetName: "alarmCol", Kind: model.RefNotificationObject, Position: 1},
	}
	if err := st.ReplaceModule(context.Background(),
		&model.Module{Name: "TEST-MIB", OIDRoot: "1.3.6.1.4.1.99", ParseStatus: model.ParseStatusClean},
		syms, refs, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceModule(context.Background(),
		&model.Module{Name: "EMPTY-MIB", OIDRoot: "1.3.6.1.4.1.100", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{{ModuleName: "EMPTY-MIB", Name: "emptyRoot", OID: "1.3.6.1.4.1.100", Kind: model.KindObjectIdentity}},
		nil, nil); err != nil {
		t.Fatal(err)
	}

	srv := New(st, "", "test", t.TempDir())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestModuleEventsEndpoint(t *testing.T) {
	ts := eventsTestServer(t)
	resp, err := http.Get(ts.URL + "/m/TEST-MIB/events.xml")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content-type = %q, want application/xml", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="TEST-MIB.events.xml"`) {
		t.Errorf("content-disposition = %q", cd)
	}
	got := body(t, resp)
	if !strings.Contains(got, `<events xmlns="http://xmlns.opennms.org/xsd/eventconf">`) {
		t.Errorf("missing eventconf namespace:\n%s", got)
	}
	if n := strings.Count(got, "<event>"); n != 1 {
		t.Errorf("got %d <event> elements, want 1", n)
	}
	// Default UEI base.
	if !strings.Contains(got, "<uei>uei.opennms.org/traps/TEST-MIB/alarmRaised</uei>") {
		t.Errorf("missing/incorrect default uei:\n%s", got)
	}
	// Scalar (accessible-for-notify) → OID param; column → positional.
	if !strings.Contains(got, "alarmId=%parm[1.3.6.1.4.1.99.1.1.0]%") {
		t.Errorf("scalar object not OID-referenced:\n%s", got)
	}
	if !strings.Contains(got, "alarmCol=%parm[#2]%") {
		t.Errorf("columnar object not positional:\n%s", got)
	}
	// descr/logmsg are whitespace-collapsed to a single line, so the
	// serialized document carries no newline/tab/CR character refs.
	for _, ref := range []string{"&#xA;", "&#x9;", "&#xD;"} {
		if strings.Contains(got, ref) {
			t.Errorf("response contains whitespace char reference %q:\n%s", ref, got)
		}
	}
}

func TestModuleEventsNoNotifications404(t *testing.T) {
	ts := eventsTestServer(t)
	resp, err := http.Get(ts.URL + "/m/EMPTY-MIB/events.xml")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestModuleEventsUEIOverride(t *testing.T) {
	ts := eventsTestServer(t)
	resp, err := http.Get(ts.URL + "/m/TEST-MIB/events.xml?uei=uei.opennms.org/custom")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := body(t, resp)
	if !strings.Contains(got, "<uei>uei.opennms.org/custom/alarmRaised</uei>") {
		t.Errorf("uei override not applied:\n%s", got)
	}
	if strings.Contains(got, "uei.opennms.org/traps/") {
		t.Errorf("default uei leaked despite override:\n%s", got)
	}
}

func TestModuleEventsForcePositional(t *testing.T) {
	ts := eventsTestServer(t)
	resp, err := http.Get(ts.URL + "/m/TEST-MIB/events.xml?parms=position")
	if err != nil {
		t.Fatal(err)
	}
	got := body(t, resp)
	// Even the scalar object falls back to positional.
	if !strings.Contains(got, "alarmId=%parm[#1]%") {
		t.Errorf("parms=position did not force positional for scalar:\n%s", got)
	}
}

func TestModuleEventsInvalidUEI(t *testing.T) {
	ts := eventsTestServer(t)
	// A space is outside the allowed charset; "/" and ":::" are in the
	// charset but punctuation-only (no alphanumeric) — both must be
	// rejected so they can't yield a malformed "<uei>/name".
	for _, bad := range []string{"bad%20uei", "%2F", "%3A%3A%3A"} {
		resp, err := http.Get(ts.URL + "/m/TEST-MIB/events.xml?uei=" + bad)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("uei=%q: status = %d, want 400", bad, resp.StatusCode)
		}
	}
}
