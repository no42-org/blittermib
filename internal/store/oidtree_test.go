/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// foldSegs joins a folded row's chain labels into a dotted seg-path
// (".1.3.6" minus the leading dot) for compact assertions.
func foldSegs(r FoldedNodeRow) string {
	out := ""
	for i, s := range r.Chain {
		if i > 0 {
			out += "."
		}
		out += s.Label
	}
	return out
}

// TestListNodeChildrenFolded covers path compression: a single-child run
// collapses, folding INTO the branch child where it stops (a node with one
// child merges with that child even when the child is a branch), and a run
// bottoming out in a leaf absorbs the leaf.
func TestListNodeChildrenFolded(t *testing.T) {
	s := newStore(t)
	// Under enterprises (1.3.6.1.4.1):
	//   .99            single child .99.1 …                → folds in
	//     .99.1        single child .99.1.1 (a BRANCH)     → folds in, stops
	//       .99.1.1    two children .1 and .2 (branch)     → the anchor
	//   .77            single child .77.3 (a LEAF)         → absorbs leaf
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "a", "1.3.6.1.4.1.99.1.1.1"),
		oidSym("M", "b", "1.3.6.1.4.1.99.1.1.2"),
		oidSym("M", "leaf", "1.3.6.1.4.1.77.3"),
	})
	ctx := context.Background()

	rows, err := s.ListNodeChildrenFolded(ctx, "1.3.6.1.4.1", -1, 200, false, "")
	if err != nil {
		t.Fatalf("ListNodeChildrenFolded: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("enterprises children = %d rows, want 2 (.77 and .99)", len(rows))
	}
	// Numeric order: 77 before 99.
	r77, r99 := rows[0], rows[1]
	if r77.Seg != 77 || r99.Seg != 99 {
		t.Fatalf("rows segs = [%d %d], want [77 99]", r77.Seg, r99.Seg)
	}

	// .77 run bottoms out in the .77.3 leaf → leaf absorbed into one row.
	if got := foldSegs(r77); got != "77.3" {
		t.Errorf(".77 seg-path = %q, want %q (leaf absorbed)", got, "77.3")
	}
	if r77.Anchor().OID != "1.3.6.1.4.1.77.3" || r77.HasChildren() {
		t.Errorf(".77 anchor = %q hasChildren=%v, want the .77.3 leaf",
			r77.Anchor().OID, r77.HasChildren())
	}

	// .99 folds .99 → .99.1 → .99.1.1, INCLUDING the branch .99.1.1 where it
	// stops (each of .99 and .99.1 has exactly one child).
	if got := foldSegs(r99); got != "99.1.1" {
		t.Errorf(".99 seg-path = %q, want %q (folds into the branch)", got, "99.1.1")
	}
	if r99.Anchor().OID != "1.3.6.1.4.1.99.1.1" {
		t.Errorf(".99 anchor = %q, want 1.3.6.1.4.1.99.1.1", r99.Anchor().OID)
	}
	if r99.ChildCount != 2 || !r99.HasChildren() {
		t.Errorf(".99 anchor child_count = %d, want 2 (the .1/.2 leaves below)", r99.ChildCount)
	}
	if r99.DirectOID() != "1.3.6.1.4.1.99" {
		t.Errorf(".99 direct OID (cursor) = %q, want 1.3.6.1.4.1.99 — not the anchor",
			r99.DirectOID())
	}

	// The two leaves under the folded .99.1.1 anchor are its own rows.
	below, err := s.ListNodeChildrenFolded(ctx, "1.3.6.1.4.1.99.1.1", -1, 200, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(below) != 2 {
		t.Fatalf("below .99.1.1 = %+v, want the .1 and .2 leaves", below)
	}
}

// TestListNodeChildrenFoldedKeyset pins that folding leaves the keyset
// intact: one row per DIRECT child, cursor on the direct child's segment.
func TestListNodeChildrenFoldedKeyset(t *testing.T) {
	s := newStore(t)
	// Three single-child spines under .99: .1→.1.7, .2→.2.7, .9→.9.7.
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "a", "1.3.6.1.4.1.99.1.7"),
		oidSym("M", "b", "1.3.6.1.4.1.99.2.7"),
		oidSym("M", "c", "1.3.6.1.4.1.99.9.7"),
	})
	ctx := context.Background()
	const parent = "1.3.6.1.4.1.99"

	all, err := s.ListNodeChildrenFolded(ctx, parent, -1, 200, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d folded rows, want 3 (one per direct child)", len(all))
	}
	wantSeg := []int64{1, 2, 9}
	for i, r := range all {
		if r.Seg != wantSeg[i] {
			t.Fatalf("row %d direct seg = %d, want %d", i, r.Seg, wantSeg[i])
		}
		if len(r.Chain) != 2 { // direct + absorbed leaf
			t.Errorf("row %d chain len = %d, want 2 (single-child + leaf)", i, len(r.Chain))
		}
	}

	// Keyset after the direct seg 1 → the .2 and .9 spines remain.
	page, err := s.ListNodeChildrenFolded(ctx, parent, 1, 200, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Seg != 2 || page[1].Seg != 9 {
		t.Errorf("after direct seg=1 = %v, want direct segs [2 9]", page)
	}
}

// TestListNodeChildrenFoldedBranches pins the workspace "container map":
// branchesOnly hides leaf objects, the fold stops at the deepest CONTAINER
// (never absorbs a leaf), and a container whose children are all leaves is
// reported non-expandable (Expandable=false) though its ChildCount stands.
func TestListNodeChildrenFoldedBranches(t *testing.T) {
	s := newStore(t)
	// Under sFlowAgent (…99.1): 3 scalars (leaves) + a table whose entry
	// has 2 leaf columns.
	//   …99.1.1/.2/.3   scalars (leaves)
	//   …99.1.4         table  → …99.1.4.1 entry → …99.1.4.1.1/.2 columns
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "s1", "1.3.6.1.4.1.99.1.1"),
		oidSym("M", "s2", "1.3.6.1.4.1.99.1.2"),
		oidSym("M", "s3", "1.3.6.1.4.1.99.1.3"),
		oidSym("M", "col1", "1.3.6.1.4.1.99.1.4.1.1"),
		oidSym("M", "col2", "1.3.6.1.4.1.99.1.4.1.2"),
	})
	ctx := context.Background()
	const agent = "1.3.6.1.4.1.99.1"

	// Default mode shows all 4 direct children (3 scalars + the folded
	// table.entry).
	def, err := s.ListNodeChildrenFolded(ctx, agent, -1, 200, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(def) != 4 {
		t.Fatalf("default mode under sFlowAgent = %d rows, want 4 (3 scalars + table)", len(def))
	}

	// branchesOnly hides the 3 scalar leaves → one row, the table folded
	// to its entry, stopping at the entry (columns NOT absorbed).
	br, err := s.ListNodeChildrenFolded(ctx, agent, -1, 200, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(br) != 1 {
		t.Fatalf("branchesOnly under sFlowAgent = %d rows, want 1 (scalars hidden)", len(br))
	}
	row := br[0]
	if got := foldSegs(row); got != "4.1" {
		t.Errorf("folded seg-path = %q, want %q (table.entry, columns not absorbed)", got, "4.1")
	}
	if row.Anchor().OID != "1.3.6.1.4.1.99.1.4.1" {
		t.Errorf("anchor = %q, want the …99.1.4.1 entry", row.Anchor().OID)
	}
	if row.ChildCount != 2 {
		t.Errorf("entry ChildCount = %d, want 2 (its columns)", row.ChildCount)
	}
	if row.HasChildren() {
		t.Error("entry should be a non-expandable tree leaf (all children are leaf columns)")
	}

	// Drilling into the entry in branchesOnly yields nothing — its only
	// children are leaf columns, which live in the list pane, not the tree.
	cols, err := s.ListNodeChildrenFolded(ctx, "1.3.6.1.4.1.99.1.4.1", -1, 200, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 0 {
		t.Errorf("branchesOnly under the entry = %d rows, want 0 (columns hidden)", len(cols))
	}
}

// nodeFamily reads the three subtree-family flags for one oid_node row.
func nodeFamily(t *testing.T, s *Store, oid string) (scalar, table, notif bool) {
	t.Helper()
	var hs, ht, hn int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT has_scalar, has_table, has_notif FROM oid_node WHERE oid = ?`,
		oid).Scan(&hs, &ht, &hn); err != nil {
		t.Fatalf("read family flags for %s: %v", oid, err)
	}
	return hs == 1, ht == 1, hn == 1
}

// kindSym is oidSym with an explicit kind.
func kindSym(module, name, oid string, kind model.SymbolKind) model.Symbol {
	s := oidSym(module, name, oid)
	s.Kind = kind
	return s
}

// TestRebuildOIDTreeFamilyFlags pins the subtree family flags: a leaf of a
// family sets that flag on itself and every ancestor up to the apex, an
// unrelated family stays 0, and a column counts under the SCALAR family
// (matching the list's kind buckets).
func TestRebuildOIDTreeFamilyFlags(t *testing.T) {
	s := newStore(t)
	seedAndBuild(t, s, "M", []model.Symbol{
		// system → a scalar.
		kindSym("M", "sysDescr", "1.3.6.1.2.1.1.1", model.KindScalar),
		// ipAddrTable → entry → a column (column is SCALAR family).
		kindSym("M", "ipAddrTable", "1.3.6.1.2.1.4.20", model.KindTable),
		kindSym("M", "ipAddrEntry", "1.3.6.1.2.1.4.20.1", model.KindTableEntry),
		kindSym("M", "ipAdEntAddr", "1.3.6.1.2.1.4.20.1.1", model.KindColumn),
		// a notification under its own arc.
		kindSym("M", "coldStart", "1.3.6.1.6.3.1.1.5.1", model.KindNotificationType),
	})

	// The scalar's ancestors carry has_scalar; not table/notif.
	if sc, tb, nt := nodeFamily(t, s, "1.3.6.1.2.1.1"); !sc || tb || nt {
		t.Errorf("system flags = scalar%v table%v notif%v, want scalar only", sc, tb, nt)
	}
	// The table node: has_table (it IS a table) AND has_scalar (its column
	// descendant is scalar-family). Not notif.
	if sc, tb, nt := nodeFamily(t, s, "1.3.6.1.2.1.4.20"); !sc || !tb || nt {
		t.Errorf("ipAddrTable flags = scalar%v table%v notif%v, want scalar+table", sc, tb, nt)
	}
	// The notification's arc: notif only.
	if sc, tb, nt := nodeFamily(t, s, "1.3.6.1.6.3.1.1.5"); sc || tb || !nt {
		t.Errorf("notif arc flags = scalar%v table%v notif%v, want notif only", sc, tb, nt)
	}
	// All three families reach the shared apex (iso=1): it has every kind
	// somewhere beneath it.
	if sc, tb, nt := nodeFamily(t, s, "1"); !sc || !tb || !nt {
		t.Errorf("apex flags = scalar%v table%v notif%v, want all three", sc, tb, nt)
	}
}

// TestListNodeChildrenFoldedFamily pins the kind-family filter on the
// container map: it shows only containers whose subtree holds the family,
// and a matched container is expandable only to children that ALSO match.
func TestListNodeChildrenFoldedFamily(t *testing.T) {
	s := newStore(t)
	// Under mib-2 (1.3.6.1.2.1): system has a scalar; interfaces has a
	// table (ifTable→ifEntry→ifIndex column).
	seedAndBuild(t, s, "M", []model.Symbol{
		kindSym("M", "sysDescr", "1.3.6.1.2.1.1.1", model.KindScalar),
		kindSym("M", "ifTable", "1.3.6.1.2.1.2.2", model.KindTable),
		kindSym("M", "ifEntry", "1.3.6.1.2.1.2.2.1", model.KindTableEntry),
		kindSym("M", "ifIndex", "1.3.6.1.2.1.2.2.1.1", model.KindColumn),
	})
	ctx := context.Background()
	const mib2 = "1.3.6.1.2.1"

	// branches + table family: only the interfaces branch (it holds a
	// table); system (scalars only) is pruned.
	tbl, err := s.ListNodeChildrenFolded(ctx, mib2, -1, 200, true, "table")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl) != 1 {
		t.Fatalf("mib-2 under 'table' = %d rows, want 1 (interfaces only)", len(tbl))
	}
	// interfaces is a single-child chain, so it folds to its table-entry.
	if d := tbl[0].DirectOID(); d != "1.3.6.1.2.1.2" {
		t.Errorf("'table' row direct child = %q, want interfaces 1.3.6.1.2.1.2", d)
	}
	if a := tbl[0].Anchor().OID; a != "1.3.6.1.2.1.2.2.1" {
		t.Errorf("'table' row anchor = %q, want the folded ifEntry 1.3.6.1.2.1.2.2.1", a)
	}

	// branches + notif family: nothing in mib-2 here is a notification.
	notif, err := s.ListNodeChildrenFolded(ctx, mib2, -1, 200, true, "notif")
	if err != nil {
		t.Fatal(err)
	}
	if len(notif) != 0 {
		t.Errorf("mib-2 under 'notif' = %d rows, want 0", len(notif))
	}

	// branches + scalar family: BOTH branches match — system (its scalar)
	// and interfaces (its column is scalar-family).
	sc, err := s.ListNodeChildrenFolded(ctx, mib2, -1, 200, true, "scalar")
	if err != nil {
		t.Fatal(err)
	}
	if len(sc) != 2 {
		t.Fatalf("mib-2 under 'scalar' = %d rows, want 2 (system + interfaces)", len(sc))
	}
}

// TestListNodeChildrenFoldedFamilySingleMatchFolds reproduces the isnsMIB
// case: a node with several children of which only ONE is family-matching
// folds INTO that child under the filter, collapsing two rows into one
// (.163.0 isnsMIB.isnsNotifications), instead of showing the module node
// and its notifications wrapper separately.
func TestListNodeChildrenFoldedFamilySingleMatchFolds(t *testing.T) {
	s := newStore(t)
	// isnsMIB (…163): a notifications wrapper (.0) holding two notifs, plus
	// two unrelated scalar children (.1, .2).
	seedAndBuild(t, s, "M", []model.Symbol{
		kindSym("M", "isnsMIB", "1.3.6.1.2.1.163", model.KindObjectIdentity),
		kindSym("M", "isnsNotifications", "1.3.6.1.2.1.163.0", model.KindObjectIdentity),
		kindSym("M", "isnsServerStart", "1.3.6.1.2.1.163.0.1", model.KindNotificationType),
		kindSym("M", "isnsServerShutdown", "1.3.6.1.2.1.163.0.2", model.KindNotificationType),
		kindSym("M", "isnsScalarA", "1.3.6.1.2.1.163.1", model.KindScalar),
		kindSym("M", "isnsScalarB", "1.3.6.1.2.1.163.2", model.KindScalar),
	})
	ctx := context.Background()

	// Under notif: listing mib-2's children yields one folded isnsMIB row
	// collapsed to its notifications wrapper.
	rows, err := s.ListNodeChildrenFolded(ctx, "1.3.6.1.2.1", -1, 200, true, "notif")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("mib-2 under 'notif' = %d rows, want 1 (the isnsMIB branch)", len(rows))
	}
	r := rows[0]
	if got := foldSegs(r); got != "163.0" {
		t.Errorf("seg-path = %q, want %q (isnsMIB folds into its wrapper)", got, "163.0")
	}
	if r.DirectOID() != "1.3.6.1.2.1.163" || r.Anchor().OID != "1.3.6.1.2.1.163.0" {
		t.Errorf("row direct/anchor = %s/%s, want 163/163.0", r.DirectOID(), r.Anchor().OID)
	}
	// The wrapper's children are notification LEAVES (hidden), so the
	// folded row is a non-expandable tree leaf.
	if r.HasChildren() {
		t.Error("folded isnsMIB.isnsNotifications should be a non-expandable leaf")
	}

	// Under scalar: isnsMIB has TWO scalar-matching children (.1, .2), so it
	// does NOT fold (count != 1) — it stays as its own .163 row. Both are
	// leaf scalars (shown in the list, not the tree), so it is a
	// non-expandable tree leaf.
	sc, err := s.ListNodeChildrenFolded(ctx, "1.3.6.1.2.1", -1, 200, true, "scalar")
	if err != nil {
		t.Fatal(err)
	}
	if len(sc) != 1 || foldSegs(sc[0]) != "163" || sc[0].HasChildren() {
		t.Fatalf("isnsMIB under 'scalar' = %+v, want one non-expandable .163 row (two scalar leaves)", sc)
	}
}

// TestListNodeChildrenBefore checks backward keyset paging (the "show
// earlier" path): segments strictly below the cursor, in ascending order.
func TestListNodeChildrenBefore(t *testing.T) {
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

	// Everything below seg 10, ascending.
	before, err := s.ListNodeChildrenBefore(ctx, parent, 10, 200)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int64, len(before))
	for i, n := range before {
		got[i] = n.Seg
	}
	want := []int64{1, 2, 9}
	if len(got) != len(want) {
		t.Fatalf("before seg=10 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("before seg=10 = %v, want %v (ascending)", got, want)
		}
	}

	// Closest page below 100 with limit 2 → [9, 10] (the two largest < 100).
	page, err := s.ListNodeChildrenBefore(ctx, parent, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Seg != 9 || page[1].Seg != 10 {
		t.Errorf("before seg=100 limit=2 = %v, want segs [9 10]", page)
	}
}

// spineParents extracts the ordered Parent of each level for compact
// assertions.
func spineParents(levels []SpineLevel) []string {
	out := make([]string, len(levels))
	for i, lv := range levels {
		out[i] = lv.Parent
	}
	return out
}

// TestSpinePagesDeepFocus pins the core preload: SpinePages returns one
// level per spine step from the apex down to the focus, each descending into
// the previous level's fold ANCHOR (not the next numeric prefix), and stops
// at the level whose folded run carries the focus.
func TestSpinePagesDeepFocus(t *testing.T) {
	s := newStore(t)
	// focus = …99.5.2 (a leaf). Structure:
	//   enterprises → 99 (99 has children 5 and 7 → a branch, the apex anchor)
	//   99.5 has children .2 (focus) and .9 (branch)
	//   99.7 has one child .1
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "focus", "1.3.6.1.4.1.99.5.2"),
		oidSym("M", "sib", "1.3.6.1.4.1.99.5.9"),
		oidSym("M", "seven", "1.3.6.1.4.1.99.7.1"),
	})
	ctx := context.Background()

	levels, err := s.SpinePages(ctx, "1.3.6.1.4.1.99.5.2", false, "", 200)
	if err != nil {
		t.Fatalf("SpinePages: %v", err)
	}
	wantParents := []string{"", "1.3.6.1.4.1.99", "1.3.6.1.4.1.99.5"}
	if got := spineParents(levels); len(got) != 3 ||
		got[0] != wantParents[0] || got[1] != wantParents[1] || got[2] != wantParents[2] {
		t.Fatalf("spine parents = %v, want %v", got, wantParents)
	}

	// Apex level folds iso…enterprises.99 into one row anchored at 99.
	if n := len(levels[0].Rows); n != 1 {
		t.Fatalf("apex level = %d rows, want 1 (folded spine)", n)
	}
	if a := levels[0].Rows[0].Anchor().OID; a != "1.3.6.1.4.1.99" {
		t.Errorf("apex row anchor = %q, want the enterprises.99 fold", a)
	}
	// Middle level: 99's children are 5 and 7.
	if len(levels[1].Rows) != 2 || levels[1].Rows[0].Seg != 5 || levels[1].Rows[1].Seg != 7 {
		t.Errorf("level under 99 segs = %+v, want [5 7]", levels[1].Rows)
	}
	// Deepest level: 99.5's children are 2 (focus) and 9; the walk stops here
	// (a row already carries the focus) — no level for the focus's own children.
	if len(levels[2].Rows) != 2 || levels[2].Rows[0].Seg != 2 || levels[2].Rows[1].Seg != 9 {
		t.Errorf("level under 99.5 segs = %+v, want [2 9]", levels[2].Rows)
	}
	for i, lv := range levels {
		if lv.Anchored {
			t.Errorf("level %d unexpectedly Anchored (narrow levels need no 'show earlier')", i)
		}
	}
}

// TestSpinePagesBranchesStopsAtContainer pins the hidden-leaf case: in
// branchesOnly mode a focus that is a leaf column is not itself a spine
// level; the walk descends to the deepest CONTAINER that holds it (the
// table-entry) and stops, so the client highlights that container.
func TestSpinePagesBranchesStopsAtContainer(t *testing.T) {
	s := newStore(t)
	// agent 99.1 has scalars (.1/.2, leaves) and a table (.4 → entry .4.1 →
	// columns .1/.2). focus is a column (leaf, hidden in branchesOnly).
	seedAndBuild(t, s, "M", []model.Symbol{
		kindSym("M", "s1", "1.3.6.1.4.1.99.1.1", model.KindScalar),
		kindSym("M", "s2", "1.3.6.1.4.1.99.1.2", model.KindScalar),
		kindSym("M", "tbl", "1.3.6.1.4.1.99.1.4", model.KindTable),
		kindSym("M", "entry", "1.3.6.1.4.1.99.1.4.1", model.KindTableEntry),
		kindSym("M", "col1", "1.3.6.1.4.1.99.1.4.1.1", model.KindColumn),
		kindSym("M", "col2", "1.3.6.1.4.1.99.1.4.1.2", model.KindColumn),
	})
	ctx := context.Background()

	levels, err := s.SpinePages(ctx, "1.3.6.1.4.1.99.1.4.1.1", true, "", 200)
	if err != nil {
		t.Fatalf("SpinePages: %v", err)
	}
	// Two levels: the apex fold (anchored at the agent 99.1) and the agent's
	// container children. No level parented at the entry or the hidden column.
	if got := spineParents(levels); len(got) != 2 || got[0] != "" || got[1] != "1.3.6.1.4.1.99.1" {
		t.Fatalf("spine parents = %v, want [\"\" \"1.3.6.1.4.1.99.1\"]", got)
	}
	// The deepest level's spine row is the table folded to its entry, and the
	// entry is a non-expandable tree leaf (its children are hidden columns).
	var entry *FoldedNodeRow
	for i := range levels[1].Rows {
		if levels[1].Rows[i].Anchor().OID == "1.3.6.1.4.1.99.1.4.1" {
			entry = &levels[1].Rows[i]
		}
	}
	if entry == nil {
		t.Fatalf("deepest level missing the folded table-entry row: %+v", levels[1].Rows)
	}
	if entry.HasChildren() {
		t.Error("table-entry should be a non-expandable tree leaf (children are hidden columns)")
	}
}

// TestSpinePagesWideLevelAnchored pins that a wide level whose spine child is
// beyond the first page is re-fetched anchored at the child and flagged
// Anchored (the client renders "show earlier").
func TestSpinePagesWideLevelAnchored(t *testing.T) {
	s := newStore(t)
	// 99 has five branch children 1..5 (each with a child), so it never folds
	// past 99. focus goes through the fifth — beyond a 2-row first page.
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "c1", "1.3.6.1.4.1.99.1.1"),
		oidSym("M", "c2", "1.3.6.1.4.1.99.2.1"),
		oidSym("M", "c3", "1.3.6.1.4.1.99.3.1"),
		oidSym("M", "c4", "1.3.6.1.4.1.99.4.1"),
		oidSym("M", "c5", "1.3.6.1.4.1.99.5.1"),
	})
	ctx := context.Background()

	levels, err := s.SpinePages(ctx, "1.3.6.1.4.1.99.5.1", false, "", 2)
	if err != nil {
		t.Fatalf("SpinePages: %v", err)
	}
	// The level under 99 is anchored at seg 5 (past the first 2-row page).
	var under99 *SpineLevel
	for i := range levels {
		if levels[i].Parent == "1.3.6.1.4.1.99" {
			under99 = &levels[i]
		}
	}
	if under99 == nil {
		t.Fatalf("no level parented at 99: parents=%v", spineParents(levels))
	}
	if !under99.Anchored {
		t.Error("wide level under 99 should be Anchored (spine child past the first page)")
	}
	if findRowBySeg(under99.Rows, 5) == nil {
		t.Errorf("anchored level missing the spine child (seg 5): %+v", under99.Rows)
	}
}

// TestSpinePagesUnknownFocus pins the fallback: a focus that does not resolve
// to a node returns only as far as the trie reaches (here the apex), never
// erroring — the client then highlights the deepest rendered ancestor.
func TestSpinePagesUnknownFocus(t *testing.T) {
	s := newStore(t)
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "real", "1.3.6.1.4.1.99"),
	})
	ctx := context.Background()

	levels, err := s.SpinePages(ctx, "9.9.9", false, "", 200)
	if err != nil {
		t.Fatalf("SpinePages: %v", err)
	}
	// The 9 arc doesn't exist; only the apex level (iso) comes back.
	if got := spineParents(levels); len(got) != 1 || got[0] != "" {
		t.Fatalf("unknown-focus spine parents = %v, want [\"\"] (apex only)", got)
	}
	if findRowBySeg(levels[0].Rows, 9) != nil {
		t.Error("apex level should not contain a seg-9 row (no such arc)")
	}
}

// TestOIDTreeGeneration pins that the generation token is 0 before any
// build and advances on EVERY rebuild — including a content-only rebuild
// that leaves the schema version constant. It is also anchored to
// wall-clock seconds so a wiped/recreated DB cannot land on a previous
// epoch's value (the cross-DB half of the ETag-validity contract).
func TestOIDTreeGeneration(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if g, err := s.OIDTreeGeneration(ctx); err != nil || g != 0 {
		t.Fatalf("generation before build = (%d, %v), want (0, nil)", g, err)
	}
	start := time.Now().UnixNano()
	seedAndBuild(t, s, "M", []model.Symbol{oidSym("M", "real", "1.3.6.1.4.1.99")})
	g1, err := s.OIDTreeGeneration(ctx)
	if err != nil {
		t.Fatalf("OIDTreeGeneration: %v", err)
	}
	// Wall-clock anchoring at nanosecond resolution: a fresh DB's first
	// generation is the current epoch instant, not a restarted counter.
	if g1 < start {
		t.Fatalf("generation after first build = %d, want >= %d (wall-clock anchored)", g1, start)
	}
	// A second rebuild (content changed, schema version unchanged) must bump
	// the generation — the case a version-only token would miss.
	seedAndBuild(t, s, "N", []model.Symbol{oidSym("N", "more", "1.3.6.1.4.1.99.7")})
	g2, err := s.OIDTreeGeneration(ctx)
	if err != nil {
		t.Fatalf("OIDTreeGeneration: %v", err)
	}
	if g2 <= g1 {
		t.Errorf("generation after content rebuild = %d, want > %d", g2, g1)
	}
}

// TestMigrateStampOIDTreeGeneration pins the upgrade path: a DB whose trie
// predates the generation token (oid_tree_version present, generation key
// absent — simulated by stripping the key) gets the token stamped by the
// one-row migration at Open, WITHOUT a trie rebuild, so pre-upgrade DBs
// never serve the shared zero generation and never pay a full rebuild for
// a token-only upgrade.
func TestMigrateStampOIDTreeGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	// Build a trie, then strip the generation key — the exact schema_meta
	// state of a DB built before the token existed.
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedAndBuild(t, s, "M", []model.Symbol{oidSym("M", "real", "1.3.6.1.4.1.99")})
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM schema_meta WHERE key = 'oid_tree_generation'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen: the migration must stamp a wall-clock token and leave the
	// trie alone (still current — no rebuild owed).
	start := time.Now().UnixNano()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	gen, err := s2.OIDTreeGeneration(ctx)
	if err != nil {
		t.Fatalf("OIDTreeGeneration: %v", err)
	}
	if gen < start {
		t.Errorf("migrated generation = %d, want >= %d (wall-clock stamp)", gen, start)
	}
	if stale, err := s2.OIDTreeStale(ctx); err != nil || stale {
		t.Errorf("trie after stamp migration stale = (%v, %v), want (false, nil) — token-only upgrade must not force a rebuild", stale, err)
	}
	// The trie content survived untouched.
	if _, _, _, _, ok := node(t, s2, "1.3.6.1.4.1.99"); !ok {
		t.Error("trie content missing after the stamp migration — it must not clear oid_node")
	}
}

// TestMigrateStampOIDTreeGenerationSkipsFreshDB pins the other half: a DB
// with no trie yet (no oid_tree_version marker) is NOT stamped at Open —
// the generation stays 0, treeETag serves uncacheable responses, and the
// first RebuildOIDTree writes the real token.
func TestMigrateStampOIDTreeGenerationSkipsFreshDB(t *testing.T) {
	s := newStore(t)
	if g, err := s.OIDTreeGeneration(context.Background()); err != nil || g != 0 {
		t.Fatalf("fresh DB generation = (%d, %v), want (0, nil) — nothing to stamp before the first build", g, err)
	}
}

// TestOpenStampedDBIsWriteFree pins that the steady-state Open (version and
// generation both present) executes NO write statement: a read-only DB file
// must open fine. Regression guard for the stamp migration — even a
// conflicting no-op INSERT acquires SQLite's write lock, which failed Open
// on read-only media and blocked every Open behind a concurrent writer
// (fatal for blittermib-mcp, which opens the live server DB per session).
func TestOpenStampedDBIsWriteFree(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seedAndBuild(t, s, "M", []model.Symbol{oidSym("M", "real", "1.3.6.1.4.1.99")})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Make the DB (and any WAL sidecars) read-only; a write-free Open must
	// still succeed and read the stamped generation.
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(f); err == nil {
			if err := os.Chmod(f, 0o444); err != nil {
				t.Fatalf("chmod %s: %v", f, err)
			}
			t.Cleanup(func() { _ = os.Chmod(f, 0o644) })
		}
	}

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open on read-only stamped DB failed — steady-state Open must be write-free: %v", err)
	}
	defer func() { _ = s2.Close() }()
	gen, err := s2.OIDTreeGeneration(ctx)
	if err != nil {
		t.Fatalf("OIDTreeGeneration: %v", err)
	}
	if gen <= 0 {
		t.Errorf("generation on read-only stamped DB = %d, want > 0", gen)
	}
}

// TestRebuildOIDTreeOmitsNullSentinel verifies the { 0 0 } null-identifier
// sentinel (zeroDotZero) is excluded from the trie — neither 0.0 nor its
// otherwise-childless synthetic 0 root is materialised — while a normal
// symbol at another OID is still projected.
func TestRebuildOIDTreeOmitsNullSentinel(t *testing.T) {
	s := newStore(t)
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "zeroDotZero", "0.0"),
		oidSym("M", "real", "1.3.6.1.4.1.99"),
	})

	if _, _, _, _, ok := node(t, s, "0.0"); ok {
		t.Error("oid_node has a row for 0.0; the null sentinel must be omitted")
	}
	if _, _, _, _, ok := node(t, s, "0"); ok {
		t.Error("oid_node has a row for the synthetic 0 root; it must be dropped once childless")
	}
	if _, _, _, _, ok := node(t, s, "1.3.6.1.4.1.99"); !ok {
		t.Error("oid_node missing the normal 1.3.6.1.4.1.99 node; only the sentinel should be omitted")
	}
}

// TestNullSentinelSymbolStillResolvable confirms omission is tree-only: the
// sentinel is absent from oid_node yet its symbol row is untouched and still
// resolves by OID. Asserting BOTH halves is what makes this guard the
// feature — a symbol-only check would pass even if the trie kept 0.0.
func TestNullSentinelSymbolStillResolvable(t *testing.T) {
	s := newStore(t)
	seedAndBuild(t, s, "SNMPv2-SMI", []model.Symbol{
		oidSym("SNMPv2-SMI", "zeroDotZero", "0.0"),
	})

	// Off-tree: no oid_node row for the sentinel.
	if _, _, _, _, ok := node(t, s, "0.0"); ok {
		t.Error("oid_node has a row for 0.0; the sentinel must be omitted from the tree")
	}
	// ...but still resolvable via the symbol table.
	sym, err := s.GetSymbolByOID(context.Background(), "0.0")
	if err != nil {
		t.Fatalf("GetSymbolByOID(0.0): %v", err)
	}
	if sym == nil || sym.Name != "zeroDotZero" {
		t.Errorf("GetSymbolByOID(0.0) = %v, want zeroDotZero (tree omission must not delete the symbol)", sym)
	}
}

// TestNullSentinelWithDescendantIsUnnamedBridge locks the semantics when a
// corpus defines a symbol UNDER 0.0: the recursive prefix step must re-create
// 0.0 (and its 0 root) so the descendant is reachable, but 0.0 must be an
// unnamed synthetic bridge — the winner CTE excludes 0.0, so the zeroDotZero
// sentinel is never re-attached even though its symbol row exists.
func TestNullSentinelWithDescendantIsUnnamedBridge(t *testing.T) {
	s := newStore(t)
	seedAndBuild(t, s, "M", []model.Symbol{
		oidSym("M", "zeroDotZero", "0.0"),
		oidSym("M", "underSentinel", "0.0.5"),
	})

	// The descendant is reachable, so 0.0 and 0 exist as bridges...
	hasSym, _, name, _, ok := node(t, s, "0.0")
	if !ok {
		t.Fatal("oid_node missing 0.0; it must exist as a bridge to reach 0.0.5")
	}
	if hasSym || name != "" {
		t.Errorf("0.0 bridge = (has_symbol=%v, name=%q); want an unnamed synthetic node, not the zeroDotZero sentinel", hasSym, name)
	}
	if _, _, _, _, ok := node(t, s, "0"); !ok {
		t.Error("oid_node missing the 0 root; it must exist as a bridge above 0.0")
	}
	if _, _, _, _, ok := node(t, s, "0.0.5"); !ok {
		t.Error("oid_node missing the 0.0.5 descendant")
	}
}
