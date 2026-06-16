/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/no42-org/blittermib/internal/correlate"
	"github.com/no42-org/blittermib/internal/model"
)

// TestBackfillRelationships simulates a cache that predates the feature:
// symbols/refs are present but the relationship tables are empty and
// user_version is 0. The backfill must classify the stored corpus
// without re-ingesting.
func TestBackfillRelationships(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	// Seed IF-MIB via ReplaceModule (this also classifies)...
	syms := []model.Symbol{
		{ModuleName: "IF-MIB", Name: "linkDown", OID: "1.3.6.1.6.3.1.1.5.3", Kind: model.KindNotificationType, Status: model.StatusCurrent, Description: "about to enter the down state"},
		{ModuleName: "IF-MIB", Name: "linkUp", OID: "1.3.6.1.6.3.1.1.5.4", Kind: model.KindNotificationType, Status: model.StatusCurrent, Description: "left the down state"},
		{ModuleName: "IF-MIB", Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1", Kind: model.KindColumn},
	}
	refs := []model.Reference{
		{SourceModule: "IF-MIB", SourceName: "linkDown", TargetModule: "IF-MIB", TargetName: "ifIndex", Kind: model.RefNotificationObject},
		{SourceModule: "IF-MIB", SourceName: "linkUp", TargetModule: "IF-MIB", TargetName: "ifIndex", Kind: model.RefNotificationObject},
	}
	if err := s.ReplaceModule(ctx, &model.Module{Name: "IF-MIB", OIDRoot: "1.3.6.1", ParseStatus: model.ParseStatusClean}, syms, refs, nil); err != nil {
		t.Fatal(err)
	}

	// ...then wipe the derived tables and reset the version, mimicking a
	// pre-feature cache (symbols present, relationships absent).
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notification_relationship`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notification_pair`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
	if rels, _ := s.ListRelationships(ctx, "IF-MIB"); len(rels) != 0 {
		t.Fatalf("precondition: expected empty relationships, got %d", len(rels))
	}

	// Backfill should reclassify from stored symbols/refs.
	if err := s.backfillRelationships(ctx); err != nil {
		t.Fatalf("backfillRelationships: %v", err)
	}
	rels, err := s.ListRelationships(ctx, "IF-MIB")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(rels))
	for _, r := range rels {
		got[r.Notification] = string(r.Class)
	}
	if got["linkDown"] != "raise" || got["linkUp"] != "clear" {
		t.Errorf("after backfill: linkDown=%q linkUp=%q, want raise/clear", got["linkDown"], got["linkUp"])
	}

	// Idempotent: a second call is a no-op (version gate set).
	if err := s.backfillRelationships(ctx); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	rels2, _ := s.ListRelationships(ctx, "IF-MIB")
	if len(rels2) != len(rels) {
		t.Errorf("second backfill changed counts: %d → %d", len(rels), len(rels2))
	}
}

// TestBackfillVersionBumpReclassifies guards the upgrade path: a DB
// backfilled by an OLDER engine carries a prior generation's (possibly
// wrong) classifications at user_version = N. Bumping
// relationshipsBackfillVersion past N must re-run and overwrite the stale
// generation, not skip it. Regression test for the symptom where the list
// pane and detail pane showed different classification vintages.
func TestBackfillVersionBumpReclassifies(t *testing.T) {
	if relationshipsBackfillVersion < 2 {
		t.Skip("no version bump to exercise")
	}
	ctx := context.Background()
	s := newStore(t)

	syms := []model.Symbol{
		{ModuleName: "IF-MIB", Name: "linkDown", OID: "1.3.6.1.6.3.1.1.5.3", Kind: model.KindNotificationType, Status: model.StatusCurrent, Description: "about to enter the down state"},
		{ModuleName: "IF-MIB", Name: "linkUp", OID: "1.3.6.1.6.3.1.1.5.4", Kind: model.KindNotificationType, Status: model.StatusCurrent, Description: "left the down state"},
		{ModuleName: "IF-MIB", Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1", Kind: model.KindColumn},
	}
	refs := []model.Reference{
		{SourceModule: "IF-MIB", SourceName: "linkDown", TargetModule: "IF-MIB", TargetName: "ifIndex", Kind: model.RefNotificationObject},
		{SourceModule: "IF-MIB", SourceName: "linkUp", TargetModule: "IF-MIB", TargetName: "ifIndex", Kind: model.RefNotificationObject},
	}
	if err := s.ReplaceModule(ctx, &model.Module{Name: "IF-MIB", OIDRoot: "1.3.6.1", ParseStatus: model.ParseStatusClean}, syms, refs, nil); err != nil {
		t.Fatal(err)
	}

	// Simulate a stale prior generation: an older engine mislabeled
	// linkUp as orphan and pinned user_version to the previous value.
	if _, err := s.db.ExecContext(ctx, `UPDATE notification_relationship SET classification='orphan' WHERE notification_name='linkUp'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notification_pair`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", relationshipsBackfillVersion-1)); err != nil {
		t.Fatal(err)
	}

	// The bumped backfill must re-run and correct the stale classification.
	if err := s.backfillRelationships(ctx); err != nil {
		t.Fatalf("backfillRelationships: %v", err)
	}
	rel, err := s.GetRelationship(ctx, "IF-MIB", "linkUp")
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || rel.Class != correlate.ClassClear {
		t.Fatalf("after version bump: linkUp = %v, want clear", rel)
	}
}
