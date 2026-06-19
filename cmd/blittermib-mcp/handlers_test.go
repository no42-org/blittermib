/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/store"
)

// newTestHandlers opens a fresh, empty (schema-only) store in a temp dir.
// An empty corpus is enough to lock the not-found / empty-result contracts;
// positive-data coverage lives in the fixture-backed tests (task 5.1).
func newTestHandlers(t *testing.T) *handlers {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &handlers{st: st}
}

// A missing symbol must be reported as data (Found=false), never as a
// tool/transport error — store.GetSymbol returns store.ErrNotFound, not nil.
func TestLookupSymbolNotFoundIsData(t *testing.T) {
	h := newTestHandlers(t)
	_, out, err := h.lookupSymbol(context.Background(), nil, lookupSymbolIn{Module: "NOPE", Name: "nope"})
	if err != nil {
		t.Fatalf("missing symbol should not error, got %v", err)
	}
	if out.Found || out.Symbol != nil {
		t.Errorf("expected not-found result, got %+v", out)
	}
}

// An unresolved OID must fall through to the (empty) nearest-prefix search,
// not short-circuit on ErrNotFound from GetSymbolByOID.
func TestLookupOIDUnresolvedIsData(t *testing.T) {
	h := newTestHandlers(t)
	_, out, err := h.lookupOID(context.Background(), nil, lookupOIDIn{OID: "1.3.6.1.4.1.99999.1"})
	if err != nil {
		t.Fatalf("unresolved OID should not error, got %v", err)
	}
	if out.Exact != nil {
		t.Errorf("expected no exact match on empty corpus, got %+v", out.Exact)
	}
}

// A malformed OID is no-match data, not a transport error.
func TestLookupOIDMalformedIsData(t *testing.T) {
	h := newTestHandlers(t)
	if _, _, err := h.lookupOID(context.Background(), nil, lookupOIDIn{OID: "not-an-oid"}); err != nil {
		t.Fatalf("malformed OID should be no-match data, got error %v", err)
	}
}

func TestClassifyNotificationNotFoundIsData(t *testing.T) {
	h := newTestHandlers(t)
	_, out, err := h.classifyNotification(context.Background(), nil, classifyIn{Module: "NOPE", Name: "nope"})
	if err != nil {
		t.Fatalf("missing relationship should not error, got %v", err)
	}
	if out.Found || out.Relationship != nil {
		t.Errorf("expected not-found result, got %+v", out)
	}
}

// An empty search must marshal to "hits":[] (non-nil slice), never null.
func TestSearchEmptyReturnsNonNilSlice(t *testing.T) {
	h := newTestHandlers(t)
	_, out, err := h.searchMIBs(context.Background(), nil, searchIn{Query: "nothingmatcheshere"})
	if err != nil {
		t.Fatalf("search should not error on no match, got %v", err)
	}
	if out.Hits == nil {
		t.Error("Hits must be a non-nil empty slice (marshals to []), got nil (marshals to null)")
	}
}

func TestDecodeWalkOversizedRejected(t *testing.T) {
	h := newTestHandlers(t)
	big := strings.Repeat("x", walkMaxBytes+1)
	if _, _, err := h.decodeWalk(context.Background(), nil, decodeWalkIn{Text: big}); err == nil {
		t.Error("oversized walk text should be rejected")
	}
}
