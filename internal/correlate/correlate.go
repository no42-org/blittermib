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
	"fmt"
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
// It pairs directionally-named notifications (e.g. linkDown/linkUp) by
// shared root + opposing tokens, confirmed by a shared correlating
// varbind (1.2); refuses to cross-pair current with deprecated/obsolete
// near-duplicates (1.3); and classifies every unpaired notification as
// an orphan — a problem with no clear, or a standalone/informational
// notification (1.3). The description/group signals (1.4) and
// calibrated confidence scoring (1.5) build on this.
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

	vb := varbindSets(refs)

	// Direction + STATUS per notification, grouped by root.
	dir := make(map[string]direction)
	tok := make(map[string]string)
	status := make(map[string]model.Status)
	byRoot := make(map[string][]string)
	for _, n := range notifs {
		root, d, t := splitDirection(tokenize(n.Name))
		dir[n.Name] = d
		tok[n.Name] = t
		status[n.Name] = n.Status
		if d != dirNone {
			byRoot[root] = append(byRoot[root], n.Name)
		}
	}

	// Build clear→raise edges and per-notification evidence within each
	// root that has both a raise and a clear member.
	type acc struct {
		class    Classification
		shared   bool
		varbind  string // example shared varbind (human-readable)
		token    string
		partners []string // raises (for a clear) or clears (for a raise)
	}
	rels := make(map[string]*acc)
	get := func(name string, class Classification) *acc {
		a := rels[name]
		if a == nil {
			a = &acc{class: class, token: tok[name]}
			rels[name] = a
		}
		return a
	}

	roots := make([]string, 0, len(byRoot))
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Strings(roots)

	for _, root := range roots {
		var raises, clears []string
		for _, nm := range byRoot[root] {
			switch dir[nm] {
			case dirRaise:
				raises = append(raises, nm)
			case dirClear:
				clears = append(clears, nm)
			}
		}
		if len(raises) == 0 || len(clears) == 0 {
			continue // no opposing partner in this root → left for 1.3 (orphan)
		}
		for _, c := range clears {
			for _, r := range raises {
				// Never cross-pair a current notification with a
				// deprecated/obsolete near-duplicate (FR5).
				if !statusCompatible(status[c], status[r]) {
					continue
				}
				ca, ra := get(c, ClassClear), get(r, ClassRaise)
				ca.partners = append(ca.partners, r)
				ra.partners = append(ra.partners, c)
				if s := sharedVarbind(vb[c], vb[r]); s != "" {
					for _, a := range []*acc{ca, ra} {
						a.shared = true
						if a.varbind == "" {
							a.varbind = shortVarbind(s)
						}
					}
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
		a := rels[name]
		if a == nil {
			summary := "no resolution found"
			switch {
			case dir[name] == dirRaise:
				summary = "problem with no matching clear notification"
			case dir[name] == dirClear:
				summary = "resolution with no matching problem notification"
			case len(vb[name]) == 0:
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
		conf := ConfLikely
		ev := Evidence{Signals: []SignalHit{{
			Kind:   SignalName,
			Detail: fmt.Sprintf("directional token %q with a matching-root partner", a.token),
		}}}
		if a.shared {
			conf = ConfHigh
			ev.Signals = append(ev.Signals, SignalHit{
				Kind:   SignalVarbind,
				Detail: "shared correlating varbind " + a.varbind,
			})
		}
		if a.class == ClassClear {
			ev.Summary = "clears " + strings.Join(a.partners, ", ")
		} else {
			ev.Summary = "problem; cleared by " + strings.Join(a.partners, ", ")
		}
		rel := Relationship{Notification: name, Class: a.class, Confidence: conf, Evidence: ev}
		if a.class == ClassClear {
			rel.Clears = append([]string(nil), a.partners...)
		}
		out = append(out, rel)
	}
	return out
}
