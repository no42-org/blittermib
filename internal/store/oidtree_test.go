/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"context"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
)

// oidSym is a minimal symbol for trie tests — only module/name/oid
// matter; RebuildOIDTree derives parent/label/seg from the OID itself.
func oidSym(module, name, oid string) model.Symbol {
	return model.Symbol{
		ModuleName: module, Name: name, OID: oid,
		Kind: model.KindObjectIdentity, Status: model.StatusCurrent,
	}
}

func seedAndBuild(t *testing.T, s *Store, mod string, syms []model.Symbol) {
	t.Helper()
	ctx := context.Background()
	m := &model.Module{Name: mod, ParseStatus: model.ParseStatusClean}
	if err := s.ReplaceModule(ctx, m, syms, nil, nil); err != nil {
		t.Fatalf("ReplaceModule(%s): %v", mod, err)
	}
	if err := s.RebuildOIDTree(ctx); err != nil {
		t.Fatalf("RebuildOIDTree: %v", err)
	}
}

// node reads one oid_node row; ok is false when absent.
func node(t *testing.T, s *Store, oid string) (hasSymbol bool, childCount int, name, mod string, ok bool) {
	t.Helper()
	var hs, cc int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT has_symbol, child_count, name, module_name FROM oid_node WHERE oid = ?`,
		oid).Scan(&hs, &cc, &name, &mod)
	if err != nil {
		return false, 0, "", "", false
	}
	return hs == 1, cc, name, mod, true
}

// TestRebuildOIDTreeBridgesHoles seeds a real node and a deeper real
// node with the intermediate prefix missing; the rebuild must invent a
// synthetic bridge so the deep subtree is reachable from the root.
func TestRebuildOIDTreeBridgesHoles(t *testing.T) {
	s := newStore(t)
	// 1.3.6.1.4.1.99 (real) and 1.3.6.1.4.1.99.5.1 (real); 99.5 is a hole.
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "vendor", "1.3.6.1.4.1.99"),
		oidSym("M", "leaf", "1.3.6.1.4.1.99.5.1"),
		oidSym("M", "two", "1.3.6.1.4.1.99.2"),
	})

	if hs, _, _, _, ok := node(t, s, "1.3.6.1.4.1.99"); !ok || !hs {
		t.Errorf("1.3.6.1.4.1.99 should be a real node (ok=%v hasSymbol=%v)", ok, hs)
	}
	hs, cc, _, _, ok := node(t, s, "1.3.6.1.4.1.99.5")
	if !ok {
		t.Fatal("synthetic bridge 1.3.6.1.4.1.99.5 missing — deep subtree orphaned")
	}
	if hs {
		t.Error("1.3.6.1.4.1.99.5 should be synthetic (has_symbol=0)")
	}
	if cc != 1 {
		t.Errorf("bridge 99.5 child_count = %d, want 1 (the .5.1 leaf)", cc)
	}
	// 99 has two children: the .2 leaf and the .5 bridge.
	if _, cc, _, _, _ := node(t, s, "1.3.6.1.4.1.99"); cc != 2 {
		t.Errorf("99 child_count = %d, want 2", cc)
	}
}

// TestRebuildOIDTreeDedupWinner covers the UNIQUE-constraint regression:
// one OID defined twice in a module (two names) and again in another
// module must collapse to a single node, won by (module_name, name) order.
func TestRebuildOIDTreeDedupWinner(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	// Module A defines the same OID under two names; B defines it too.
	if err := s.ReplaceModule(ctx, &model.Module{Name: "A", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			oidSym("A", "xAlias", "1.3.6.1.4.1.99.1"),
			oidSym("A", "x", "1.3.6.1.4.1.99.1"),
		}, nil, nil); err != nil {
		t.Fatalf("ReplaceModule(A): %v", err)
	}
	if err := s.ReplaceModule(ctx, &model.Module{Name: "B", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{oidSym("B", "y", "1.3.6.1.4.1.99.1")}, nil, nil); err != nil {
		t.Fatalf("ReplaceModule(B): %v", err)
	}
	if err := s.RebuildOIDTree(ctx); err != nil {
		t.Fatalf("RebuildOIDTree: %v", err)
	}

	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM oid_node WHERE oid = '1.3.6.1.4.1.99.1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("OID defined 3× collapsed to %d nodes, want 1", n)
	}
	_, _, name, mod, _ := node(t, s, "1.3.6.1.4.1.99.1")
	if mod != "A" || name != "x" {
		t.Errorf("winner = %s::%s, want A::x (module then name order)", mod, name)
	}
}

// TestListNodeChildrenNumericKeyset checks numeric sibling ordering and
// keyset pagination — the core of the read path.
func TestListNodeChildrenNumericKeyset(t *testing.T) {
	s := newStore(t)
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "a", "1.3.6.1.4.1.99.1"),
		oidSym("M", "b", "1.3.6.1.4.1.99.2"),
		oidSym("M", "c", "1.3.6.1.4.1.99.9"),
		oidSym("M", "d", "1.3.6.1.4.1.99.10"),
		oidSym("M", "e", "1.3.6.1.4.1.99.100"),
	})
	ctx := context.Background()
	const parent = "1.3.6.1.4.1.99"

	all, err := s.ListNodeChildren(ctx, parent, -1, 200)
	if err != nil {
		t.Fatalf("ListNodeChildren: %v", err)
	}
	gotSegs := make([]int64, len(all))
	for i, n := range all {
		gotSegs[i] = n.Seg
	}
	want := []int64{1, 2, 9, 10, 100} // numeric, not lexical (1,10,100,2,9)
	if len(gotSegs) != len(want) {
		t.Fatalf("got %d children, want %d", len(gotSegs), len(want))
	}
	for i := range want {
		if gotSegs[i] != want[i] {
			t.Fatalf("child order = %v, want %v (numeric)", gotSegs, want)
		}
	}

	// Keyset: after seg 9 → [10, 100].
	page, err := s.ListNodeChildren(ctx, parent, 9, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Seg != 10 || page[1].Seg != 100 {
		t.Errorf("after seg=9 = %v, want segs [10 100]", page)
	}

	// Limit clamps the page.
	first, err := s.ListNodeChildren(ctx, parent, -1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Seg != 1 || first[1].Seg != 2 {
		t.Errorf("limit=2 first page = %v, want segs [1 2]", first)
	}

	// has_children flag: 99 (as a child of 1.3.6.1.4.1) reports children.
	parents, err := s.ListNodeChildren(ctx, "1.3.6.1.4.1", -1, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || !parents[0].HasChildren {
		t.Errorf("node 99 should report HasChildren; got %+v", parents)
	}
}
