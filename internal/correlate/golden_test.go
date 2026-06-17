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
		{
			// Story 3.1 AC1+AC2: the directional token is glued to an
			// all-caps acronym run; the tokenizer fix surfaces it. Names
			// share a root → name+varbind → High.
			name: "Cisco ATM OAM Failure/Recover (acronym-glued)",
			syms: []model.Symbol{
				nt("CISCO-ATM-PVCTRAP-EXTN-MIB", "catmIntfPvcOAMFailureTrap", model.StatusCurrent, "one or more PVCLs on this interface has OAM loop back failed"),
				nt("CISCO-ATM-PVCTRAP-EXTN-MIB", "catmIntfPvcOAMRecoverTrap", model.StatusCurrent, "one or more PVCLs on this interface has OAM loop back recovered"),
			},
			refs: []model.Reference{
				obj("CISCO-ATM-PVCTRAP-EXTN-MIB", "catmIntfPvcOAMFailureTrap", "catmIntfPvcFailures"),
				obj("CISCO-ATM-PVCTRAP-EXTN-MIB", "catmIntfPvcOAMRecoverTrap", "catmIntfPvcFailures"),
			},
			want: map[string]want{
				"catmIntfPvcOAMFailureTrap": {ClassRaise, nil},
				"catmIntfPvcOAMRecoverTrap": {ClassClear, []string{"catmIntfPvcOAMFailureTrap"}},
			},
		},
		{
			// Story 3.1 AC2: "raised"/"cleared" vocabulary (absent before).
			name: "Cisco Content Engine Raised/Cleared",
			syms: []model.Symbol{
				nt("CISCO-CONTENT-ENGINE-MIB", "cceAlarmCriticalRaised", model.StatusCurrent, "the Agent generates this trap when any module raises a Critical alarm"),
				nt("CISCO-CONTENT-ENGINE-MIB", "cceAlarmCriticalCleared", model.StatusCurrent, "the Agent generates this trap when any module clears a Critical alarm"),
			},
			refs: []model.Reference{
				obj("CISCO-CONTENT-ENGINE-MIB", "cceAlarmCriticalRaised", "cceAlarmType"),
				obj("CISCO-CONTENT-ENGINE-MIB", "cceAlarmCriticalCleared", "cceAlarmType"),
			},
			want: map[string]want{
				"cceAlarmCriticalRaised":  {ClassRaise, nil},
				"cceAlarmCriticalCleared": {ClassClear, []string{"cceAlarmCriticalRaised"}},
			},
		},
		{
			// Story 3.1 AC2: assert/deassert vocabulary, shared name-root.
			name: "Huawei Assert/Deassert (name-root)",
			syms: []model.Symbol{
				nt("HUAWEI-SERVER-IBMC-MIB", "hwBoardMismatchAssert", model.StatusCurrent, "the board is mismatched. (Generated)"),
				nt("HUAWEI-SERVER-IBMC-MIB", "hwBoardMismatchDeassert", model.StatusCurrent, "the board is mismatched. (Cleared)"),
			},
			refs: []model.Reference{
				obj("HUAWEI-SERVER-IBMC-MIB", "hwBoardMismatchAssert", "hwTrapObject"),
				obj("HUAWEI-SERVER-IBMC-MIB", "hwBoardMismatchDeassert", "hwTrapObject"),
			},
			want: map[string]want{
				"hwBoardMismatchAssert":   {ClassRaise, nil},
				"hwBoardMismatchDeassert": {ClassClear, []string{"hwBoardMismatchAssert"}},
			},
		},
		{
			// Story 3.1 AC3: roots differ (the clear keeps "fault" in its
			// root), so this pairs on the shared varbind signature alone —
			// and therefore caps at Likely (AC5), still correct class.
			name: "Huawei Fault/Deassert (varbind-signature)",
			syms: []model.Symbol{
				nt("HUAWEI-SERVER-IBMC-MIB", "hwBMCHeartBeatFault", model.StatusCurrent, "iBMC heartbeat abnormal. (Generated)"),
				nt("HUAWEI-SERVER-IBMC-MIB", "hwBMCHeartBeatFaultDeassert", model.StatusCurrent, "iBMC heartbeat abnormal. (Cleared)"),
			},
			refs: []model.Reference{
				obj("HUAWEI-SERVER-IBMC-MIB", "hwBMCHeartBeatFault", "hwHeartBeatId"),
				obj("HUAWEI-SERVER-IBMC-MIB", "hwBMCHeartBeatFaultDeassert", "hwHeartBeatId"),
			},
			want: map[string]want{
				"hwBMCHeartBeatFault":         {ClassRaise, nil},
				"hwBMCHeartBeatFaultDeassert": {ClassClear, []string{"hwBMCHeartBeatFault"}},
			},
		},
		{
			// Story 3.1 AC2: "fault" raise token, shared name-root.
			name: "Polycom AlarmFault/AlarmClear",
			syms: []model.Symbol{
				nt("POLYCOM-RMX-MIB", "rmxBadEthernetSettingsAlarmFault", model.StatusCurrent, "bad ethernet settings"),
				nt("POLYCOM-RMX-MIB", "rmxBadEthernetSettingsAlarmClear", model.StatusCurrent, "bad ethernet settings"),
			},
			refs: []model.Reference{
				obj("POLYCOM-RMX-MIB", "rmxBadEthernetSettingsAlarmFault", "rmxActiveAlarmIndex"),
				obj("POLYCOM-RMX-MIB", "rmxBadEthernetSettingsAlarmClear", "rmxActiveAlarmIndex"),
			},
			want: map[string]want{
				"rmxBadEthernetSettingsAlarmFault": {ClassRaise, nil},
				"rmxBadEthernetSettingsAlarmClear": {ClassClear, []string{"rmxBadEthernetSettingsAlarmFault"}},
			},
		},
		{
			// Story 3.1 AC3 negative: a varbind signature shared by THREE
			// notifications is NOT a grouping signal — no over-pairing.
			name: "three-way shared varbind signature stays orphan",
			syms: []model.Symbol{
				nt("V3-MIB", "alphaFailEvent", model.StatusCurrent, "the alpha subsystem has failed"),
				nt("V3-MIB", "betaOkEvent", model.StatusCurrent, "the beta subsystem is restored"),
				nt("V3-MIB", "gammaFailEvent", model.StatusCurrent, "the gamma subsystem has failed"),
			},
			refs: []model.Reference{
				obj("V3-MIB", "alphaFailEvent", "vCommonIndex"),
				obj("V3-MIB", "betaOkEvent", "vCommonIndex"),
				obj("V3-MIB", "gammaFailEvent", "vCommonIndex"),
			},
			want: map[string]want{
				"alphaFailEvent": {ClassOrphan, nil},
				"betaOkEvent":    {ClassOrphan, nil},
				"gammaFailEvent": {ClassOrphan, nil},
			},
		},
		{
			// Genuine orphans (precision realism): a threshold trap with a
			// raise direction but no clear counterpart, and an
			// informational trap — both must stay orphan.
			name: "genuine orphans (threshold, informational)",
			syms: []model.Symbol{
				nt("ADSL-LINE-MIB", "adslAtucPerfESsThreshTrap", model.StatusCurrent, "errored second 15-minute interval threshold reached"),
				nt("ADSL-LINE-MIB", "adslAtucRateChangeTrap", model.StatusCurrent, "the ATUCs transmit rate has changed"),
			},
			refs: []model.Reference{
				obj("ADSL-LINE-MIB", "adslAtucPerfESsThreshTrap", "adslAtucPerfESs"),
			},
			want: map[string]want{
				"adslAtucPerfESsThreshTrap": {ClassOrphan, nil},
				"adslAtucRateChangeTrap":    {ClassOrphan, nil},
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
