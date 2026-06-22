/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mcptools

import (
	"context"
	"fmt"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// bulkHandlers seeds a store with n column symbols that all share a common
// description token, so a single query matches every one of them — enough
// rows to observe the search_mibs limit clamp at both ends.
func bulkHandlers(t *testing.T, n int) *handlers {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mod := &model.Module{Name: "BULK-MIB", OIDRoot: "1.3.6.1.4.1.61509.1", ParseStatus: model.ParseStatusClean}
	syms := make([]model.Symbol, 0, n)
	for i := 1; i <= n; i++ {
		syms = append(syms, model.Symbol{
			ModuleName:  "BULK-MIB",
			Name:        fmt.Sprintf("bulkCol%d", i),
			OID:         fmt.Sprintf("1.3.6.1.4.1.61509.1.%d", i),
			ParentOID:   "1.3.6.1.4.1.61509.1",
			Kind:        model.KindColumn,
			Description: "searchabletoken interface counter",
		})
	}
	if err := st.ReplaceModule(ctx, mod, syms, nil, nil); err != nil {
		t.Fatalf("seed BULK-MIB: %v", err)
	}
	return &handlers{st: st}
}

// The search_mibs limit is bounded on both ends: zero/negative fall back to
// the default of 20, an over-ceiling value clamps to 100, and an in-range
// value is honored. Both transports share this handler, so testing it here
// covers stdio and the network /mcp route at once.
func TestSearchMIBsLimitBounds(t *testing.T) {
	h := bulkHandlers(t, 150)

	cases := []struct {
		name  string
		limit int
		want  int // expected hit count given 150 matching rows
	}{
		{"zero falls back to default", 0, defaultSearchLimit},
		{"negative falls back to default", -5, defaultSearchLimit},
		{"in-range is honored", 50, 50},
		{"over-ceiling clamps", 1000, maxSearchLimit},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, out, err := h.searchMIBs(context.Background(), nil, searchIn{Query: "searchabletoken", Limit: tc.limit})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(out.Hits) != tc.want {
				t.Errorf("limit %d returned %d hits, want %d", tc.limit, len(out.Hits), tc.want)
			}
		})
	}
}
