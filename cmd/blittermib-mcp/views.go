/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"github.com/no42-org/blittermib/internal/correlate"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/walk"
)

// The view types below are the agent-facing wire format. They are kept
// deliberately separate from the internal structs so the tool contract
// stays stable when internal types evolve, and so internal-only fields
// (model.Symbol.ID, SourceLine) never leak to clients.

// symbolView is the projection of model.Symbol returned by lookup tools.
type symbolView struct {
	Module       string            `json:"module"`
	Name         string            `json:"name"`
	OID          string            `json:"oid"`
	Kind         string            `json:"kind"`
	Syntax       string            `json:"syntax,omitempty"`
	Access       string            `json:"access,omitempty"`
	Status       string            `json:"status,omitempty"`
	Units        string            `json:"units,omitempty"`
	IndexColumns []string          `json:"index_columns,omitempty"`
	IndexImplied bool              `json:"index_implied,omitempty"`
	EnumValues   []model.EnumValue `json:"enum_values,omitempty"`
	Description  string            `json:"description,omitempty"`
}

func toSymbolView(s *model.Symbol) *symbolView {
	if s == nil {
		return nil
	}
	return &symbolView{
		Module:       s.ModuleName,
		Name:         s.Name,
		OID:          s.OID,
		Kind:         string(s.Kind),
		Syntax:       s.Syntax,
		Access:       string(s.Access),
		Status:       string(s.Status),
		Units:        s.Units,
		IndexColumns: s.IndexColumns,
		IndexImplied: s.IndexImplied,
		EnumValues:   s.EnumValues,
		Description:  s.Description,
	}
}

// hitView is the projection of store.SearchHit returned by search and
// nearest-prefix OID lookups.
type hitView struct {
	Module  string  `json:"module"`
	Name    string  `json:"name"`
	OID     string  `json:"oid"`
	Kind    string  `json:"kind"`
	Snippet string  `json:"snippet,omitempty"`
	Rank    float64 `json:"rank"`
}

func toHitViews(hits []store.SearchHit) []hitView {
	out := make([]hitView, 0, len(hits))
	for _, h := range hits {
		out = append(out, hitView{
			Module:  h.Module,
			Name:    h.Name,
			OID:     h.OID,
			Kind:    h.Kind,
			Snippet: h.Snippet,
			Rank:    h.Rank,
		})
	}
	return out
}

// walkEntryView is the projection of one walk.ResolvedEntry.
type walkEntryView struct {
	Ident       string          `json:"ident"`
	Resolved    bool            `json:"resolved"`
	Module      string          `json:"module,omitempty"`
	Symbol      string          `json:"symbol,omitempty"`
	SymbolOID   string          `json:"symbol_oid,omitempty"`
	Suffix      string          `json:"suffix,omitempty"`
	IndexName   string          `json:"index_name,omitempty"`
	IndexValue  string          `json:"index_value,omitempty"`
	IndexDecode string          `json:"index_decode,omitempty"`
	Unresolved  *unresolvedView `json:"unresolved,omitempty"`
}

// unresolvedView carries the fallback hints for an OID no loaded module
// covers (enterprise PEN, nearest known module root, canonical arc).
type unresolvedView struct {
	Prefix            string `json:"prefix,omitempty"`
	MatchedModuleRoot string `json:"matched_module_root,omitempty"`
	EnterpriseID      uint32 `json:"enterprise_id,omitempty"`
	EnterpriseName    string `json:"enterprise_name,omitempty"`
	CanonicalName     string `json:"canonical_name,omitempty"`
}

func toWalkEntryView(e walk.ResolvedEntry) walkEntryView {
	v := walkEntryView{
		Ident:       e.Entry.Ident,
		Resolved:    e.Resolved,
		Module:      e.Module,
		Symbol:      e.Symbol,
		SymbolOID:   e.SymbolOID,
		Suffix:      e.Suffix,
		IndexName:   e.IndexName,
		IndexValue:  e.IndexValue,
		IndexDecode: e.IndexDecode,
	}
	if e.Unresolved != nil {
		v.Unresolved = &unresolvedView{
			Prefix:            e.Unresolved.Prefix,
			MatchedModuleRoot: e.Unresolved.MatchedModuleRoot,
			EnterpriseID:      e.Unresolved.EnterpriseID,
			EnterpriseName:    e.Unresolved.EnterpriseName,
			CanonicalName:     e.Unresolved.CanonicalName,
		}
	}
	return v
}

// notificationView is the projection of a correlate.Relationship — the
// inferred raise/clear/orphan classification with its evidence trail.
type notificationView struct {
	Notification   string   `json:"notification"`
	Classification string   `json:"classification"`
	Confidence     string   `json:"confidence"`
	Evidence       string   `json:"evidence,omitempty"`
	Clears         []string `json:"clears,omitempty"`
}

func toNotificationView(r *correlate.Relationship) *notificationView {
	if r == nil {
		return nil
	}
	return &notificationView{
		Notification:   r.Notification,
		Classification: string(r.Class),
		Confidence:     string(r.Confidence),
		Evidence:       r.Evidence.String(),
		Clears:         r.Clears,
	}
}
