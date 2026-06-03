/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package eventconf

import (
	"strings"
	"testing"
)

func TestMarshalCommentSafe(t *testing.T) {
	// A module name with a double dash must not produce "--" inside the
	// XML comment header (illegal XML); commentSafe collapses dash runs.
	out, err := Marshal(Events{Events: []Event{{UEI: "x", EventLabel: "x", Severity: "Indeterminate"}}}, "FOO--BAR---BAZ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "<!-- FOO-BAR-BAZ -->") {
		t.Errorf("comment not collapsed:\n%s", got)
	}
	if strings.Contains(got, "--BAR") || strings.Contains(got, "FOO--") {
		t.Errorf("output still contains a double dash:\n%s", got)
	}
}

func TestMarshalGolden(t *testing.T) {
	events := Events{Events: []Event{{
		Mask: &Mask{Maskelements: []Maskelement{
			{Mename: "id", Mevalue: []string{".1.3.6.1.4.1.99.1"}},
			{Mename: "generic", Mevalue: []string{"6"}},
			{Mename: "specific", Mevalue: []string{"1"}},
		}},
		UEI:        "uei.opennms.org/traps/TEST-MIB/alarmRaised",
		EventLabel: "TEST-MIB defined trap event: alarmRaised",
		Descr:      "Raised when x < y & z fails",
		Logmsg:     Logmsg{Dest: "logndisplay", Content: "alarmRaised trap received"},
		Severity:   "Indeterminate",
		Varbindsdecode: []Varbindsdecode{{
			Parmid: "parm[.1.3.6.1.4.1.99.1.0]",
			Decode: []Decode{{Varbindvalue: "1", Varbinddecodedstring: "cleared"}},
		}},
	}}}

	out, err := Marshal(events, "TEST-MIB")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(out)

	wantContains := []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<!-- TEST-MIB -->`,
		`<events xmlns="` + Namespace + `">`,
		`<mename>id</mename>`,
		`<logmsg dest="logndisplay">`,
	}
	for _, w := range wantContains {
		if !strings.Contains(got, w) {
			t.Errorf("output missing %q\n---\n%s", w, got)
		}
	}

	// DESCRIPTION special characters are XML-escaped, not emitted raw.
	if !strings.Contains(got, "x &lt; y &amp; z fails") {
		t.Errorf("descr not escaped:\n%s", got)
	}

	// XSD sequence order: mask < uei < event-label < descr < logmsg <
	// severity < varbindsdecode.
	order := []string{"<mask>", "<uei>", "<event-label>", "<descr>", "<logmsg ", "<severity>", "<varbindsdecode>"}
	last := -1
	for _, tag := range order {
		idx := strings.Index(got, tag)
		if idx < 0 {
			t.Fatalf("missing element %q", tag)
		}
		if idx < last {
			t.Errorf("element %q out of XSD order:\n%s", tag, got)
		}
		last = idx
	}
}
