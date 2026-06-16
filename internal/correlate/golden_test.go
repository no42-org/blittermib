/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package correlate

import (
	"sort"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// The golden set is the labeled ground truth for inference: real
// standard-MIB notification scenarios with their known correct
// classification. It is the oracle the broader corpus lacks (Story
// 1.6). CI runs it on every build; a regression fails verify (NFR5).

type want struct {
	class  Classification
	clears []string
}

type goldenCase struct {
	name string
	syms []model.Symbol
	refs []model.Reference
	want map[string]want
}

func nt(mod, name string, status model.Status, desc string) model.Symbol {
	return model.Symbol{ModuleName: mod, Name: name, Kind: model.KindNotificationType, Status: status, Description: desc}
}

func obj(mod, src, tgt string) model.Reference {
	return model.Reference{SourceModule: mod, SourceName: src, TargetModule: mod, TargetName: tgt, Kind: model.RefNotificationObject}
}

func grpMember(mod, group, member string) model.Reference {
	return model.Reference{SourceModule: mod, SourceName: group, TargetModule: mod, TargetName: member, Kind: model.RefGroupMember}
}

func goldenSet() []goldenCase {
	return []goldenCase{
		{
			name: "IF-MIB linkDown/linkUp",
			syms: []model.Symbol{
				nt("IF-MIB", "linkDown", model.StatusCurrent, "ifOperStatus is about to enter the down state"),
				nt("IF-MIB", "linkUp", model.StatusCurrent, "ifOperStatus left the down state"),
			},
			refs: []model.Reference{
				obj("IF-MIB", "linkDown", "ifIndex"), obj("IF-MIB", "linkUp", "ifIndex"),
			},
			want: map[string]want{
				"linkDown": {ClassRaise, nil},
				"linkUp":   {ClassClear, []string{"linkDown"}},
			},
		},
		{
			name: "orphans (entConfigChange, authenticationFailure)",
			syms: []model.Symbol{
				nt("ENTITY-MIB", "entConfigChange", model.StatusCurrent, "entLastChangeTime changed"),
				nt("SNMPv2-MIB", "authenticationFailure", model.StatusCurrent, "the SNMP entity has received a protocol message that is not properly authenticated"),
			},
			want: map[string]want{
				"entConfigChange":       {ClassOrphan, nil},
				"authenticationFailure": {ClassOrphan, nil},
			},
		},
		{
			// The adversarial case: current pair is name-less and must
			// pair via description+group+varbind; the deprecated
			// near-duplicates pair with EACH OTHER, never across status.
			name: "BGP4-MIB current + deprecated notifications",
			syms: []model.Symbol{
				nt("BGP4-MIB", "bgpBackwardTransNotification", model.StatusCurrent, "the BGP FSM moves from a higher numbered state to a lower numbered state"),
				nt("BGP4-MIB", "bgpEstablishedNotification", model.StatusCurrent, "the BGP FSM enters the established state"),
				nt("BGP4-MIB", "bgpBackwardTransition", model.StatusDeprecated, "the BGP FSM moves from a higher numbered state to a lower numbered state"),
				nt("BGP4-MIB", "bgpEstablished", model.StatusDeprecated, "the BGP FSM enters the established state"),
			},
			refs: []model.Reference{
				// current pair → bgp4MIBNotificationGroup
				grpMember("BGP4-MIB", "bgp4MIBNotificationGroup", "bgpBackwardTransNotification"),
				grpMember("BGP4-MIB", "bgp4MIBNotificationGroup", "bgpEstablishedNotification"),
				// deprecated pair → bgp4MIBTrapGroup
				grpMember("BGP4-MIB", "bgp4MIBTrapGroup", "bgpBackwardTransition"),
				grpMember("BGP4-MIB", "bgp4MIBTrapGroup", "bgpEstablished"),
				// all share peer-state varbinds
				obj("BGP4-MIB", "bgpBackwardTransNotification", "bgpPeerState"),
				obj("BGP4-MIB", "bgpEstablishedNotification", "bgpPeerState"),
				obj("BGP4-MIB", "bgpBackwardTransition", "bgpPeerState"),
				obj("BGP4-MIB", "bgpEstablished", "bgpPeerState"),
			},
			want: map[string]want{
				"bgpBackwardTransNotification": {ClassRaise, nil},
				"bgpEstablishedNotification":   {ClassClear, []string{"bgpBackwardTransNotification"}},
				"bgpBackwardTransition":        {ClassRaise, nil},
				"bgpEstablished":               {ClassClear, []string{"bgpBackwardTransition"}},
			},
		},
	}
}

// recallTargetPct is the minimum pairing recall at Likely-or-higher
// confidence required over the labeled golden set (NFR7).
const recallTargetPct = 80

// TestGoldenSet asserts every labeled case and measures precision AND
// recall on the labeled oracle: golden accuracy must be 100%, every
// High-confidence result must be correct (the High-band floor, NFR6),
// and pairing recall at Likely-or-higher must meet the NFR7 target.
// These are the only ground-truth labels we have; the corpus-wide run
// (mib-correlate-report) reports distribution, not precision/recall.
func TestGoldenSet(t *testing.T) {
	var total, correct, high, highCorrect int
	var expectedEdges, recalledEdges int
	for _, gc := range goldenSet() {
		got := byName(Classify(gc.syms, gc.refs))
		for name, w := range gc.want {
			total++
			r, ok := got[name]
			clears := append([]string(nil), r.Clears...)
			sort.Strings(clears)
			wantClears := append([]string(nil), w.clears...)
			sort.Strings(wantClears)
			ok = ok && r.Class == w.class && equalStrings(clears, wantClears)
			if ok {
				correct++
			} else {
				t.Errorf("[%s] %s = {class:%q clears:%v}, want {class:%q clears:%v}",
					gc.name, name, r.Class, r.Clears, w.class, w.clears)
			}
			if r.Confidence == ConfHigh {
				high++
				if ok {
					highCorrect++
				}
			}
			// Recall: each expected clear→raise edge that was found at
			// Likely-or-higher confidence counts as recalled.
			for _, raise := range w.clears {
				expectedEdges++
				if r.Confidence != ConfGuess && contains(r.Clears, raise) {
					recalledEdges++
				}
			}
		}
	}
	if correct != total {
		t.Fatalf("golden-set accuracy %d/%d, want 100%%", correct, total)
	}
	// High-confidence precision floor (NFR6: >= 95%). On the golden
	// oracle it is 100%.
	if high > 0 && highCorrect != high {
		t.Fatalf("high-confidence precision %d/%d, want 100%% on golden set", highCorrect, high)
	}
	// Pairing recall at Likely-or-higher (NFR7: >= 80%).
	if expectedEdges > 0 {
		recallPct := recalledEdges * 100 / expectedEdges
		if recallPct < recallTargetPct {
			t.Fatalf("pairing recall %d%% (%d/%d edges), want >= %d%%", recallPct, recalledEdges, expectedEdges, recallTargetPct)
		}
		t.Logf("golden: accuracy %d/%d; high precision %d/%d; recall %d/%d edges (%d%%)",
			correct, total, highCorrect, high, recalledEdges, expectedEdges, recallPct)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCoverageTally checks the Coverage accounting reconciles.
func TestCoverageTally(t *testing.T) {
	cov := NewCoverage()
	for _, gc := range goldenSet() {
		cov.AddModule(Classify(gc.syms, gc.refs))
	}
	sum := 0
	for _, k := range classOrder {
		sum += cov.ByClass[k]
	}
	if sum != cov.Notifications {
		t.Errorf("class counts %d do not reconcile with %d notifications", sum, cov.Notifications)
	}
	confSum := 0
	for _, k := range confidenceOrder {
		confSum += cov.ByConfidence[k]
	}
	if confSum != cov.Notifications {
		t.Errorf("confidence counts %d do not reconcile with %d notifications", confSum, cov.Notifications)
	}
}
