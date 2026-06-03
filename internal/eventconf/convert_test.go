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

func scalar(name, oid string) model.Symbol {
	return model.Symbol{Name: name, OID: oid, Kind: model.KindScalar}
}

func column(name, oid string) model.Symbol {
	return model.Symbol{Name: name, OID: oid, Kind: model.KindColumn}
}

func notif(name, oid string, objs ...model.Symbol) Notification {
	return Notification{
		Symbol:  model.Symbol{Name: name, OID: oid, Kind: model.KindNotificationType, Description: "desc of " + name},
		Objects: objs,
	}
}

// logmsgLine returns the parameter line for object `objName` in the
// event's logmsg, or "" if absent.
func logmsgLine(evt Event, objName string) string {
	for _, line := range strings.Split(evt.Logmsg.Content, "\n") {
		if strings.HasPrefix(line, objName+"=") {
			return line
		}
	}
	return ""
}

func TestFromModuleScalarOIDParams(t *testing.T) {
	events := FromModule("TEST-MIB", []Notification{
		notif("alarmRaised", "1.3.6.1.4.1.99.1.0.1",
			scalar("alarmId", "1.3.6.1.4.1.99.1.1"),
			scalar("alarmText", "1.3.6.1.4.1.99.1.2"),
		),
	}, Options{UEIBase: "uei.opennms.org/traps/TEST-MIB"})

	if len(events.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(events.Events))
	}
	evt := events.Events[0]
	if evt.UEI != "uei.opennms.org/traps/TEST-MIB/alarmRaised" {
		t.Errorf("uei = %q", evt.UEI)
	}
	if evt.EventLabel != "TEST-MIB defined trap event: alarmRaised" {
		t.Errorf("event-label = %q", evt.EventLabel)
	}
	if evt.Severity != "Indeterminate" {
		t.Errorf("severity = %q, want Indeterminate", evt.Severity)
	}
	if want := "alarmId=%parm[1.3.6.1.4.1.99.1.1.0]%"; logmsgLine(evt, "alarmId") != want {
		t.Errorf("alarmId line = %q, want %q", logmsgLine(evt, "alarmId"), want)
	}
	if want := "alarmText=%parm[1.3.6.1.4.1.99.1.2.0]%"; logmsgLine(evt, "alarmText") != want {
		t.Errorf("alarmText line = %q, want %q", logmsgLine(evt, "alarmText"), want)
	}
}

func TestFromModuleColumnarPositional(t *testing.T) {
	events := FromModule("TEST-MIB", []Notification{
		notif("rowChanged", "1.3.6.1.4.1.99.2.0.1",
			column("colA", "1.3.6.1.4.1.99.2.1.1"),
			column("colB", "1.3.6.1.4.1.99.2.1.2"),
		),
	}, Options{UEIBase: "uei.opennms.org/traps"})

	evt := events.Events[0]
	if want := "colA=%parm[#1]%"; logmsgLine(evt, "colA") != want {
		t.Errorf("colA line = %q, want %q", logmsgLine(evt, "colA"), want)
	}
	if want := "colB=%parm[#2]%"; logmsgLine(evt, "colB") != want {
		t.Errorf("colB line = %q, want %q", logmsgLine(evt, "colB"), want)
	}
}

func TestFromModuleMixedNumbering(t *testing.T) {
	// scalar, column, scalar, column — columns must be #2 and #4
	// (position counts all objects), scalars use OID form.
	events := FromModule("TEST-MIB", []Notification{
		notif("mixed", "1.3.6.1.4.1.99.3.0.1",
			scalar("s1", "1.3.6.1.4.1.99.3.1"),
			column("c1", "1.3.6.1.4.1.99.3.2.1"),
			scalar("s2", "1.3.6.1.4.1.99.3.3"),
			column("c2", "1.3.6.1.4.1.99.3.4.1"),
		),
	}, Options{UEIBase: "uei.opennms.org/traps"})

	evt := events.Events[0]
	checks := map[string]string{
		"s1": "s1=%parm[1.3.6.1.4.1.99.3.1.0]%",
		"c1": "c1=%parm[#2]%",
		"s2": "s2=%parm[1.3.6.1.4.1.99.3.3.0]%",
		"c2": "c2=%parm[#4]%",
	}
	for obj, want := range checks {
		if got := logmsgLine(evt, obj); got != want {
			t.Errorf("%s line = %q, want %q", obj, got, want)
		}
	}
}

func TestFromModuleForcePositional(t *testing.T) {
	events := FromModule("TEST-MIB", []Notification{
		notif("mixed", "1.3.6.1.4.1.99.3.0.1",
			scalar("s1", "1.3.6.1.4.1.99.3.1"),
			column("c1", "1.3.6.1.4.1.99.3.2.1"),
		),
	}, Options{UEIBase: "uei.opennms.org/traps", ForcePositional: true})

	evt := events.Events[0]
	if want := "s1=%parm[#1]%"; logmsgLine(evt, "s1") != want {
		t.Errorf("s1 line = %q, want %q (force positional)", logmsgLine(evt, "s1"), want)
	}
	if want := "c1=%parm[#2]%"; logmsgLine(evt, "c1") != want {
		t.Errorf("c1 line = %q, want %q", logmsgLine(evt, "c1"), want)
	}
}

func TestFromModuleVarbindsdecodeUsesEnum(t *testing.T) {
	n := notif("statusChange", "1.3.6.1.4.1.99.4.0.1",
		model.Symbol{
			Name: "opStatus", OID: "1.3.6.1.4.1.99.4.1", Kind: model.KindScalar,
			EnumValues: []model.EnumValue{{Name: "up", Number: 1}, {Name: "down", Number: 2}},
		},
	)
	events := FromModule("TEST-MIB", []Notification{n}, Options{UEIBase: "uei.opennms.org/traps"})
	evt := events.Events[0]
	if len(evt.Varbindsdecode) != 1 {
		t.Fatalf("got %d varbindsdecode, want 1", len(evt.Varbindsdecode))
	}
	vbd := evt.Varbindsdecode[0]
	if vbd.Parmid != "parm[1.3.6.1.4.1.99.4.1.0]" {
		t.Errorf("parmid = %q", vbd.Parmid)
	}
	if len(vbd.Decode) != 2 || vbd.Decode[0].Varbindvalue != "1" || vbd.Decode[0].Varbinddecodedstring != "up" {
		t.Errorf("decode = %+v", vbd.Decode)
	}
}
