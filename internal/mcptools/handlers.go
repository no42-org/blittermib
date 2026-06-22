/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mcptools

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/walk"
)

// handlers holds the read-only store the tool handlers query.
type handlers struct{ st *store.Store }

const (
	defaultSearchLimit = 20
	// maxSearchLimit is the upper bound on search_mibs `limit`. A
	// too-large limit is a benign convenience overshoot (not a safety
	// boundary), so it clamps to this ceiling rather than erroring —
	// the bound matters most for the network transport, where an
	// untrusted caller could otherwise request an unbounded result set.
	maxSearchLimit = 100
	prefixHitLimit = 5
	// walkMaxBytes caps decode_walk input, mirroring the web /walk
	// size limit (internal/server walkMaxBytes) so a giant paste can't
	// drive unbounded parse/lookup work. This is a hard safety cap, so
	// oversize input is rejected, not truncated.
	walkMaxBytes = 10 << 20 // 10 MB
)

// --- search_mibs ---

type searchIn struct {
	Query string `json:"query" jsonschema:"full-text query over symbol name, OID, and description"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of hits to return (default 20)"`
}

type searchOut struct {
	Hits []hitView `json:"hits"`
}

func (h *handlers) searchMIBs(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	limit := in.Limit
	if limit <= 0 {
		// Unset, zero, or a negative value sent over the wire all fall
		// back to the default rather than erroring.
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		// Clamp an over-large request down to the ceiling instead of
		// rejecting it — the caller still gets a useful (bounded) page.
		limit = maxSearchLimit
	}
	hits, err := h.st.Search(ctx, in.Query, limit)
	if err != nil {
		return nil, searchOut{}, err
	}
	return nil, searchOut{Hits: toHitViews(hits)}, nil
}

// --- lookup_oid ---

type lookupOIDIn struct {
	OID string `json:"oid" jsonschema:"numeric SNMP OID, e.g. 1.3.6.1.4.1.2636.3.1"`
}

type lookupOIDOut struct {
	Exact         *symbolView `json:"exact,omitempty"`
	NearestPrefix []hitView   `json:"nearest_prefix,omitempty"`
}

func (h *handlers) lookupOID(ctx context.Context, _ *mcp.CallToolRequest, in lookupOIDIn) (*mcp.CallToolResult, lookupOIDOut, error) {
	sym, err := h.st.GetSymbolByOID(ctx, in.OID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, lookupOIDOut{}, err
	}
	if sym != nil {
		return nil, lookupOIDOut{Exact: toSymbolView(sym)}, nil
	}
	// No exact owner: fall back to the nearest known prefix. The DB was
	// just reached by GetSymbolByOID, so the only realistic error here is
	// SearchByOIDPrefix rejecting a malformed (non-numeric) OID — treat
	// that as no match, since not-found is data, not a transport error.
	hits, err := h.st.SearchByOIDPrefix(ctx, in.OID, prefixHitLimit)
	if err != nil {
		return nil, lookupOIDOut{}, nil
	}
	return nil, lookupOIDOut{NearestPrefix: toHitViews(hits)}, nil
}

// --- lookup_symbol ---

type lookupSymbolIn struct {
	Module string `json:"module" jsonschema:"MIB module name, e.g. IF-MIB"`
	Name   string `json:"name" jsonschema:"symbol name within the module, e.g. ifIndex"`
}

type lookupSymbolOut struct {
	Found  bool        `json:"found"`
	Symbol *symbolView `json:"symbol,omitempty"`
}

func (h *handlers) lookupSymbol(ctx context.Context, _ *mcp.CallToolRequest, in lookupSymbolIn) (*mcp.CallToolResult, lookupSymbolOut, error) {
	sym, err := h.st.GetSymbol(ctx, in.Module, in.Name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, lookupSymbolOut{Found: false}, nil
	}
	if err != nil {
		return nil, lookupSymbolOut{}, err
	}
	return nil, lookupSymbolOut{Found: true, Symbol: toSymbolView(sym)}, nil
}

// --- classify_notification ---

type classifyIn struct {
	Module string `json:"module" jsonschema:"MIB module name"`
	Name   string `json:"name" jsonschema:"notification (NOTIFICATION-TYPE/TRAP-TYPE) name"`
}

type classifyOut struct {
	Found        bool              `json:"found"`
	Relationship *notificationView `json:"relationship,omitempty"`
}

func (h *handlers) classifyNotification(ctx context.Context, _ *mcp.CallToolRequest, in classifyIn) (*mcp.CallToolResult, classifyOut, error) {
	rel, err := h.st.GetRelationship(ctx, in.Module, in.Name)
	if err != nil {
		return nil, classifyOut{}, err
	}
	if rel == nil {
		return nil, classifyOut{Found: false}, nil
	}
	return nil, classifyOut{Found: true, Relationship: toNotificationView(rel)}, nil
}

// --- decode_walk ---

type decodeWalkIn struct {
	Text string `json:"text" jsonschema:"raw snmpwalk/snmpbulkwalk output to decode"`
}

type decodeWalkOut struct {
	Entries      []walkEntryView `json:"entries"`
	Modules      []string        `json:"modules,omitempty"`
	SkippedLines int             `json:"skipped_lines"`
}

func (h *handlers) decodeWalk(ctx context.Context, _ *mcp.CallToolRequest, in decodeWalkIn) (*mcp.CallToolResult, decodeWalkOut, error) {
	if len(in.Text) > walkMaxBytes {
		return nil, decodeWalkOut{}, fmt.Errorf("walk text exceeds %d-byte limit", walkMaxBytes)
	}
	resolved, err := walk.Resolve(ctx, walk.Parse(in.Text), h.st)
	if err != nil {
		return nil, decodeWalkOut{}, err
	}
	entries := make([]walkEntryView, 0, len(resolved.Entries))
	for _, e := range resolved.Entries {
		entries = append(entries, toWalkEntryView(e))
	}
	return nil, decodeWalkOut{
		Entries:      entries,
		Modules:      resolved.Modules,
		SkippedLines: resolved.SkippedLines,
	}, nil
}
