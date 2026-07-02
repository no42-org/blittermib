/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/no42-org/blittermib/internal/model"
)

// oidTreeVersion gates the one-time (re)build of the oid_node trie. Bump
// it whenever the trie's shape or column semantics change so an
// already-built DB rebuilds on the next boot instead of serving a stale
// generation. Stored under schema_meta('oid_tree_version'), independent
// of the PRAGMA user_version owned by the relationship backfill.
//
// v3: the trie omits the null-identifier sentinel { 0 0 } (see the
// prefixes seed in RebuildOIDTree), so a v2 DB rebuilds to drop it.
const oidTreeVersion = 3

// NodeRow is one node of the materialised OID trie, as served to the
// tree browser. HasChildren is derived from child_count; HasSymbol is
// false for synthetic bridge nodes (an intermediate prefix with no
// symbol of its own), whose Name/Module/Kind are empty and whose
// display name the caller resolves via the IANA canonical registry.
type NodeRow struct {
	OID         string
	ParentOID   string
	Label       string
	Seg         int64
	Name        string
	ModuleName  string
	Kind        model.SymbolKind
	HasSymbol   bool
	HasChildren bool
}

// OIDTreeGeneration returns a monotonic token advanced by every
// RebuildOIDTree — i.e. whenever the trie's CONTENT is (re)materialised,
// including content-only rebuilds after an import or hot reload. Unlike
// oidTreeVersion (a fixed schema-shape marker, re-stamped unchanged on a
// content rebuild), it changes whenever the browsable data changes, so it
// is the correct cache-validity token for the tree endpoints (an ETag
// component). The value is anchored to wall-clock seconds (see the bump in
// RebuildOIDTree), so it also differs ACROSS database files: a wiped and
// re-imported DB cannot land on a previous epoch's value and re-issue an
// old ETag for different content. 0 when the trie has never been built.
func (s *Store) OIDTreeGeneration(ctx context.Context) (int64, error) {
	var gen int64
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM schema_meta WHERE key = 'oid_tree_generation'`).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read oid tree generation: %w", err)
	}
	return gen, nil
}

// OIDTreeStale reports whether oid_node needs a (re)build because its
// recorded version predates oidTreeVersion (or was never recorded).
func (s *Store) OIDTreeStale(ctx context.Context) (bool, error) {
	var ver int
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM schema_meta WHERE key = 'oid_tree_version'`).Scan(&ver)
	if errors.Is(err, sql.ErrNoRows) {
		// No marker yet: the trie was never built — stale.
		return true, nil
	}
	if err != nil {
		// A real read error (cancellation, I/O, lock) — surface it
		// rather than masking it as "stale" and triggering a rebuild
		// that would only re-hit the same fault.
		return false, fmt.Errorf("read oid tree version: %w", err)
	}
	return ver < oidTreeVersion, nil
}

// RebuildOIDTree rebuilds the oid_node trie from `symbol`. It is a global
// projection (synthetic bridges depend on cross-module descendants), so
// it cannot be maintained per-module inside ReplaceModule the way the
// relationship tables are — it is run once at boot when stale, once after
// a bulk import, and (debounced) after a hot reload.
//
// The whole rebuild runs in one transaction so readers see the previous
// generation until it commits. With MaxOpenConns(1) the write serialises
// with reads, so at full corpus scale (~1.9M nodes) it briefly stalls
// other queries for the duration (~14s measured) — acceptable for the
// boot/import paths; the browse tree is intentionally eventually
// consistent (symbol-backed views read `symbol` directly and stay fresh).
func (s *Store) RebuildOIDTree(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rebuild oid tree: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM oid_node`); err != nil {
		return fmt.Errorf("rebuild oid tree: clear: %w", err)
	}

	// Build the trie in one INSERT: every distinct OID and all of its
	// prefixes. The recursive UNION dedups prefixes; `pp` attaches each
	// node's parent (its OID minus the last segment); `winner` the
	// dedup-winner symbol — exactly one per OID via ROW_NUMBER (a module
	// may define the same OID under two names, so module_name alone is
	// not unique); `cc` the child count per parent (a single grouped
	// scan, joined in, rather than a correlated subquery per row). label
	// is the last segment text; seg is it as an integer for numeric
	// sibling ordering. has_symbol is 0 for prefixes with no winner.
	// has_scalar/has_table/has_notif are SEEDED here from each node's own
	// dedup-winner kind (the leaf's family — scalar+column, table+entry,
	// notification-type); the propagation loop below rolls them up so a
	// node's flag also reflects any descendant. Synthetic nodes (no winner
	// kind) seed 0 and gain flags only from descendants.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oid_node(oid, parent_oid, label, seg, name, module_name, kind, has_symbol, child_count,
		                     has_scalar, has_table, has_notif)
		WITH RECURSIVE prefixes(p) AS (
			-- Omit the SNMP null-identifier sentinel { 0 0 } (zeroDotZero):
			-- a placeholder value, not a browsable object, and the only
			-- descendant of the ccitt(0) arc in the corpus — excluding it
			-- here drops both 0.0 and its now-childless synthetic 0 root.
			-- The symbol row itself is untouched (still resolvable by OID/name).
			SELECT DISTINCT oid FROM symbol WHERE oid <> '' AND oid <> '0.0'
			UNION
			SELECT rtrim(rtrim(p, '0123456789'), '.') FROM prefixes WHERE p LIKE '%.%'
		),
		pp AS (
			SELECT p, rtrim(rtrim(p, '0123456789'), '.') AS parent
			FROM prefixes WHERE p <> ''
		),
		winner AS (
			-- 0.0 excluded here too (not just the seed): if a corpus ever
			-- defines a descendant under 0.0, the recursive step re-creates
			-- the 0.0 prefix as a bridge to reach it — but it must stay an
			-- unnamed synthetic node, never re-attach the zeroDotZero sentinel.
			SELECT oid, name, module_name, kind FROM (
				SELECT oid, name, module_name, kind,
				       ROW_NUMBER() OVER (PARTITION BY oid ORDER BY module_name, name) AS rn
				FROM symbol WHERE oid <> '' AND oid <> '0.0'
			) WHERE rn = 1
		),
		cc AS (
			SELECT parent AS poid, COUNT(*) AS n FROM pp GROUP BY parent
		)
		SELECT pp.p,
		       pp.parent,
		       CASE WHEN pp.parent = '' THEN pp.p ELSE substr(pp.p, length(pp.parent) + 2) END,
		       CAST(CASE WHEN pp.parent = '' THEN pp.p ELSE substr(pp.p, length(pp.parent) + 2) END AS INTEGER),
		       COALESCE(w.name, ''), COALESCE(w.module_name, ''), COALESCE(w.kind, ''),
		       (w.oid IS NOT NULL),
		       COALESCE(cc.n, 0),
		       (COALESCE(w.kind, '') IN ('scalar', 'column')),
		       (COALESCE(w.kind, '') IN ('table', 'table-entry')),
		       (COALESCE(w.kind, '') = 'notification-type')
		FROM pp
		LEFT JOIN winner w ON w.oid = pp.p
		LEFT JOIN cc ON cc.poid = pp.p`); err != nil {
		return fmt.Errorf("rebuild oid tree: insert nodes: %w", err)
	}

	// Propagate the family flags bottom-up: each pass ORs every parent's
	// flags with the MAX of its DIRECT children's flags, so after `depth`
	// passes a deep leaf's family has reached the apex. Monotonic (bits
	// only flip 0→1), so it terminates; the loop stops the moment a pass
	// changes nothing. The derived table `c` is materialised, so reading
	// oid_node while updating it is safe. Bounded by OID depth; the cap is
	// a backstop.
	for i := 0; i < 64; i++ {
		res, err := tx.ExecContext(ctx, `
			UPDATE oid_node AS p
			SET has_scalar = MAX(p.has_scalar, c.hs),
			    has_table  = MAX(p.has_table,  c.ht),
			    has_notif  = MAX(p.has_notif,  c.hn)
			FROM (
				SELECT parent_oid AS poid,
				       MAX(has_scalar) AS hs, MAX(has_table) AS ht, MAX(has_notif) AS hn
				FROM oid_node WHERE parent_oid <> '' GROUP BY parent_oid
			) AS c
			WHERE p.oid = c.poid
			  AND (p.has_scalar < c.hs OR p.has_table < c.ht OR p.has_notif < c.hn)`)
		if err != nil {
			return fmt.Errorf("rebuild oid tree: propagate family flags: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rebuild oid tree: propagate rows: %w", err)
		}
		if n == 0 {
			break
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_meta(key, value) VALUES ('oid_tree_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, oidTreeVersion); err != nil {
		return fmt.Errorf("rebuild oid tree: mark version: %w", err)
	}

	// Advance a monotonic generation on every rebuild so the tree-API cache
	// validator changes whenever the trie's CONTENT changes. The version
	// above is a fixed shape marker (re-stamped unchanged on a content-only
	// rebuild), so it cannot serve that role — an import or hot reload
	// rewrites oid_node while leaving the version at its constant.
	//
	// The value is MAX(previous+1, now): anchoring to wall-clock seconds
	// makes the token unique across DATABASE FILES too — a plain counter
	// restarts at 1 in a wiped/recreated DB, so two DB lifetimes with equal
	// rebuild counts would re-issue byte-identical ETags for different
	// content and conditional requests would false-304 to a stale tree.
	// previous+1 keeps it strictly advancing even under clock skew or two
	// rebuilds within the same second.
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_meta(key, value) VALUES ('oid_tree_generation', ?)
		ON CONFLICT(key) DO UPDATE SET value = MAX(CAST(value AS INTEGER) + 1, ?)`, now, now); err != nil {
		return fmt.Errorf("rebuild oid tree: bump generation: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rebuild oid tree: commit: %w", err)
	}
	return nil
}

// ListNodeChildren returns up to `limit` children of `parentOID` from the
// materialised trie, ordered numerically by segment, starting after the
// `afterSeg` cursor (pass a negative value for the first page). One
// indexed query, no per-child probe: HasChildren comes from child_count.
//
// Children of a node share its parent and have distinct last segments, so
// `seg` is unique within the result and the keyset uses a strict `seg > ?`
// with no tiebreak. `limit <= 0` falls back to a sane default.
func (s *Store) ListNodeChildren(ctx context.Context, parentOID string, afterSeg int64, limit int) ([]NodeRow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT oid, parent_oid, label, seg, name, module_name, kind,
		       has_symbol, child_count > 0
		FROM oid_node
		WHERE parent_oid = ? AND seg > ?
		ORDER BY seg
		LIMIT ?`, parentOID, afterSeg, limit)
	if err != nil {
		return nil, fmt.Errorf("list node children of %s: %w", parentOID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []NodeRow
	for rows.Next() {
		var n NodeRow
		var kind string
		if err := rows.Scan(&n.OID, &n.ParentOID, &n.Label, &n.Seg,
			&n.Name, &n.ModuleName, &kind, &n.HasSymbol, &n.HasChildren); err != nil {
			return nil, fmt.Errorf("scan node child of %s: %w", parentOID, err)
		}
		n.Kind = model.SymbolKind(kind)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node children of %s: %w", parentOID, err)
	}
	return out, nil
}

// FoldSeg is one node on a path-compressed chain: enough for the caller
// to render the dotted seg/name path and resolve a synthetic name.
// HasSymbol distinguishes a symbol-backed node from a synthetic bridge.
type FoldSeg struct {
	OID       string
	Label     string // last OID segment as text, e.g. "6"
	Name      string // stored symbol name; "" for a synthetic bridge
	HasSymbol bool
}

// FoldedNodeRow is one tree row after path compression: a maximal run of
// single-child nodes collapsed to its deepest node (the *anchor*). Chain
// runs from the requested parent's direct child down to the anchor
// (length 1 when nothing folded). Seg is the DIRECT child's segment — the
// keyset cursor key under the parent, NOT the anchor's. ModuleName, Kind,
// and ChildCount describe the anchor; OID/Name/HasSymbol of the anchor are
// Chain's last element.
type FoldedNodeRow struct {
	Seg        int64
	Chain      []FoldSeg
	ModuleName string
	Kind       model.SymbolKind
	ChildCount int64
	// Expandable reports whether the anchor has a child the TREE will
	// render. In the default mode that is "has any child" (child_count>0);
	// in branches-only mode (the workspace tree, which hides leaf objects)
	// it is "has a child that is itself a container", so a table-entry
	// whose only children are leaf columns is a non-expandable tree leaf.
	Expandable bool
}

// DirectOID is the direct child's OID — the keyset cursor value under the
// requested parent (its last segment is Seg).
func (f FoldedNodeRow) DirectOID() string { return f.Chain[0].OID }

// Anchor is the deepest node of the run — the one that owns the row's
// child-count, expansion, and symbol link.
func (f FoldedNodeRow) Anchor() FoldSeg { return f.Chain[len(f.Chain)-1] }

// HasChildren reports whether the anchor is expandable in the tree.
func (f FoldedNodeRow) HasChildren() bool { return f.Expandable }

// scanFolded groups the depth-ordered fold rows into one FoldedNodeRow per
// direct child (per dseg). Rows arrive ordered by (dseg, depth), so each
// group is contiguous and its deepest row (last seen) is the anchor. The
// trailing `has_branch` column carries the anchor's expandability.
func scanFolded(rows *sql.Rows, parentOID string) ([]FoldedNodeRow, error) {
	var out []FoldedNodeRow
	curSeg := int64(-1)
	have := false
	for rows.Next() {
		var dseg, depth, cc int64
		var oid, label, name, mod, kind string
		var hs, hasBranch bool
		if err := rows.Scan(&dseg, &depth, &oid, &label, &name, &mod, &kind, &hs, &cc, &hasBranch); err != nil {
			return nil, fmt.Errorf("scan folded child of %s: %w", parentOID, err)
		}
		seg := FoldSeg{OID: oid, Label: label, Name: name, HasSymbol: hs}
		if !have || dseg != curSeg {
			out = append(out, FoldedNodeRow{
				Seg: dseg, Chain: []FoldSeg{seg},
				ModuleName: mod, Kind: model.SymbolKind(kind), ChildCount: cc, Expandable: hasBranch,
			})
			curSeg, have = dseg, true
			continue
		}
		last := &out[len(out)-1]
		last.Chain = append(last.Chain, seg)
		// Deeper row → it is the new anchor; carry its anchor attributes.
		last.ModuleName, last.Kind, last.ChildCount, last.Expandable = mod, model.SymbolKind(kind), cc, hasBranch
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate folded children of %s: %w", parentOID, err)
	}
	return out, nil
}

// foldChildren runs the path-compressing read for one keyset page of a
// parent's direct children. `cmp`/`order` select the direction — (`>`,
// `ASC`) forward, (`<`, `DESC`) backward — compile-time constants, never
// caller input, so the concatenation carries no injection.
//
// Default mode: the recursion walks the chain while the current node has
// exactly one child (`f.child_count = 1`), absorbing it — so `mgmt` folds
// into branch `mib-2` (`.2.1 mgmt.mib-2`) and a terminal leaf is absorbed.
// `has_branch` = child_count>0.
//
// branchesOnly mode (the workspace "container map"): leaf objects
// (child_count=0) are filtered from the page AND never absorbed into a run
// (`c.child_count > 0`), so a chain stops at the deepest CONTAINER (a
// table-entry shows, its leaf columns do not). `has_branch` then means
// "has a container child" — a table-entry whose children are all columns
// is a non-expandable tree leaf.
func (s *Store) foldChildren(ctx context.Context, parentOID, cmp, order string, bound int64, limit int, branchesOnly bool, family string) ([]FoldedNodeRow, error) {
	if limit <= 0 {
		limit = 200
	}
	pageFilter := ""
	hasBranchExpr := "fold.child_count > 0"
	// recurseJoin/recurseWhere define when a node folds into a child.
	// Default: fold while the node has exactly one child (absorb it).
	recurseJoin := "JOIN oid_node c ON c.parent_oid = f.oid"
	recurseWhere := "f.child_count = 1"
	if branchesOnly {
		pageFilter = " AND child_count > 0"
		// Container map: only fold into CONTAINER children (never absorb a
		// leaf), so a chain stops at the deepest container.
		recurseWhere = "f.child_count = 1 AND c.child_count > 0"
		hasBranchExpr = "EXISTS(SELECT 1 FROM oid_node g WHERE g.parent_oid = fold.oid AND g.child_count > 0)"
		// Kind-chip family filter (only with branchesOnly): show a
		// container iff its subtree contains the family, and expand it only
		// to children that ALSO match — else a chevron opens an empty level.
		// familyColumn maps to a fixed column name (never raw input).
		if col := familyColumn(family); col != "" {
			pageFilter += " AND " + col + " = 1"
			hasBranchExpr = "EXISTS(SELECT 1 FROM oid_node g WHERE g.parent_oid = fold.oid AND g.child_count > 0 AND g." + col + " = 1)"
			// Fold on the FILTERED tree: collapse a node into its child when
			// the node has exactly ONE family-matching child AND that child
			// is a container — so isnsMIB (3 children, only isnsNotifications
			// notif-bearing) folds to `.163.0 isnsMIB.isnsNotifications`
			// instead of showing two rows. A node with a single matching
			// LEAF (count 1, no container) correctly does NOT fold — that
			// leaf is its own direct object, reachable by clicking the node.
			recurseJoin = "JOIN oid_node c ON c.parent_oid = f.oid AND c.child_count > 0 AND c." + col + " = 1"
			recurseWhere = "(SELECT COUNT(*) FROM oid_node g WHERE g.parent_oid = f.oid AND g." + col + " = 1) = 1"
		}
	}
	// #nosec G202 -- no injection: cmp/order are compile-time direction
	// constants; pageFilter/hasBranchExpr/recurseJoin/recurseWhere are built
	// only from string literals; col comes from familyColumn's fixed column
	// whitelist (has_scalar/has_table/has_notif); every runtime value
	// (parentOID/bound/limit) is bound via a ? placeholder below.
	q := `
		WITH RECURSIVE page AS (
			SELECT oid, label, seg, name, module_name, kind, has_symbol, child_count
			FROM oid_node
			WHERE parent_oid = ? AND seg ` + cmp + ` ?` + pageFilter + `
			ORDER BY seg ` + order + ` LIMIT ?
		),
		fold(dseg, depth, oid, label, name, module_name, kind, has_symbol, child_count) AS (
			SELECT seg, 0, oid, label, name, module_name, kind, has_symbol, child_count FROM page
			UNION ALL
			SELECT f.dseg, f.depth + 1, c.oid, c.label, c.name, c.module_name, c.kind, c.has_symbol, c.child_count
			FROM fold f
			` + recurseJoin + `
			WHERE ` + recurseWhere + `
		)
		SELECT dseg, depth, oid, label, name, module_name, kind, has_symbol, child_count, ` + hasBranchExpr + `
		FROM fold ORDER BY dseg, depth`
	rows, err := s.db.QueryContext(ctx, q, parentOID, bound, limit)
	if err != nil {
		return nil, fmt.Errorf("list folded children of %s: %w", parentOID, err)
	}
	defer func() { _ = rows.Close() }()
	return scanFolded(rows, parentOID)
}

// familyColumn maps a kind-chip family ("scalar"|"table"|"notif") to its
// oid_node subtree-flag COLUMN name, or "" for any other value (no filter).
// The mapping is a fixed allow-list — the result is concatenated into SQL,
// so it must never echo raw input.
func familyColumn(family string) string {
	switch family {
	case "scalar":
		return "has_scalar"
	case "table":
		return "has_table"
	case "notif":
		return "has_notif"
	default:
		return ""
	}
}

// ListNodeChildrenFolded is ListNodeChildren with path compression: each
// returned row is one direct child of `parentOID`, with its single-child
// run folded into the row's Chain (see FoldedNodeRow). The keyset (numeric
// order, `afterSeg` cursor, `limit`) operates on the DIRECT children
// exactly as ListNodeChildren does — folding never changes the number of
// rows in a level, only how deep each row reaches. `branchesOnly` hides
// leaf objects (the workspace container map); `family` (only with
// branchesOnly) further prunes to containers whose subtree holds that
// kind-chip family (see foldChildren).
func (s *Store) ListNodeChildrenFolded(ctx context.Context, parentOID string, afterSeg int64, limit int, branchesOnly bool, family string) ([]FoldedNodeRow, error) {
	return s.foldChildren(ctx, parentOID, ">", "ASC", afterSeg, limit, branchesOnly, family)
}

// ListNodeChildrenFoldedBefore is the backward-paging counterpart of
// ListNodeChildrenFolded (the "show earlier" path): direct children with
// segment strictly below `beforeSeg`, returned in ascending segment order,
// each with its single-child run folded.
func (s *Store) ListNodeChildrenFoldedBefore(ctx context.Context, parentOID string, beforeSeg int64, limit int, branchesOnly bool, family string) ([]FoldedNodeRow, error) {
	return s.foldChildren(ctx, parentOID, "<", "DESC", beforeSeg, limit, branchesOnly, family)
}

// SpineLevel is one level of a preloaded OID-tree spine: the folded
// children of Parent, exactly as ListNodeChildrenFolded would return them
// for a single lazy expansion. Anchored is true when the page was fetched
// anchored at the spine child (a wide level where earlier siblings were
// skipped) — the caller renders a "show earlier" affordance for it, matching
// the lazy loadAnchored path.
type SpineLevel struct {
	Parent   string
	Rows     []FoldedNodeRow
	Anchored bool
}

// spineMaxDepth backstops the spine walk against a data anomaly; real OID
// depth in the corpus is well under this. Mirrors the client guard.
const spineMaxDepth = 64

// SpinePages returns every level from the apex down to `focus` in one call —
// the whole set of pages the client's expandSpineTo would otherwise fetch
// one round trip per level. It walks the same way that loop does: list a
// level's folded children, find the row whose run carries the next spine
// segment, and descend into that row's ANCHOR (folding means the next parent
// is the anchor OID, not the next numeric prefix). A wide level whose spine
// child is beyond the first page is re-fetched anchored at the child (the
// `loadAnchored` "jump to the child's page" behaviour), and the level is
// marked Anchored. branchesOnly/family carry through unchanged, so a
// preloaded spine renders identically to a hand-expanded one.
//
// The walk stops (and does not descend further) when the spine child is
// absent from the level (a filtered leaf, or an unknown focus — the level
// still renders as far as the trie reaches), when a folded row's run already
// carries focus, or when the deepest reachable container is a tree leaf. The
// highlight target is left to the caller/client (rowFor∥deepestAncestorRow
// over the rendered rows), so it stays a single source of truth.
func (s *Store) SpinePages(ctx context.Context, focus string, branchesOnly bool, family string, limit int) ([]SpineLevel, error) {
	if limit <= 0 {
		limit = 200
	}
	var levels []SpineLevel
	parent := ""
	for i := 0; i < spineMaxDepth; i++ {
		child := spineChild(parent, focus)
		if child == "" {
			// focus == parent (or not below it): focus is already rendered
			// as a row in the level just added — do not expand its children.
			break
		}
		rows, err := s.ListNodeChildrenFolded(ctx, parent, -1, limit, branchesOnly, family)
		if err != nil {
			return nil, err
		}
		cs := lastSegInt(child)
		row := findRowBySeg(rows, cs)
		anchored := false
		// Re-fetch anchored only when the first page was FULL and the child
		// wasn't on it — then the child may be on a later page (a wide level).
		// A short first page already enumerated every child, so a missing
		// child is genuinely absent; re-fetching would just repeat the scan.
		if row == nil && cs > 0 && len(rows) == limit {
			// Wide level: the spine child is past the first page. Re-fetch the
			// keyset page anchored at the child (so it is the first row) and
			// flag the level so the client offers "show earlier".
			rows2, err := s.ListNodeChildrenFolded(ctx, parent, cs-1, limit, branchesOnly, family)
			if err != nil {
				return nil, err
			}
			if r := findRowBySeg(rows2, cs); r != nil {
				rows, row, anchored = rows2, r, true
			}
		}
		levels = append(levels, SpineLevel{Parent: parent, Rows: rows, Anchored: anchored})
		if row == nil {
			break // spine child absent (filtered leaf / broke / unknown focus)
		}
		if rowCoversOID(*row, focus) {
			break // the folded run already carries focus — it is rendered here
		}
		if !row.Expandable {
			break // deepest reachable container (focus is a hidden leaf under it)
		}
		// The anchor is always strictly deeper than `parent` (the row's chain
		// starts at a direct child), so the walk provably advances each
		// iteration; spineMaxDepth above is the sole anomaly backstop.
		parent = row.Anchor().OID
	}
	return levels, nil
}

// spineChild returns the direct child of `parent` on the path to `focus` —
// `focus` truncated to one more segment than `parent`. Returns "" when
// `parent` is not a strict prefix of `focus` (including parent == focus).
func spineChild(parent, focus string) string {
	if parent == focus {
		return ""
	}
	var rest string
	if parent == "" {
		rest = focus
	} else {
		if !strings.HasPrefix(focus, parent+".") {
			return ""
		}
		rest = focus[len(parent)+1:]
	}
	seg := rest
	if i := strings.IndexByte(rest, '.'); i >= 0 {
		seg = rest[:i]
	}
	if seg == "" {
		return ""
	}
	if parent == "" {
		return seg
	}
	return parent + "." + seg
}

// lastSegInt parses an OID's trailing segment as an integer (-1 on failure),
// matching the keyset's numeric sibling ordering.
func lastSegInt(oid string) int64 {
	seg := oid
	if i := strings.LastIndexByte(oid, '.'); i >= 0 {
		seg = oid[i+1:]
	}
	n, err := strconv.ParseInt(seg, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// findRowBySeg returns the folded row for the direct child with segment
// `seg` (rows are keyed by their direct child's Seg, unique per level), or
// nil when no such row is in the page.
func findRowBySeg(rows []FoldedNodeRow, seg int64) *FoldedNodeRow {
	for i := range rows {
		if rows[i].Seg == seg {
			return &rows[i]
		}
	}
	return nil
}

// rowCoversOID reports whether a folded row's run spans `oid` — its direct
// child is an ancestor-or-equal of `oid` and its anchor is a
// descendant-or-equal. Mirrors tree.js rowCovers: true when the row already
// represents `oid` (it was folded into the run).
func rowCoversOID(row FoldedNodeRow, oid string) bool {
	return oidUnderPrefix(oid, row.DirectOID()) && oidUnderPrefix(row.Anchor().OID, oid)
}

// oidUnderPrefix reports whether `oid` is `prefix` or a descendant of it.
// NOTE: this mirrors web.OIDUnderPrefix (not imported — the view layer sits
// above the store) EXCEPT that an EMPTY prefix matches nothing here;
// rowCoversOID never passes one. Align the semantics before reusing this
// more broadly.
func oidUnderPrefix(oid, prefix string) bool {
	return oid == prefix || strings.HasPrefix(oid, prefix+".")
}

// ListNodeChildrenBefore returns up to `limit` children of `parentOID`
// with segment strictly LESS than `beforeSeg`, returned in ascending
// segment order (the page immediately preceding the cursor). It powers
// backward paging — the "show earlier" affordance for a level the spine
// opened anchored in the middle. The query is DESC-limited then reversed
// so it's the closest page below the cursor.
func (s *Store) ListNodeChildrenBefore(ctx context.Context, parentOID string, beforeSeg int64, limit int) ([]NodeRow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT oid, parent_oid, label, seg, name, module_name, kind,
		       has_symbol, child_count > 0
		FROM oid_node
		WHERE parent_oid = ? AND seg < ?
		ORDER BY seg DESC
		LIMIT ?`, parentOID, beforeSeg, limit)
	if err != nil {
		return nil, fmt.Errorf("list node children before %d of %s: %w", beforeSeg, parentOID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []NodeRow
	for rows.Next() {
		var n NodeRow
		var kind string
		if err := rows.Scan(&n.OID, &n.ParentOID, &n.Label, &n.Seg,
			&n.Name, &n.ModuleName, &kind, &n.HasSymbol, &n.HasChildren); err != nil {
			return nil, fmt.Errorf("scan node child of %s: %w", parentOID, err)
		}
		n.Kind = model.SymbolKind(kind)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node children of %s: %w", parentOID, err)
	}
	// Reverse to ascending so the caller can prepend the page as-is.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
