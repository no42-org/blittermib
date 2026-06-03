/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package eventconf

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/no42-org/blittermib/internal/model"
)

// Notification pairs a NOTIFICATION-TYPE/TRAP-TYPE symbol with its
// objects (varbinds) in OBJECTS-clause order. It is the input carrier
// for FromModule; the store populates it from the reference table's
// position-ordered rows.
type Notification struct {
	Symbol  model.Symbol
	Objects []model.Symbol
}

// Options tune the generated events.
type Options struct {
	// UEIBase is the prefix for every event UEI; the per-event UEI is
	// "{UEIBase}/{notificationName}". Trailing slashes are normalized.
	UEIBase string
	// ForcePositional emits %parm[#N]% for every varbind regardless of
	// kind, reproducing legacy mib2events output. When false, scalar /
	// accessible-for-notify objects use the OID form %parm[{oid}.0]%.
	ForcePositional bool
}

// FromModule converts a module's notifications into an eventconf
// document.
func FromModule(moduleName string, notifs []Notification, opts Options) Events {
	base := strings.TrimRight(opts.UEIBase, "/")
	out := Events{Events: make([]Event, 0, len(notifs))}
	for _, n := range notifs {
		out.Events = append(out.Events, buildEvent(moduleName, n, base, opts.ForcePositional))
	}
	return out
}

func buildEvent(moduleName string, n Notification, ueibase string, forcePositional bool) Event {
	evt := Event{
		UEI:        ueibase + "/" + n.Symbol.Name,
		EventLabel: moduleName + " defined trap event: " + n.Symbol.Name,
		Descr:      n.Symbol.Description,
		Severity:   "Indeterminate",
		Logmsg:     buildLogmsg(n, forcePositional),
	}
	if mask := buildMask(n.Symbol.OID); mask != nil {
		evt.Mask = mask
	}
	evt.Varbindsdecode = buildVarbindsdecode(n, forcePositional)
	return evt
}

// buildLogmsg renders "{name} trap received" followed by one
// "{object}={paramToken}" line per varbind. No HTML wrapper.
func buildLogmsg(n Notification, forcePositional bool) Logmsg {
	lines := make([]string, 0, len(n.Objects)+1)
	lines = append(lines, n.Symbol.Name+" trap received")
	for i, obj := range n.Objects {
		lines = append(lines, obj.Name+"="+paramToken(obj, i+1, forcePositional))
	}
	return Logmsg{Dest: "logndisplay", Content: strings.Join(lines, "\n")}
}

// buildMask derives the id / generic / specific trap-matching mask.
// Returns nil when the OID is too short to split (no specific-type).
func buildMask(oid string) *Mask {
	enterprise, specific, ok := splitTrapOID(oid)
	if !ok {
		return nil
	}
	return &Mask{Maskelements: []Maskelement{
		{Mename: "id", Mevalue: []string{enterprise}},
		{Mename: "generic", Mevalue: []string{"6"}},
		{Mename: "specific", Mevalue: []string{specific}},
	}}
}

// buildVarbindsdecode emits one entry per enum-typed object, mapping
// each enum value to its name. The parmid uses the same hybrid form
// (OID for scalars, position otherwise) as the logmsg token.
func buildVarbindsdecode(n Notification, forcePositional bool) []Varbindsdecode {
	var out []Varbindsdecode
	for i, obj := range n.Objects {
		if len(obj.EnumValues) == 0 {
			continue
		}
		decodes := make([]Decode, 0, len(obj.EnumValues))
		for _, ev := range obj.EnumValues {
			decodes = append(decodes, Decode{
				Varbindvalue:         strconv.FormatInt(ev.Number, 10),
				Varbinddecodedstring: ev.Name,
			})
		}
		out = append(out, Varbindsdecode{
			Parmid: parmName(obj, i+1, forcePositional),
			Decode: decodes,
		})
	}
	return out
}

// paramToken is the %parm[...]% substitution token used in the logmsg.
func paramToken(obj model.Symbol, position int, forcePositional bool) string {
	return "%" + parmName(obj, position, forcePositional) + "%"
}

// parmName is the inner parm reference (e.g. "parm[#1]" or
// "parm[1.3.6.1.2.1.1.0]"). A scalar or accessible-for-notify object
// has a fixed ".0" instance, so it can be referenced by OID — robust
// against varbind reordering (the NMS-19070 principle). A columnar
// object's instance is a dynamic table index unknown at generation
// time, so it falls back to its 1-based position among all objects.
//
// The OID is emitted WITHOUT a leading dot. At runtime OpenNMS names a
// trap varbind parameter by SnmpObjId.toString(), which separates
// sub-identifiers with dots but emits no leading dot (SnmpObjId.java);
// %parm[NAME]% then resolves by an exact whitespace-trimmed string
// compare (AbstractEventUtil.getParmTrim). A leading dot here would
// not match the parameter name and the token would expand to nothing.
func parmName(obj model.Symbol, position int, forcePositional bool) string {
	if !forcePositional && (obj.Kind == model.KindScalar || obj.Access == model.AccessAccessibleNotify) {
		return "parm[" + noLeadingDot(obj.OID) + ".0]"
	}
	return fmt.Sprintf("parm[#%d]", position)
}

// noLeadingDot strips a leading dot from a dotted OID so the emitted
// parameter name matches the dot-less SnmpObjId.toString() form
// OpenNMS assigns to trap varbinds. Store OIDs are already dot-less;
// this guards against a future dotted source.
func noLeadingDot(oid string) string {
	return strings.TrimPrefix(oid, ".")
}
