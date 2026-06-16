/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

// Package correlate infers raise/clear/orphan relationships between a
// module's SNMP notifications from already-compiled symbols and
// references.
//
// It is a PURE package: no database, no IO, no globals, no time or
// randomness. Classify is a deterministic function of its inputs — the
// same module yields byte-identical, stably-ordered output every run —
// so the inference can be golden-tested without a store, and the store
// can rebuild the derived tables on every ingest without surprises.
//
// The four MVP signals (name-token, varbind-signature,
// DESCRIPTION-prose, NOTIFICATION-GROUP/OID-sibling) land in later
// stories; this scaffold establishes the vocabulary constants, the
// Evidence contract, and the Classify entry point.
package correlate

import (
	"sort"
	"strings"

	"github.com/no42-org/blittermib/internal/model"
)

// Classification is the per-notification verdict. The string values
// are load-bearing: they are persisted verbatim and map directly to
// the OpenNMS alarm-type (raise=1, clear=2, orphan=3).
type Classification string

const (
	ClassRaise  Classification = "raise"
	ClassClear  Classification = "clear"
	ClassOrphan Classification = "orphan"
)

// Confidence is the inference confidence band. Cutoffs are calibrated
// (Story 1.6) so High meets the precision target; only High-confidence
// relationships are eligible for alarm-data export.
type Confidence string

const (
	ConfHigh   Confidence = "high"
	ConfLikely Confidence = "likely"
	ConfGuess  Confidence = "guess"
)

// SignalKind names one of the four independent inference signals.
type SignalKind string

const (
	SignalName        SignalKind = "name"
	SignalVarbind     SignalKind = "varbind"
	SignalDescription SignalKind = "description"
	SignalGroup       SignalKind = "group"
)

// Relationship is the inference result for one notification. Clears
// lists the raise notification(s) this notification resolves; it is
// empty unless Class == ClassClear.
type Relationship struct {
	Notification string
	Class        Classification
	Confidence   Confidence
	Evidence     Evidence
	Clears       []string
}

// maxPairingGroupMembers bounds the notification membership of a
// NOTIFICATION-GROUP for it to count as a pairing signal. A 2-member
// group identifies a pair (linkUp/linkDown, BGP established/transition);
// larger groups bundle unrelated notifications and are ignored.
const maxPairingGroupMembers = 2

// Classify infers relationships for every NOTIFICATION-TYPE/TRAP-TYPE
// in syms, using refs for the varbind signal.
//
// syms/refs are expected to be a SINGLE module's symbols and references
// (notification names are unique within a module per the store's
// (module_name, name) constraint); Classify keys its working maps by
// bare name and is not designed for multi-module input.
//
// Contract: deterministic (identical input → identical, stably-ordered
// output), never panics on malformed input, and never returns an error
// — inference is best-effort enrichment, never a gate on ingest.
//
// It pairs opposing-direction notifications that share a grouping
// signal — same name root (1.2) or membership in the same SMALL
// NOTIFICATION-GROUP (1.4) — using four signals: name-token,
// varbind-signature, DESCRIPTION-prose, and group membership. Direction
// comes from the name when present, else from the description, so pairs
// whose names carry no directional token (e.g. BGP
// backward-transition/established) still pair. It refuses to cross-pair
// current with deprecated/obsolete near-duplicates (1.3) and classifies
// every unpaired notification as an orphan (1.3). Confidence rises with
// signal agreement; Story 1.5 calibrates the bands and Story 1.6
// measures precision/recall.
//
// Pairing is O(n²) in the module's notification count, which is small
// in practice (tens; inference runs once per ingest). A group is only
// treated as a pairing signal when it binds at most maxPairingGroupMembers
// notifications: a group that bundles many notifications says nothing
// about which pairs with which, so counting it would combinatorially
// over-pair.
func Classify(syms []model.Symbol, refs []model.Reference) []Relationship {
	notifs := make([]model.Symbol, 0)
	for _, s := range syms {
		if s.Kind == model.KindNotificationType || s.Kind == model.KindTrapType {
			notifs = append(notifs, s)
		}
	}
	if len(notifs) == 0 {
		return nil
	}
	// Sort by name so all downstream iteration is deterministic.
	sort.Slice(notifs, func(i, j int) bool { return notifs[i].Name < notifs[j].Name })

	vbAll := varbindSets(refs)
	grpAll := groupSets(refs)

	// A NOTIFICATION-GROUP is a trustworthy pairing signal only when it
	// binds a small set of notifications (e.g. the 2-member
	// linkUpDownNotificationsGroup / bgp4MIBNotificationGroup). Count the
	// notification members per group; groups above the threshold are
	// ignored as a pairing signal to avoid combinatorial over-pairing.
	notifNames := make(map[string]bool, len(notifs))
	for _, n := range notifs {
		notifNames[n.Name] = true
	}
	groupNotifCount := make(map[string]int)
	for _, r := range refs {
		if r.Kind == model.RefGroupMember && notifNames[r.TargetName] {
			groupNotifCount[r.SourceModule+"::"+r.SourceName]++
		}
	}

	// Per-notification facts. eff is the name direction when present,
	// else the description direction. groups holds only small (pairing)
	// groups the notification belongs to.
	type ninfo struct {
		nameRoot   string
		nameDir    direction
		descDir    direction
		descPhrase string
		vb         map[string]bool
		groups     map[string]bool
		status     model.Status
		eff        direction
	}
	info := make(map[string]*ninfo, len(notifs))
	for _, n := range notifs {
		root, nd, _ := splitDirection(tokenize(n.Name))
		dd, ph := descriptionDirection(n.Description, n.Reference)
		eff := nd
		if eff == dirNone {
			eff = dd
		}
		var smallGroups map[string]bool
		for g := range grpAll[n.Name] {
			if c := groupNotifCount[g]; c >= 2 && c <= maxPairingGroupMembers {
				if smallGroups == nil {
					smallGroups = make(map[string]bool)
				}
				smallGroups[g] = true
			}
		}
		info[n.Name] = &ninfo{
			nameRoot: root, nameDir: nd, descDir: dd, descPhrase: ph,
			vb: vbAll[n.Name], groups: smallGroups,
			status: n.Status, eff: eff,
		}
	}

	// acc records which signals paired a notification and its partners.
	type acc struct {
		class    Classification
		partners []string
		sigName  bool
		sigVb    bool
		sigDesc  bool
		sigGroup bool
		vbEx     string
		groupEx  string
	}
	rels := make(map[string]*acc)
	get := func(name string, class Classification) *acc {
		a := rels[name]
		if a == nil {
			a = &acc{class: class}
			rels[name] = a
		}
		return a
	}

	// Candidate pairs: every opposing-direction pair that shares a
	// grouping signal (same name-root, NOTIFICATION-GROUP, or OID
	// parent). Iterated over the name-sorted notifs by index, and each
	// (i,j) is visited once, so pairing and partner lists are
	// deterministic with no duplicates.
	for i := 0; i < len(notifs); i++ {
		for j := i + 1; j < len(notifs); j++ {
			a, b := info[notifs[i].Name], info[notifs[j].Name]
			var raise, clear string
			switch {
			case a.eff == dirRaise && b.eff == dirClear:
				raise, clear = notifs[i].Name, notifs[j].Name
			case a.eff == dirClear && b.eff == dirRaise:
				raise, clear = notifs[j].Name, notifs[i].Name
			default:
				continue
			}
			ra, ca := info[raise], info[clear]
			// Never cross-pair current with deprecated/obsolete (FR5).
			if !statusCompatible(ra.status, ca.status) {
				continue
			}
			sameRoot := ra.nameDir != dirNone && ca.nameDir != dirNone &&
				ra.nameRoot != "" && ra.nameRoot == ca.nameRoot
			grpEx := firstShared(ra.groups, ca.groups)
			if !sameRoot && grpEx == "" {
				continue // no grouping signal → don't link arbitrary notifications
			}
			vbEx := firstShared(ra.vb, ca.vb)
			descOpposing := ra.descDir == dirRaise && ca.descDir == dirClear

			rAcc, cAcc := get(raise, ClassRaise), get(clear, ClassClear)
			rAcc.partners = append(rAcc.partners, clear)
			cAcc.partners = append(cAcc.partners, raise)
			for _, x := range []*acc{rAcc, cAcc} {
				if sameRoot {
					x.sigName = true
				}
				if vbEx != "" {
					x.sigVb = true
					if x.vbEx == "" {
						x.vbEx = shortVarbind(vbEx)
					}
				}
				if descOpposing {
					x.sigDesc = true
				}
				if grpEx != "" && !x.sigGroup {
					x.sigGroup = true
					x.groupEx = "NOTIFICATION-GROUP " + shortVarbind(grpEx)
				}
			}
		}
	}

	// Emit one relationship per notification, in name order. Paired
	// notifications are raise/clear; everything else is an orphan
	// (a problem with no clear, or a standalone/informational
	// notification — both alarm-type 3).
	out := make([]Relationship, 0, len(notifs))
	for _, n := range notifs {
		name := n.Name
		ni := info[name]
		a := rels[name]
		if a == nil {
			summary := "no resolution found"
			switch {
			case ni.eff == dirRaise:
				summary = "problem with no matching clear notification"
			case ni.eff == dirClear:
				summary = "resolution with no matching problem notification"
			case len(ni.vb) == 0:
				summary = "standalone notification (no varbinds); no resolution"
			}
			out = append(out, Relationship{
				Notification: name,
				Class:        ClassOrphan,
				Confidence:   ConfHigh,
				Evidence:     Evidence{Summary: summary},
			})
			continue
		}
		sort.Strings(a.partners)

		var sigs []SignalHit
		if a.sigName {
			sigs = append(sigs, SignalHit{Kind: SignalName, Detail: "matching name root with opposing directional token"})
		}
		if a.sigVb {
			sigs = append(sigs, SignalHit{Kind: SignalVarbind, Detail: "shared correlating varbind " + a.vbEx})
		}
		if a.sigDesc {
			sigs = append(sigs, SignalHit{Kind: SignalDescription, Detail: "DESCRIPTION direction: " + ni.descPhrase})
		}
		if a.sigGroup {
			sigs = append(sigs, SignalHit{Kind: SignalGroup, Detail: a.groupEx})
		}

		// A notification whose own name and description directions
		// disagree is an uncertain signal; so is a one-to-many pairing.
		conflict := ni.nameDir != dirNone && ni.descDir != dirNone && ni.nameDir != ni.descDir
		conf := scoreConfidence(
			signalSet{name: a.sigName, varbind: a.sigVb, desc: a.sigDesc, group: a.sigGroup},
			len(a.partners), conflict,
		)

		summary := "problem; cleared by " + strings.Join(a.partners, ", ")
		if a.class == ClassClear {
			summary = "clears " + strings.Join(a.partners, ", ")
		}
		rel := Relationship{
			Notification: name,
			Class:        a.class,
			Confidence:   conf,
			Evidence:     Evidence{Signals: sigs, Summary: summary},
		}
		if a.class == ClassClear {
			rel.Clears = append([]string(nil), a.partners...)
		}
		out = append(out, rel)
	}
	return out
}
