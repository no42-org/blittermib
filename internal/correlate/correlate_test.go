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

// TestClassifyScaffoldEmpty documents the Phase-0 scaffold state:
// Classify returns no relationships until the signals land. Updated
// in Story 1.2 when linkDown/linkUp must classify.
func TestClassifyScaffoldEmpty(t *testing.T) {
	syms, refs := sampleNotifs()
	if got := Classify(syms, refs); len(got) != 0 {
		t.Fatalf("scaffold Classify should return no relationships, got %d", len(got))
	}
}

// TestClassifyNoPanicOnEmpty confirms the never-panic contract on
// degenerate input.
func TestClassifyNoPanicOnEmpty(t *testing.T) {
	if got := Classify(nil, nil); got != nil {
		t.Fatalf("Classify(nil, nil) = %#v, want nil", got)
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
