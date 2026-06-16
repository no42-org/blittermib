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
