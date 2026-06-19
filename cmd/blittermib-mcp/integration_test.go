/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// seededHandlers returns handlers backed by an in-memory store holding a
// small IF-MIB slice: the interface table columns (for OID/symbol/walk
// resolution) and a linkDown/linkUp notification pair sharing the ifIndex
// varbind (so the classifier emits a relationship).
func seededHandlers(t *testing.T) *handlers {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mod := &model.Module{Name: "IF-MIB", OIDRoot: "1.3.6.1.2.1.2", ParseStatus: model.ParseStatusClean}
	syms := []model.Symbol{
		{ModuleName: "IF-MIB", Name: "ifTable", OID: "1.3.6.1.2.1.2.2", ParentOID: "1.3.6.1.2.1.2", Kind: model.KindTable},
		{ModuleName: "IF-MIB", Name: "ifEntry", OID: "1.3.6.1.2.1.2.2.1", ParentOID: "1.3.6.1.2.1.2.2", Kind: model.KindTableEntry, IndexColumns: []string{"ifIndex"}},
		{ModuleName: "IF-MIB", Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1", ParentOID: "1.3.6.1.2.1.2.2.1", Kind: model.KindColumn, Syntax: "Integer32", Access: model.AccessReadOnly, Description: "A unique value for each interface."},
		{ModuleName: "IF-MIB", Name: "ifInOctets", OID: "1.3.6.1.2.1.2.2.1.10", ParentOID: "1.3.6.1.2.1.2.2.1", Kind: model.KindColumn, Syntax: "Counter32", Access: model.AccessReadOnly},
		{ModuleName: "IF-MIB", Name: "linkDown", OID: "1.3.6.1.6.3.1.1.5.3", Kind: model.KindNotificationType, Description: "A linkDown trap."},
		{ModuleName: "IF-MIB", Name: "linkUp", OID: "1.3.6.1.6.3.1.1.5.4", Kind: model.KindNotificationType, Description: "A linkUp trap."},
	}
	refs := []model.Reference{
		{SourceModule: "IF-MIB", SourceName: "linkDown", TargetModule: "IF-MIB", TargetName: "ifIndex", Kind: model.RefNotificationObject, Position: 0},
		{SourceModule: "IF-MIB", SourceName: "linkUp", TargetModule: "IF-MIB", TargetName: "ifIndex", Kind: model.RefNotificationObject, Position: 0},
	}
	if err := st.ReplaceModule(ctx, mod, syms, refs, nil); err != nil {
		t.Fatalf("seed IF-MIB: %v", err)
	}
	return &handlers{st: st}
}

func TestSearchMIBsRanked(t *testing.T) {
	h := seededHandlers(t)
	_, out, err := h.searchMIBs(context.Background(), nil, searchIn{Query: "ifIndex"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(out.Hits) == 0 {
		t.Fatal("expected at least one hit for ifIndex")
	}
	if out.Hits[0].Name != "ifIndex" {
		t.Errorf("expected ifIndex ranked first, got %q", out.Hits[0].Name)
	}
}

func TestLookupOIDExact(t *testing.T) {
	h := seededHandlers(t)
	_, out, err := h.lookupOID(context.Background(), nil, lookupOIDIn{OID: "1.3.6.1.2.1.2.2.1.1"})
	if err != nil {
		t.Fatalf("lookup_oid: %v", err)
	}
	if out.Exact == nil || out.Exact.Name != "ifIndex" {
		t.Fatalf("expected exact ifIndex, got %+v", out.Exact)
	}
}

func TestLookupOIDNearestPrefix(t *testing.T) {
	h := seededHandlers(t)
	// The interfaces subtree root owns no symbol of its own, so this falls
	// through to the nearest-prefix search and returns the columns beneath it.
	_, out, err := h.lookupOID(context.Background(), nil, lookupOIDIn{OID: "1.3.6.1.2.1.2"})
	if err != nil {
		t.Fatalf("lookup_oid: %v", err)
	}
	if out.Exact != nil {
		t.Fatalf("expected no exact owner, got %+v", out.Exact)
	}
	if len(out.NearestPrefix) == 0 {
		t.Error("expected nearest-prefix hits under the interfaces subtree")
	}
}

func TestLookupSymbolKnown(t *testing.T) {
	h := seededHandlers(t)
	_, out, err := h.lookupSymbol(context.Background(), nil, lookupSymbolIn{Module: "IF-MIB", Name: "ifIndex"})
	if err != nil {
		t.Fatalf("lookup_symbol: %v", err)
	}
	if !out.Found || out.Symbol == nil {
		t.Fatalf("expected ifIndex found, got %+v", out)
	}
	if out.Symbol.OID != "1.3.6.1.2.1.2.2.1.1" || out.Symbol.Kind != "column" {
		t.Errorf("unexpected symbol detail: %+v", out.Symbol)
	}
}

func TestDecodeWalkResolvable(t *testing.T) {
	h := seededHandlers(t)
	capture := ".1.3.6.1.2.1.2.2.1.1.1 = INTEGER: 1\n"
	_, out, err := h.decodeWalk(context.Background(), nil, decodeWalkIn{Text: capture})
	if err != nil {
		t.Fatalf("decode_walk: %v", err)
	}
	if len(out.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(out.Entries))
	}
	e := out.Entries[0]
	if !e.Resolved || e.Module != "IF-MIB" || e.Symbol != "ifIndex" {
		t.Errorf("expected resolved ifIndex, got %+v", e)
	}
}

func TestDecodeWalkUnresolvedCarriesHints(t *testing.T) {
	h := seededHandlers(t)
	// An OID under enterprises that no loaded module owns.
	capture := ".1.3.6.1.4.1.99999.1.2.3 = STRING: \"x\"\n"
	_, out, err := h.decodeWalk(context.Background(), nil, decodeWalkIn{Text: capture})
	if err != nil {
		t.Fatalf("decode_walk: %v", err)
	}
	if len(out.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(out.Entries))
	}
	e := out.Entries[0]
	if e.Resolved || e.Unresolved == nil {
		t.Fatalf("expected unresolved entry with hints, got %+v", e)
	}
	if e.Unresolved.EnterpriseID != 99999 {
		t.Errorf("expected enterprise PEN 99999, got %d", e.Unresolved.EnterpriseID)
	}
}

func TestClassifyNotificationKnown(t *testing.T) {
	h := seededHandlers(t)

	// linkDown is the raise side of the link up/down pair.
	_, down, err := h.classifyNotification(context.Background(), nil, classifyIn{Module: "IF-MIB", Name: "linkDown"})
	if err != nil {
		t.Fatalf("classify_notification linkDown: %v", err)
	}
	if !down.Found || down.Relationship == nil {
		t.Fatalf("expected linkDown classified, got %+v", down)
	}
	if down.Relationship.Classification != "raise" {
		t.Errorf("linkDown classification = %q, want raise", down.Relationship.Classification)
	}

	// linkUp is the clear side and names the raise it clears, exercising the
	// notification_pair join in GetRelationship.
	_, up, err := h.classifyNotification(context.Background(), nil, classifyIn{Module: "IF-MIB", Name: "linkUp"})
	if err != nil {
		t.Fatalf("classify_notification linkUp: %v", err)
	}
	if up.Relationship == nil || up.Relationship.Classification != "clear" {
		t.Fatalf("linkUp classification = %+v, want clear", up.Relationship)
	}
	if len(up.Relationship.Clears) == 0 || up.Relationship.Clears[0] != "linkDown" {
		t.Errorf("linkUp.Clears = %v, want [linkDown]", up.Relationship.Clears)
	}
}

// TestServerAdvertisesFiveReadOnlyTools is the automated stand-in for the
// manual stdio handshake check: connect a client to the wired server over an
// in-memory transport and assert exactly the five expected tools are listed.
func TestServerAdvertisesFiveReadOnlyTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := seededHandlers(t)
	srv := newServer(h.st)

	serverT, clientT := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	got := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	want := []string{"search_mibs", "lookup_oid", "lookup_symbol", "decode_walk", "classify_notification"}
	if len(res.Tools) != len(want) {
		t.Errorf("advertised %d tools, want %d: %v", len(res.Tools), len(want), got)
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing advertised tool %q", name)
		}
	}
}
