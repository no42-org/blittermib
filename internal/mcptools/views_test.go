/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mcptools

import (
	"encoding/json"
	"testing"

	"github.com/no42-org/blittermib/internal/correlate"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/walk"
)

// keysOf marshals v to JSON and returns its top-level object keys.
func keysOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys := make(map[string]bool, len(m))
	for k := range m {
		keys[k] = true
	}
	return keys
}

func TestViewFieldsExposed(t *testing.T) {
	cases := []struct {
		name string
		view any
		want []string
	}{
		{
			name: "symbolView",
			view: toSymbolView(&model.Symbol{
				ModuleName: "IF-MIB", Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1",
				Kind: model.SymbolKind("column"), Syntax: "InterfaceIndex",
				Access: model.Access("read-only"), Status: model.Status("current"),
				IndexColumns: []string{"ifIndex"},
				EnumValues:   []model.EnumValue{{Name: "up", Number: 1}},
				Description:  "A unique value for each interface.",
			}),
			want: []string{"module", "name", "oid", "kind", "syntax", "access", "status", "index_columns", "enum_values", "description"},
		},
		{
			name: "hitView",
			view: toHitViews([]store.SearchHit{{
				Module: "IF-MIB", Name: "ifIndex", OID: "1.3.6.1.2.1.2.2.1.1",
				Kind: "column", Snippet: "unique value", Rank: -1.5,
			}})[0],
			want: []string{"module", "name", "oid", "kind", "snippet", "rank"},
		},
		{
			name: "walkEntryView resolved",
			view: toWalkEntryView(walk.ResolvedEntry{
				Entry: walk.Entry{Ident: "1.3.6.1.2.1.2.2.1.1.1"}, Resolved: true,
				Module: "IF-MIB", Symbol: "ifIndex", SymbolOID: "1.3.6.1.2.1.2.2.1.1",
				Suffix: "1", IndexName: "ifIndex", IndexValue: "1", IndexDecode: "integer",
			}),
			want: []string{"ident", "resolved", "module", "symbol", "symbol_oid", "suffix", "index_name", "index_value", "index_decode"},
		},
		{
			name: "walkEntryView unresolved",
			view: toWalkEntryView(walk.ResolvedEntry{
				Entry: walk.Entry{Ident: "1.3.6.1.4.1.9999.1"},
				Unresolved: &walk.Unresolved{
					Prefix: "1.3.6.1.4.1.9999.1", EnterpriseID: 9999,
					EnterpriseName: "Example", CanonicalName: "enterprises",
				},
			}),
			want: []string{"ident", "resolved", "unresolved"},
		},
		{
			name: "notificationView",
			view: toNotificationView(&correlate.Relationship{
				Notification: "linkDown", Class: correlate.ClassRaise, Confidence: correlate.ConfHigh,
				Evidence: correlate.Evidence{Summary: "paired with linkUp"},
				Clears:   []string{"linkUp"},
			}),
			want: []string{"notification", "classification", "confidence", "evidence", "clears"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys := keysOf(t, tc.view)
			for _, k := range tc.want {
				if !keys[k] {
					t.Errorf("%s: missing field %q (got keys %v)", tc.name, k, keys)
				}
			}
		})
	}
}

// TestSymbolViewDropsInternalFields guards that internal-only fields never
// leak into the agent-facing wire format.
func TestSymbolViewDropsInternalFields(t *testing.T) {
	keys := keysOf(t, toSymbolView(&model.Symbol{ID: 42, Name: "x", SourceLine: 7}))
	for _, leaked := range []string{"ID", "id", "SourceLine", "source_line"} {
		if keys[leaked] {
			t.Errorf("symbolView leaked internal field %q", leaked)
		}
	}
}
