/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package eventconf

import (
	"strings"
	"testing"
)

// TestFromModuleAlarmData covers Story 2.1: a classified notification
// emits <alarm-data> with the matching alarm-type (raise=1, clear=2,
// orphan=3) and a required reduction-key; an unclassified notification
// emits none; and alarm-data is positioned after severity in the
// marshalled output (XSD sequence).
func TestFromModuleAlarmData(t *testing.T) {
	mk := func(name, oid string, rel Relationship) Notification {
		n := notif(name, oid)
		n.Relationship = rel
		return n
	}
	notifs := []Notification{
		mk("fooDown", "1.3.6.1.4.1.99.1.0.1", Relationship{AlarmType: AlarmTypeRaise}),
		mk("fooUp", "1.3.6.1.4.1.99.1.0.2", Relationship{AlarmType: AlarmTypeClear, Clears: []string{"fooDown"}}),
		mk("fooNote", "1.3.6.1.4.1.99.1.0.3", Relationship{AlarmType: AlarmTypeNotification}),
		notif("fooPlain", "1.3.6.1.4.1.99.1.0.4"), // unclassified → no alarm-data
	}
	events := FromModule("M", notifs, Options{UEIBase: "uei.opennms.org/traps/M"})

	got := make(map[string]*AlarmData)
	for _, e := range events.Events {
		name := e.UEI[strings.LastIndex(e.UEI, "/")+1:]
		got[name] = e.AlarmData
	}

	for name, wantType := range map[string]string{
		"fooDown": AlarmTypeRaise,
		"fooUp":   AlarmTypeClear,
		"fooNote": AlarmTypeNotification,
	} {
		ad := got[name]
		if ad == nil {
			t.Errorf("%s: no alarm-data emitted, want alarm-type %s", name, wantType)
			continue
		}
		if ad.AlarmType != wantType {
			t.Errorf("%s: alarm-type = %q, want %q", name, ad.AlarmType, wantType)
		}
		if ad.ReductionKey == "" {
			t.Errorf("%s: reduction-key is required by the schema but empty", name)
		}
	}
	if got["fooPlain"] != nil {
		t.Errorf("unclassified notification should emit no alarm-data, got %+v", got["fooPlain"])
	}

	// XSD sequence: <alarm-data> follows <severity> (and varbindsdecode).
	out, err := Marshal(events, "M")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(out)
	sev := strings.Index(s, "<severity>")
	ad := strings.Index(s, "<alarm-data")
	if sev < 0 || ad < 0 {
		t.Fatalf("expected both <severity> and <alarm-data> in output")
	}
	if ad < sev {
		t.Errorf("alarm-data (%d) must appear after severity (%d) per the eventconf XSD sequence", ad, sev)
	}
}
