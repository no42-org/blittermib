/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

// Package eventconf projects parsed MIB notifications into OpenNMS
// eventconf XML — the `<events>` document the OpenNMS trap daemon
// uses to recognize and format SNMP traps. This is the inverse
// direction of internal/compile (which parses MIBs into the model);
// the two share no logic.
package eventconf

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// Namespace is the eventconf XSD default namespace. It is emitted as
// a literal `xmlns` attribute on the root so child elements stay
// unprefixed (matching OpenNMS's own output) rather than relying on
// encoding/xml's namespace propagation, which would stamp `xmlns=""`
// on every child.
const Namespace = "http://xmlns.opennms.org/xsd/eventconf"

// Events is the root `<events>` document.
type Events struct {
	XMLName xml.Name `xml:"events"`
	Xmlns   string   `xml:"xmlns,attr"`
	Events  []Event  `xml:"event"`
}

// Event is one `<event>`. Field order matches the eventconf XSD
// sequence (mask, uei, event-label, descr, logmsg, severity,
// varbindsdecode, …, alarm-data) — OpenNMS validates against an ordered
// schema, so the order is load-bearing, not cosmetic. `alarm-data`
// follows `varbindsdecode` in the schema's `event` sequence (the
// intervening optional elements — script, mouseovertext — are not
// emitted).
type Event struct {
	Mask           *Mask            `xml:"mask,omitempty"`
	UEI            string           `xml:"uei"`
	EventLabel     string           `xml:"event-label"`
	Descr          string           `xml:"descr"`
	Logmsg         Logmsg           `xml:"logmsg"`
	Severity       string           `xml:"severity"`
	Varbindsdecode []Varbindsdecode `xml:"varbindsdecode,omitempty"`
	// Provenance is an XML comment (empty = omitted) explaining the
	// alarm-data inference; it precedes the alarm-data element.
	Provenance string     `xml:",comment"`
	AlarmData  *AlarmData `xml:"alarm-data,omitempty"`
}

// AlarmType values map an inferred classification to the OpenNMS
// `alarm-data/@alarm-type`: 1 = problem (raise), 2 = resolution
// (clear), 3 = notification with no resolution (orphan).
const (
	AlarmTypeRaise        = "1"
	AlarmTypeClear        = "2"
	AlarmTypeNotification = "3"
)

// AlarmData is the `<alarm-data>` element that turns an event into an
// OpenNMS alarm. `reduction-key` is required by the schema; `alarm-type`
// drives pairwise correlation; `clear-key` (Story 2.2) lets a clear
// resolve its problem alarm.
type AlarmData struct {
	ReductionKey string `xml:"reduction-key,attr"`
	AlarmType    string `xml:"alarm-type,attr,omitempty"`
	ClearKey     string `xml:"clear-key,attr,omitempty"`
}

// Mask carries the trap-matching elements (id / generic / specific).
type Mask struct {
	Maskelements []Maskelement `xml:"maskelement"`
}

// Maskelement is one `<maskelement>` name/value pair. mevalue is a
// slice because the eventconf schema permits multiple values per
// element; trap masks generated here always use exactly one.
type Maskelement struct {
	Mename  string   `xml:"mename"`
	Mevalue []string `xml:"mevalue"`
}

// Logmsg is the short event message with its notification
// destination.
type Logmsg struct {
	Dest    string `xml:"dest,attr"`
	Content string `xml:",chardata"`
}

// Varbindsdecode maps one parameter's values to human-readable
// strings (the enum decode for an enumerated varbind).
type Varbindsdecode struct {
	Parmid string   `xml:"parmid"`
	Decode []Decode `xml:"decode"`
}

// Decode is one value→string mapping inside a Varbindsdecode.
type Decode struct {
	Varbindvalue         string `xml:"varbindvalue,attr"`
	Varbinddecodedstring string `xml:"varbinddecodedstring,attr"`
}

// Marshal renders an Events document as a standalone eventconf XML
// file: an XML declaration, a `<!-- module -->` comment header (so a
// human can see which MIB produced the file), then the indented
// `<events>` body.
func Marshal(events Events, module string) ([]byte, error) {
	events.Xmlns = Namespace
	body, err := xml.MarshalIndent(events, "", "   ")
	if err != nil {
		return nil, fmt.Errorf("marshal events: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header) // "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"
	fmt.Fprintf(&buf, "<!-- %s -->\n", commentSafe(module))
	buf.Write(body)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// commentSafe collapses any run of dashes to a single dash so the
// value cannot contain `--`, which XML forbids inside a comment and
// which would otherwise let a module name break out of the `<!-- -->`
// header. (SMI module names can't contain `--`, but Marshal is public
// and the guard is cheap.)
func commentSafe(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
