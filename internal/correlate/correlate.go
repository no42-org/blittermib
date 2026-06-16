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
// Contract: deterministic (identical input → identical, stably-ordered
// output), never panics on malformed input, and never returns an error
// — inference is best-effort enrichment, never a gate on ingest.
//
// Story 1.2: pairs directionally-named notifications (e.g.
// linkDown/linkUp) by shared root + opposing tokens, confirmed by a
// shared correlating varbind. Orphan detection and STATUS handling
// (1.3), the description/group signals (1.4), and calibrated confidence
// scoring (1.5) build on this. A notification not yet matched to a pair
// produces no row until 1.3 classifies the remainder as orphans.
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

	// Direction per notification + group by root.
	dir := make(map[string]direction)
	tok := make(map[string]string)
	byRoot := make(map[string][]string)
	for _, n := range notifs {
		root, d, t := splitDirection(tokenize(n.Name))
		dir[n.Name] = d
		tok[n.Name] = t
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

	if len(rels) == 0 {
		return nil
	}
	names := make([]string, 0, len(rels))
	for n := range rels {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Relationship, 0, len(rels))
	for _, name := range names {
		a := rels[name]
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
		rel := Relationship{Notification: name, Class: a.class, Confidence: conf, Evidence: ev}
		if a.class == ClassClear {
			rel.Clears = append([]string(nil), a.partners...)
			ev.Summary = "clears " + strings.Join(a.partners, ", ")
		} else {
			ev.Summary = "problem; cleared by " + strings.Join(a.partners, ", ")
		}
		rel.Evidence.Summary = ev.Summary
		out = append(out, rel)
	}
	return out
}
