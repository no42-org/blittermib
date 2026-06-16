/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"context"
	"testing"
)

// seedModule creates the IF-MIB module row so the relationship tables'
// ON DELETE CASCADE FK to module(name) is satisfiable.
func seedModule(t *testing.T, s *Store) {
	t.Helper()
	if err := s.ReplaceModule(context.Background(), sampleModule(), sampleSymbols(), sampleRefs(), nil); err != nil {
		t.Fatalf("seed ReplaceModule: %v", err)
	}
}

// TestListRelationshipsRoundTrip exercises the Phase-0 persistence path:
// rows written into the derived tables read back through
// ListRelationships with evidence decoded and clear→raise edges joined.
// (Classify is an empty scaffold, so rows are inserted directly here.)
func TestListRelationshipsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedModule(t, s)

	for _, q := range []struct {
		name, class, conf, ev string
	}{
		{"linkDown", "raise", "high", `{"signals":[{"kind":"name","detail":"root link, down"}],"summary":"raise"}`},
		{"linkUp", "clear", "high", `{"signals":[{"kind":"varbind","detail":"shared ifIndex"}],"summary":"clears linkDown"}`},
		{"entConfigChange", "orphan", "high", `{"signals":[],"summary":"no resolution"}`},
	} {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO notification_relationship
			    (module_name, notification_name, classification, confidence, evidence_json)
			VALUES ('IF-MIB', ?, ?, ?, ?)`, q.name, q.class, q.conf, q.ev); err != nil {
			t.Fatalf("insert relationship %s: %v", q.name, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_pair (module_name, clear_name, raise_name)
		VALUES ('IF-MIB', 'linkUp', 'linkDown')`); err != nil {
		t.Fatalf("insert pair: %v", err)
	}

	rels, err := s.ListRelationships(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("ListRelationships: %v", err)
	}
	// Ordered by notification_name: entConfigChange, linkDown, linkUp.
	if len(rels) != 3 {
		t.Fatalf("got %d relationships, want 3", len(rels))
	}
	if rels[0].Notification != "entConfigChange" || string(rels[0].Class) != "orphan" {
		t.Errorf("rels[0] = %+v, want entConfigChange/orphan (stable name order)", rels[0])
	}
	linkUp := rels[2]
	if linkUp.Notification != "linkUp" || string(linkUp.Class) != "clear" {
		t.Fatalf("rels[2] = %+v, want linkUp/clear", linkUp)
	}
	if len(linkUp.Clears) != 1 || linkUp.Clears[0] != "linkDown" {
		t.Errorf("linkUp.Clears = %v, want [linkDown]", linkUp.Clears)
	}
	if linkUp.Evidence.Summary != "clears linkDown" ||
		len(linkUp.Evidence.Signals) != 1 || string(linkUp.Evidence.Signals[0].Kind) != "varbind" {
		t.Errorf("linkUp.Evidence decoded wrong: %+v", linkUp.Evidence)
	}
	// A raise carries no clear edges.
	if len(rels[1].Clears) != 0 {
		t.Errorf("linkDown (raise) should have no Clears, got %v", rels[1].Clears)
	}
}

// TestRelationshipClassificationCheck pins the CHECK constraint: only
// the three vocabulary values are storable, so a typo can never be
// persisted.
func TestRelationshipClassificationCheck(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedModule(t, s)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_relationship
		    (module_name, notification_name, classification, confidence)
		VALUES ('IF-MIB', 'bogus', 'Raise', 'high')`); err == nil {
		t.Fatal("expected CHECK violation for classification 'Raise', got nil")
	}
}

// TestRelationshipsCascadeWithModule confirms the derived rows are
// cleared when their module is removed — the mechanism ReplaceModule
// relies on (DELETE FROM module cascades) to rebuild on every ingest.
func TestRelationshipsCascadeWithModule(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedModule(t, s)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_relationship
		    (module_name, notification_name, classification, confidence)
		VALUES ('IF-MIB', 'linkDown', 'raise', 'high')`); err != nil {
		t.Fatalf("insert relationship: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM module WHERE name = 'IF-MIB'`); err != nil {
		t.Fatalf("delete module: %v", err)
	}
	rels, err := s.ListRelationships(ctx, "IF-MIB")
	if err != nil {
		t.Fatalf("ListRelationships after cascade: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("relationships should cascade-delete with module, got %d", len(rels))
	}
}
