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
	"github.com/no42-org/blittermib/internal/textutil"
)

// Notification pairs a NOTIFICATION-TYPE/TRAP-TYPE symbol with its
// objects (varbinds) in OBJECTS-clause order. It is the input carrier
// for FromModule; the store populates it from the reference table's
// position-ordered rows.
type Notification struct {
	Symbol       model.Symbol
	Objects      []model.Symbol
	Relationship Relationship
}

// Relationship carries the inferred classification for a notification,
// supplied by the caller to drive alarm-data emission. It is decoupled
// from internal/correlate so eventconf stays a leaf package: the caller
// maps a correlate result onto these fields.
type Relationship struct {
	// AlarmType is "1"/"2"/"3" (raise/clear/notification) or "" when the
	// notification is unclassified — in which case no alarm-data is
	// emitted.
	AlarmType string
	// Clears names the raise notification(s) this clear resolves. Used
	// for clear-key generation in Story 2.2; unused here.
	Clears []string
	// Provenance is a human explanation of the inference (basis +
	// confidence), emitted as an XML comment beside the alarm-data so
	// the export is auditable without the UI. Empty = no comment.
	Provenance string
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
//
// Two passes: the first builds every event with its entity-scoped
// reduction-key; the second sets each clear's clear-key to the
// reduction-key of the raise it resolves (with %uei% bound to the
// raise's literal UEI), so a resolution clears exactly its problem
// alarm and only that one (the pairwise round-trip, FR24/NFR11).
func FromModule(moduleName string, notifs []Notification, opts Options) Events {
	base := strings.TrimRight(opts.UEIBase, "/")

	// Index notifications and pair each with its partner so a
	// reduction-key can be scoped by the varbind the pair actually
	// shares (the correlating index), not an arbitrary first object.
	byName := make(map[string]Notification, len(notifs))
	for _, n := range notifs {
		byName[n.Symbol.Name] = n
	}
	partner := make(map[string]string)
	for _, n := range notifs {
		for _, raise := range n.Relationship.Clears {
			if _, ok := partner[n.Symbol.Name]; !ok {
				partner[n.Symbol.Name] = raise
			}
			if _, ok := partner[raise]; !ok {
				partner[raise] = n.Symbol.Name
			}
		}
	}

	out := Events{Events: make([]Event, 0, len(notifs))}
	redKeyByName := make(map[string]string)
	for _, n := range notifs {
		tok := instanceToken(n, byName[partner[n.Symbol.Name]], opts.ForcePositional)
		evt := buildEvent(moduleName, n, base, opts.ForcePositional, tok)
		if evt.AlarmData != nil {
			// Bind %uei% to this event's literal UEI so the stored key
			// can be referenced as a clear-key by its partner.
			redKeyByName[n.Symbol.Name] = strings.Replace(evt.AlarmData.ReductionKey, "%uei%", evt.UEI, 1)
		}
		out.Events = append(out.Events, evt)
	}
	for i := range out.Events {
		ad := out.Events[i].AlarmData
		if ad == nil || ad.AlarmType != AlarmTypeClear {
			continue
		}
		// A clear resolves its raise: clear-key == the raise's
		// reduction-key. Use the first raise (genuine pairs are 1:1;
		// fan-out is capped to Likely upstream and gated from export).
		for _, raiseName := range notifs[i].Relationship.Clears {
			if rk, ok := redKeyByName[raiseName]; ok {
				ad.ClearKey = rk
				break
			}
		}
	}
	return out
}

// instanceToken returns the %parm[...]% token for the varbind this
// notification shares (by OID) with its pair partner — the correlating
// index that scopes the alarm per entity. It is empty when there is no
// partner or no shared varbind, in which case the reduction-key stays
// node-scoped rather than guessing an arbitrary object (which could
// over-clear unrelated instances). The positional case relies on the
// shared varbind sitting at the same OBJECTS position in both members,
// which holds for genuine pairs that share their OBJECTS structure.
func instanceToken(n, partner Notification, forcePositional bool) string {
	if n.Relationship.AlarmType == "" || len(n.Objects) == 0 || len(partner.Objects) == 0 {
		return ""
	}
	partnerOIDs := make(map[string]bool, len(partner.Objects))
	for _, o := range partner.Objects {
		if o.OID != "" {
			partnerOIDs[o.OID] = true
		}
	}
	for i, o := range n.Objects {
		if o.OID != "" && partnerOIDs[o.OID] {
			return "%" + parmName(o, i+1, forcePositional) + "%"
		}
	}
	return ""
}

func buildEvent(moduleName string, n Notification, ueibase string, forcePositional bool, instanceTok string) Event {
	evt := Event{
		UEI:        ueibase + "/" + n.Symbol.Name,
		EventLabel: moduleName + " defined trap event: " + n.Symbol.Name,
		Descr:      textutil.CollapseWhitespace(n.Symbol.Description),
		Severity:   "Indeterminate",
		Logmsg:     buildLogmsg(n, forcePositional),
	}
	if mask := buildMask(n.Symbol.OID); mask != nil {
		evt.Mask = mask
	}
	evt.Varbindsdecode = buildVarbindsdecode(n, forcePositional)
	if ad := buildAlarmData(n.Relationship, instanceTok); ad != nil {
		evt.AlarmData = ad
		// commentSafe guards against `--`, which XML forbids in comments.
		evt.Provenance = commentSafe(n.Relationship.Provenance)
	}
	return evt
}

// buildAlarmData emits the <alarm-data> for a classified notification.
// The reduction-key is node-scoped (%uei%:%dpname%:%nodeid%) plus the
// per-instance discriminator instanceTok (the token of the correlating
// varbind the pair shares, supplied by FromModule). The instance token
// keeps the alarm per-entity so a key never clears unrelated instances
// (FR21, NFR12). The clear-key is filled in by FromModule's second
// pass. An unclassified notification (empty AlarmType) emits no
// alarm-data.
func buildAlarmData(rel Relationship, instanceTok string) *AlarmData {
	if rel.AlarmType == "" {
		return nil
	}
	key := "%uei%:%dpname%:%nodeid%"
	if instanceTok != "" {
		key += ":" + instanceTok
	}
	return &AlarmData{
		ReductionKey: key,
		AlarmType:    rel.AlarmType,
	}
}

// buildLogmsg renders "{name} trap received" followed by each
// "{object}={paramToken}" varbind, all on a single line separated by
// spaces. No HTML wrapper, and — by avoiding newlines — no encoding/xml
// &#xA; escaping in the marshalled chardata.
func buildLogmsg(n Notification, forcePositional bool) Logmsg {
	parts := make([]string, 0, len(n.Objects)+1)
	parts = append(parts, n.Symbol.Name+" trap received")
	for i, obj := range n.Objects {
		parts = append(parts, obj.Name+"="+paramToken(obj, i+1, forcePositional))
	}
	return Logmsg{Dest: "logndisplay", Content: strings.Join(parts, " ")}
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
