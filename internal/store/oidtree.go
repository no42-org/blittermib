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

	"github.com/no42-org/blittermib/internal/model"
)

// oidTreeVersion gates the one-time (re)build of the oid_node trie. Bump
// it whenever the trie's shape or column semantics change so an
// already-built DB rebuilds on the next boot instead of serving a stale
// generation. Stored under schema_meta('oid_tree_version'), independent
// of the PRAGMA user_version owned by the relationship backfill.
const oidTreeVersion = 1

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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oid_node(oid, parent_oid, label, seg, name, module_name, kind, has_symbol, child_count)
		WITH RECURSIVE prefixes(p) AS (
			SELECT DISTINCT oid FROM symbol WHERE oid <> ''
			UNION
			SELECT rtrim(rtrim(p, '0123456789'), '.') FROM prefixes WHERE p LIKE '%.%'
		),
		pp AS (
			SELECT p, rtrim(rtrim(p, '0123456789'), '.') AS parent
			FROM prefixes WHERE p <> ''
		),
		winner AS (
			SELECT oid, name, module_name, kind FROM (
				SELECT oid, name, module_name, kind,
				       ROW_NUMBER() OVER (PARTITION BY oid ORDER BY module_name, name) AS rn
				FROM symbol WHERE oid <> ''
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
		       COALESCE(cc.n, 0)
		FROM pp
		LEFT JOIN winner w ON w.oid = pp.p
		LEFT JOIN cc ON cc.poid = pp.p`); err != nil {
		return fmt.Errorf("rebuild oid tree: insert nodes: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_meta(key, value) VALUES ('oid_tree_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, oidTreeVersion); err != nil {
		return fmt.Errorf("rebuild oid tree: mark version: %w", err)
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
