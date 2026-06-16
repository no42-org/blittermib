/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package correlate

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// sampleNotifs is a minimal IF-MIB-style fixture: the canonical
// linkDown/linkUp pair sharing ifIndex. The scaffold classifies
// nothing yet; later stories assert the raise/clear verdict here.
func sampleNotifs() ([]model.Symbol, []model.Reference) {
	syms := []model.Symbol{
		{ModuleName: "IF-MIB", Name: "linkDown", Kind: model.KindNotificationType, Status: model.StatusCurrent, OID: "1.3.6.1.6.3.1.1.5.3"},
		{ModuleName: "IF-MIB", Name: "linkUp", Kind: model.KindNotificationType, Status: model.StatusCurrent, OID: "1.3.6.1.6.3.1.1.5.4"},
		{ModuleName: "IF-MIB", Name: "ifIndex", Kind: model.KindColumn, OID: "1.3.6.1.2.1.2.2.1.1"},
	}
	refs := []model.Reference{
		{SourceModule: "IF-MIB", SourceName: "linkDown", TargetModule: "IF-MIB", TargetName: "ifIndex", Kind: model.RefNotificationObject, Position: 0},
		{SourceModule: "IF-MIB", SourceName: "linkUp", TargetModule: "IF-MIB", TargetName: "ifIndex", Kind: model.RefNotificationObject, Position: 0},
	}
	return syms, refs
}

// TestClassifyDeterministic pins the core contract: identical input
// yields identical output across runs (no map-order / time / rand
// leakage). This guard stays meaningful as signals are added.
func TestClassifyDeterministic(t *testing.T) {
	syms, refs := sampleNotifs()
	a := Classify(syms, refs)
	b := Classify(syms, refs)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Classify is non-deterministic:\n a=%#v\n b=%#v", a, b)
	}
}

// byName indexes relationships for assertion convenience.
func byName(rels []Relationship) map[string]Relationship {
	m := make(map[string]Relationship, len(rels))
	for _, r := range rels {
		m[r.Notification] = r
	}
	return m
}

// TestClassifyLinkUpDown is the canonical golden case: linkDown is a
// raise, linkUp is its clear (shared root + opposing tokens, confirmed
// by the shared ifIndex varbind → High confidence), with a clear→raise
// edge and evidence recording both signals.
func TestClassifyLinkUpDown(t *testing.T) {
	syms, refs := sampleNotifs()
	m := byName(Classify(syms, refs))

	down, ok := m["linkDown"]
	if !ok || down.Class != ClassRaise {
		t.Fatalf("linkDown = %+v, want classified raise", down)
	}
	up, ok := m["linkUp"]
	if !ok || up.Class != ClassClear {
		t.Fatalf("linkUp = %+v, want classified clear", up)
	}
	if len(up.Clears) != 1 || up.Clears[0] != "linkDown" {
		t.Errorf("linkUp.Clears = %v, want [linkDown]", up.Clears)
	}
	if up.Confidence != ConfHigh || down.Confidence != ConfHigh {
		t.Errorf("confidence = %s/%s, want high/high (shared varbind)", down.Confidence, up.Confidence)
	}
	// Evidence records the name signal and the varbind signal.
	kinds := map[SignalKind]bool{}
	for _, s := range up.Evidence.Signals {
		kinds[s.Kind] = true
	}
	if !kinds[SignalName] || !kinds[SignalVarbind] {
		t.Errorf("linkUp evidence missing a signal: %+v", up.Evidence.Signals)
	}
}

// TestClassifyTrapTypeSMIv1 confirms FR6: SMIv1 TRAP-TYPE pairs classify
// identically to SMIv2 NOTIFICATION-TYPE.
func TestClassifyTrapTypeSMIv1(t *testing.T) {
	syms := []model.Symbol{
		{ModuleName: "V1-MIB", Name: "tunnelFailed", Kind: model.KindTrapType, Status: model.StatusMandatory},
		{ModuleName: "V1-MIB", Name: "tunnelOk", Kind: model.KindTrapType, Status: model.StatusMandatory},
	}
	refs := []model.Reference{
		{SourceModule: "V1-MIB", SourceName: "tunnelFailed", TargetModule: "V1-MIB", TargetName: "tunnelId", Kind: model.RefNotificationObject},
		{SourceModule: "V1-MIB", SourceName: "tunnelOk", TargetModule: "V1-MIB", TargetName: "tunnelId", Kind: model.RefNotificationObject},
	}
	m := byName(Classify(syms, refs))
	if m["tunnelFailed"].Class != ClassRaise {
		t.Errorf("tunnelFailed = %q, want raise", m["tunnelFailed"].Class)
	}
	if got := m["tunnelOk"]; got.Class != ClassClear || len(got.Clears) != 1 || got.Clears[0] != "tunnelFailed" {
		t.Errorf("tunnelOk = %+v, want clear of tunnelFailed", got)
	}
}

// TestClassifyNoPanicOnEmpty confirms the never-panic contract on
// degenerate input.
func TestClassifyNoPanicOnEmpty(t *testing.T) {
	if got := Classify(nil, nil); got != nil {
		t.Fatalf("Classify(nil, nil) = %#v, want nil", got)
	}
}

// TestEvidenceString covers the shared renderer behind the UI popover
// and the export provenance comment (D4).
func TestEvidenceString(t *testing.T) {
	e := Evidence{
		Signals: []SignalHit{{Kind: SignalName, Detail: "matching root"}, {Kind: SignalVarbind, Detail: "shared ifIndex"}},
		Summary: "clears linkDown",
	}
	if got, want := e.String(), "clears linkDown (matching root; shared ifIndex)"; got != want {
		t.Errorf("Evidence.String() = %q, want %q", got, want)
	}
	if got := (Evidence{Summary: "no resolution found"}).String(); got != "no resolution found" {
		t.Errorf("empty-signals String() = %q", got)
	}
}

// TestClassifyOrphans confirms the orphan cases: a standalone
// informational notification (entConfigChange — no varbinds, no
// directional token) and a problem with no clear (authenticationFailure
// — raise-leaning but unpaired) both classify as orphan (alarm-type 3).
func TestClassifyOrphans(t *testing.T) {
	syms := []model.Symbol{
		{ModuleName: "ENTITY-MIB", Name: "entConfigChange", Kind: model.KindNotificationType, Status: model.StatusCurrent},
		{ModuleName: "SNMPv2-MIB", Name: "authenticationFailure", Kind: model.KindNotificationType, Status: model.StatusCurrent},
	}
	m := byName(Classify(syms, nil))
	for _, name := range []string{"entConfigChange", "authenticationFailure"} {
		r, ok := m[name]
		if !ok || r.Class != ClassOrphan {
			t.Errorf("%s = %+v, want orphan", name, r)
		}
		if len(r.Clears) != 0 {
			t.Errorf("%s orphan should have no Clears, got %v", name, r.Clears)
		}
	}
}

// TestClassifyStatusGuard confirms FR5: a current notification is never
// paired with a deprecated near-duplicate. A current raise whose only
// candidate clear is deprecated stays unpaired — both fall through to
// orphan rather than forming a cross-status pair.
func TestClassifyStatusGuard(t *testing.T) {
	syms := []model.Symbol{
		{ModuleName: "X-MIB", Name: "sessionDown", Kind: model.KindNotificationType, Status: model.StatusCurrent},
		{ModuleName: "X-MIB", Name: "sessionUp", Kind: model.KindNotificationType, Status: model.StatusDeprecated},
	}
	refs := []model.Reference{
		{SourceModule: "X-MIB", SourceName: "sessionDown", TargetModule: "X-MIB", TargetName: "sessionId", Kind: model.RefNotificationObject},
		{SourceModule: "X-MIB", SourceName: "sessionUp", TargetModule: "X-MIB", TargetName: "sessionId", Kind: model.RefNotificationObject},
	}
	m := byName(Classify(syms, refs))
	if m["sessionDown"].Class != ClassOrphan {
		t.Errorf("sessionDown (current) = %q, want orphan — must not pair with deprecated sessionUp", m["sessionDown"].Class)
	}
	if m["sessionUp"].Class != ClassOrphan {
		t.Errorf("sessionUp (deprecated) = %q, want orphan", m["sessionUp"].Class)
	}
	// A clear-leaning orphan gets a clear-specific summary, not the
	// "standalone"/"no resolution" fallback.
	if got := m["sessionUp"].Evidence.Summary; got != "resolution with no matching problem notification" {
		t.Errorf("sessionUp orphan summary = %q, want clear-specific", got)
	}

	// Sanity: two deprecated members of the same root DO pair (both legacy).
	legacy := []model.Symbol{
		{ModuleName: "X-MIB", Name: "oldFail", Kind: model.KindNotificationType, Status: model.StatusDeprecated},
		{ModuleName: "X-MIB", Name: "oldOk", Kind: model.KindNotificationType, Status: model.StatusDeprecated},
	}
	lm := byName(Classify(legacy, nil))
	if lm["oldFail"].Class != ClassRaise || lm["oldOk"].Class != ClassClear {
		t.Errorf("both-deprecated pair should still pair: oldFail=%q oldOk=%q", lm["oldFail"].Class, lm["oldOk"].Class)
	}
}

// TestClassifyBGPNameless is the adversarial golden case: the BGP4-MIB
// current pair carries NO directional name token, so it can only be
// paired via the description-prose direction ("established" vs "lower
// numbered state"), shared NOTIFICATION-GROUP, and shared varbinds.
// Proves multi-signal necessity (Story 1.4).
func TestClassifyBGPNameless(t *testing.T) {
	syms := []model.Symbol{
		{ModuleName: "BGP4-MIB", Name: "bgpBackwardTransNotification", Kind: model.KindNotificationType, Status: model.StatusCurrent,
			Description: "generated when the BGP FSM moves from a higher numbered state to a lower numbered state."},
		{ModuleName: "BGP4-MIB", Name: "bgpEstablishedNotification", Kind: model.KindNotificationType, Status: model.StatusCurrent,
			Description: "generated when the BGP FSM enters the established state."},
	}
	notif := func(src, tgt string) model.Reference {
		return model.Reference{SourceModule: "BGP4-MIB", SourceName: src, TargetModule: "BGP4-MIB", TargetName: tgt, Kind: model.RefNotificationObject}
	}
	member := func(notifName string) model.Reference {
		return model.Reference{SourceModule: "BGP4-MIB", SourceName: "bgp4MIBNotificationGroup", TargetModule: "BGP4-MIB", TargetName: notifName, Kind: model.RefGroupMember}
	}
	refs := []model.Reference{
		notif("bgpBackwardTransNotification", "bgpPeerState"),
		notif("bgpBackwardTransNotification", "bgpPeerLastError"),
		notif("bgpEstablishedNotification", "bgpPeerState"),
		notif("bgpEstablishedNotification", "bgpPeerLastError"),
		member("bgpBackwardTransNotification"),
		member("bgpEstablishedNotification"),
	}
	m := byName(Classify(syms, refs))

	bt := m["bgpBackwardTransNotification"]
	est := m["bgpEstablishedNotification"]
	if bt.Class != ClassRaise {
		t.Fatalf("bgpBackwardTransNotification = %q, want raise", bt.Class)
	}
	if est.Class != ClassClear || len(est.Clears) != 1 || est.Clears[0] != "bgpBackwardTransNotification" {
		t.Fatalf("bgpEstablishedNotification = %+v, want clear of bgpBackwardTransNotification", est)
	}
	if est.Confidence != ConfHigh {
		t.Errorf("confidence = %s, want high (description+group+varbind agree)", est.Confidence)
	}
	// Crucially, the NAME signal must NOT have fired — this pair is
	// found without it.
	for _, s := range est.Evidence.Signals {
		if s.Kind == SignalName {
			t.Errorf("name signal should not fire for the BGP pair: %+v", est.Evidence.Signals)
		}
	}
	// The description and group signals must be present.
	kinds := map[SignalKind]bool{}
	for _, s := range est.Evidence.Signals {
		kinds[s.Kind] = true
	}
	if !kinds[SignalDescription] || !kinds[SignalGroup] {
		t.Errorf("BGP pair evidence missing description/group signal: %+v", est.Evidence.Signals)
	}
}

// TestClassifyLargeGroupNoOverpairing guards the code-review finding:
// a NOTIFICATION-GROUP that bundles many notifications must NOT act as a
// pairing signal (else every raise-ish trap would pair with every
// clear-ish trap that co-resides in the group and shares a module-wide
// index varbind, producing spurious High-confidence pairs). Here four
// nameless, description-directed notifications share one big group and a
// common varbind; with no name-root and an over-size group, none should
// pair — all fall through to orphan.
func TestClassifyLargeGroupNoOverpairing(t *testing.T) {
	mk := func(name, desc string) model.Symbol {
		return model.Symbol{ModuleName: "BIG-MIB", Name: name, Kind: model.KindNotificationType, Status: model.StatusCurrent, Description: desc}
	}
	syms := []model.Symbol{
		mk("alphaProblem", "the subsystem has failed"),
		mk("alphaRecovery", "the subsystem is restored"),
		mk("betaProblem", "interface is down state detected"),
		mk("betaRecovery", "interface comes up"),
	}
	var refs []model.Reference
	for _, n := range []string{"alphaProblem", "alphaRecovery", "betaProblem", "betaRecovery"} {
		// All four in one group, all sharing a common varbind.
		refs = append(refs,
			model.Reference{SourceModule: "BIG-MIB", SourceName: "bigGroup", TargetModule: "BIG-MIB", TargetName: n, Kind: model.RefGroupMember},
			model.Reference{SourceModule: "BIG-MIB", SourceName: n, TargetModule: "BIG-MIB", TargetName: "commonIndex", Kind: model.RefNotificationObject},
		)
	}
	for _, r := range Classify(syms, refs) {
		if r.Class != ClassOrphan {
			t.Errorf("%s = %q, want orphan — a 4-member group must not pair notifications", r.Notification, r.Class)
		}
	}
}

// TestScoreConfidence pins the scoring policy directly.
func TestScoreConfidence(t *testing.T) {
	cases := []struct {
		name   string
		sig    signalSet
		fanOut int
		confl  bool
		want   Confidence
	}{
		{"name+varbind 1:1", signalSet{name: true, varbind: true}, 1, false, ConfHigh},
		{"three signals", signalSet{varbind: true, desc: true, group: true}, 1, false, ConfHigh},
		{"name+varbind but conflict", signalSet{name: true, varbind: true}, 1, true, ConfLikely},
		{"name+varbind but fan-out", signalSet{name: true, varbind: true}, 2, false, ConfLikely},
		{"two signals", signalSet{desc: true, group: true}, 1, false, ConfLikely},
		{"name only (weak, no corroboration)", signalSet{name: true}, 1, false, ConfGuess},
		{"group only", signalSet{group: true}, 1, false, ConfGuess},
	}
	for _, c := range cases {
		if got := scoreConfidence(c.sig, c.fanOut, c.confl); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// TestConfidenceConflictCap: a notification whose name says raise but
// whose description says clear is contradictory — capped to Likely even
// when name+varbind would otherwise be High. Its partner, with no
// conflict, stays High.
func TestConfidenceConflictCap(t *testing.T) {
	syms := []model.Symbol{
		{ModuleName: "C-MIB", Name: "fooDown", Kind: model.KindNotificationType, Status: model.StatusCurrent, Description: "the condition has been restored"},
		{ModuleName: "C-MIB", Name: "fooUp", Kind: model.KindNotificationType, Status: model.StatusCurrent, Description: "the condition comes up"},
	}
	refs := []model.Reference{
		{SourceModule: "C-MIB", SourceName: "fooDown", TargetModule: "C-MIB", TargetName: "fooId", Kind: model.RefNotificationObject},
		{SourceModule: "C-MIB", SourceName: "fooUp", TargetModule: "C-MIB", TargetName: "fooId", Kind: model.RefNotificationObject},
	}
	m := byName(Classify(syms, refs))
	if m["fooDown"].Confidence != ConfLikely {
		t.Errorf("fooDown confidence = %s, want likely (name says down, description says restored)", m["fooDown"].Confidence)
	}
	if m["fooUp"].Confidence != ConfHigh {
		t.Errorf("fooUp confidence = %s, want high (no conflict, 1:1)", m["fooUp"].Confidence)
	}
}

// TestConfidenceFanoutCap: a clear that resolves more than one raise is
// less certain than a clean 1:1 pairing — capped to Likely. The 1:1
// raises stay High.
func TestConfidenceFanoutCap(t *testing.T) {
	syms := []model.Symbol{
		{ModuleName: "S-MIB", Name: "svcDown", Kind: model.KindNotificationType, Status: model.StatusCurrent},
		{ModuleName: "S-MIB", Name: "svcFail", Kind: model.KindNotificationType, Status: model.StatusCurrent},
		{ModuleName: "S-MIB", Name: "svcUp", Kind: model.KindNotificationType, Status: model.StatusCurrent},
	}
	var refs []model.Reference
	for _, n := range []string{"svcDown", "svcFail", "svcUp"} {
		refs = append(refs, model.Reference{SourceModule: "S-MIB", SourceName: n, TargetModule: "S-MIB", TargetName: "svcId", Kind: model.RefNotificationObject})
	}
	m := byName(Classify(syms, refs))
	if got := m["svcUp"]; got.Confidence != ConfLikely || len(got.Clears) != 2 {
		t.Errorf("svcUp = %+v, want likely with 2 clears (fan-out cap)", got)
	}
	if m["svcDown"].Confidence != ConfHigh {
		t.Errorf("svcDown confidence = %s, want high (1:1)", m["svcDown"].Confidence)
	}
}

// TestEvidenceJSONShape locks the serialized contract shared by the UI
// popover and the export provenance comment: lowercase keys
// signals/kind/detail/summary.
func TestEvidenceJSONShape(t *testing.T) {
	ev := Evidence{
		Signals: []SignalHit{{Kind: SignalVarbind, Detail: "shared ifIndex"}},
		Summary: "inferred clear",
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	const want = `{"signals":[{"kind":"varbind","detail":"shared ifIndex"}],"summary":"inferred clear"}`
	if string(b) != want {
		t.Fatalf("evidence JSON shape drifted:\n got %s\nwant %s", b, want)
	}
}
