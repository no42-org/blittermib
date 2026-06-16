/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package eventconf

import (
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
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

// TestFromModuleClearKeyRoundTrip covers Story 2.2: a clear's clear-key
// equals its raise's reduction-key (with %uei% bound to the raise UEI),
// and both are entity-scoped by the correlating varbind — so the pair
// auto-clears in OpenNMS and never clears an unrelated instance
// (FR21/FR24/NFR11/NFR12).
func TestFromModuleClearKeyRoundTrip(t *testing.T) {
	ifIndex := model.Symbol{ModuleName: "IF-MIB", Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1", Kind: model.KindColumn}
	down := Notification{
		Symbol:       model.Symbol{ModuleName: "IF-MIB", Name: "linkDown", OID: "1.3.6.1.6.3.1.1.5.3"},
		Objects:      []model.Symbol{ifIndex},
		Relationship: Relationship{AlarmType: AlarmTypeRaise},
	}
	up := Notification{
		Symbol:       model.Symbol{ModuleName: "IF-MIB", Name: "linkUp", OID: "1.3.6.1.6.3.1.1.5.4"},
		Objects:      []model.Symbol{ifIndex},
		Relationship: Relationship{AlarmType: AlarmTypeClear, Clears: []string{"linkDown"}},
	}
	events := FromModule("IF-MIB", []Notification{down, up}, Options{UEIBase: "uei.opennms.org/traps/IF-MIB"})

	ad := make(map[string]*AlarmData)
	uei := make(map[string]string)
	for _, e := range events.Events {
		name := e.UEI[strings.LastIndex(e.UEI, "/")+1:]
		ad[name], uei[name] = e.AlarmData, e.UEI
	}

	if !strings.Contains(ad["linkDown"].ReductionKey, "parm[") {
		t.Errorf("raise reduction-key not entity-scoped: %q", ad["linkDown"].ReductionKey)
	}
	wantClearKey := strings.Replace(ad["linkDown"].ReductionKey, "%uei%", uei["linkDown"], 1)
	if ad["linkUp"].ClearKey != wantClearKey {
		t.Errorf("clear-key = %q, want %q (must equal the raise reduction-key for auto-clear)", ad["linkUp"].ClearKey, wantClearKey)
	}
	if !strings.Contains(ad["linkUp"].ClearKey, "parm[") {
		t.Errorf("clear-key not entity-scoped — would over-clear: %q", ad["linkUp"].ClearKey)
	}
}

// TestReductionKeyUsesSharedVarbind: the per-instance token must be a
// varbind the pair SHARES (so the clear can actually match it), not an
// arbitrary first object the partner doesn't carry. Here the raise's
// first object is raise-only and the shared index is second — the
// reduction-key must scope on the shared index (position 2).
func TestReductionKeyUsesSharedVarbind(t *testing.T) {
	raiseOnly := model.Symbol{ModuleName: "M", Name: "fooReason", OID: "1.3.6.1.4.1.99.2.1", Kind: model.KindColumn}
	idx := model.Symbol{ModuleName: "M", Name: "fooIndex", OID: "1.3.6.1.4.1.99.2.2", Kind: model.KindColumn}
	raise := Notification{
		Symbol:       model.Symbol{ModuleName: "M", Name: "fooDown", OID: "1.3.6.1.4.1.99.0.1"},
		Objects:      []model.Symbol{raiseOnly, idx}, // first object not shared; index second
		Relationship: Relationship{AlarmType: AlarmTypeRaise},
	}
	clear := Notification{
		Symbol:       model.Symbol{ModuleName: "M", Name: "fooUp", OID: "1.3.6.1.4.1.99.0.2"},
		Objects:      []model.Symbol{idx}, // shares only the index
		Relationship: Relationship{AlarmType: AlarmTypeClear, Clears: []string{"fooDown"}},
	}
	events := FromModule("M", []Notification{raise, clear}, Options{UEIBase: "uei.opennms.org/traps/M"})
	rk := events.Events[0].AlarmData.ReductionKey
	if !strings.Contains(rk, "parm[#2]") {
		t.Errorf("reduction-key = %q, want it scoped on the shared index (parm[#2])", rk)
	}
	if strings.Contains(rk, "parm[#1]") {
		t.Errorf("reduction-key = %q, must not scope on the non-shared first object (parm[#1])", rk)
	}
}

// TestProvenanceComment covers Story 2.4: a provenance XML comment is
// emitted before the alarm-data, sanitized against "--", and absent
// when there is no alarm-data.
func TestProvenanceComment(t *testing.T) {
	classified := notif("fooDown", "1.3.6.1.4.1.99.0.1")
	classified.Relationship = Relationship{
		AlarmType:  AlarmTypeRaise,
		Provenance: "inferred raise -- shared ifIndex; confidence High",
	}
	plain := notif("barEvent", "1.3.6.1.4.1.99.0.2") // unclassified
	out, err := Marshal(FromModule("M", []Notification{classified, plain}, Options{UEIBase: "uei.opennms.org/traps/M"}), "M")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	prov := strings.Index(s, "inferred raise")
	if prov < 0 {
		t.Fatalf("provenance comment not emitted:\n%s", s)
	}
	if strings.Contains(s, "raise -- shared") {
		t.Errorf("`--` not sanitized in comment (XML forbids it):\n%s", s)
	}
	if ad := strings.Index(s, "<alarm-data"); ad < 0 || prov > ad {
		t.Errorf("provenance comment must precede alarm-data (prov=%d, alarm-data=%d)", prov, ad)
	}
	// The unclassified event must carry no provenance comment.
	if strings.Count(s, "inferred ") != 1 {
		t.Errorf("expected exactly one provenance comment (only the classified event):\n%s", s)
	}
}
