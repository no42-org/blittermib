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
)

// SourceFile fingerprints one curated MIB file. Paths are relative
// to the corpus root so the database stays valid when the root moves
// (e.g. between -mibs locations or volume remounts).
type SourceFile struct {
	Path       string
	Size       int64
	MtimeNS    int64
	SHA256     string
	ModuleName string
}

// UpsertSourceFile records (or refreshes) a curated file's
// fingerprint after a successful compile+store.
func (s *Store) UpsertSourceFile(ctx context.Context, f SourceFile) error {
	if f.Path == "" {
		return errors.New("UpsertSourceFile: empty path")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO source_file (path, size, mtime_ns, sha256, module_name)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
		    size = excluded.size, mtime_ns = excluded.mtime_ns,
		    sha256 = excluded.sha256, module_name = excluded.module_name`,
		f.Path, f.Size, f.MtimeNS, f.SHA256, f.ModuleName)
	if err != nil {
		return fmt.Errorf("upsert source file %s: %w", f.Path, err)
	}
	return nil
}

// ListSourceFiles returns every fingerprint keyed by relative path.
func (s *Store) ListSourceFiles(ctx context.Context) (map[string]SourceFile, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, size, mtime_ns, sha256, module_name FROM source_file`)
	if err != nil {
		return nil, fmt.Errorf("list source files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]SourceFile)
	for rows.Next() {
		var f SourceFile
		if err := rows.Scan(&f.Path, &f.Size, &f.MtimeNS, &f.SHA256, &f.ModuleName); err != nil {
			return nil, err
		}
		out[f.Path] = f
	}
	return out, rows.Err()
}

// FindSourceFileBySHA returns the first curated file with the given
// content hash, or nil — the byte-identical duplicate check.
func (s *Store) FindSourceFileBySHA(ctx context.Context, sha string) (*SourceFile, error) {
	var f SourceFile
	err := s.db.QueryRowContext(ctx, `
		SELECT path, size, mtime_ns, sha256, module_name
		FROM source_file WHERE sha256 = ? ORDER BY path LIMIT 1`, sha).
		Scan(&f.Path, &f.Size, &f.MtimeNS, &f.SHA256, &f.ModuleName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find source by sha: %w", err)
	}
	return &f, nil
}

// DeleteSourceFile drops a fingerprint row (vanished or replaced
// source).
func (s *Store) DeleteSourceFile(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM source_file WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("delete source file %s: %w", path, err)
	}
	return nil
}

// DeleteModule removes a module and its dependent rows — the
// vanished-source counterpart to ReplaceModule, with the same
// deletion set (symbols cascade via FK; FTS via triggers).
func (s *Store) DeleteModule(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("DeleteModule: empty name")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM module WHERE name = ?`,
		`DELETE FROM module_import WHERE module_name = ?`,
		`DELETE FROM reference WHERE source_module = ?`,
		`DELETE FROM diagnostic WHERE module_name = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, name); err != nil {
			return fmt.Errorf("delete module %s: %w", name, err)
		}
	}
	return tx.Commit()
}

// PruneModulesWithoutSource removes modules that no source_file row
// backs — pre-upgrade orphans (the fingerprint table is newer than
// their rows) and curated files that stopped compiling (their
// fingerprint is dropped on failure). Returns the removed names.
func (s *Store) PruneModulesWithoutSource(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name FROM module
		WHERE name NOT IN (SELECT module_name FROM source_file)`)
	if err != nil {
		return nil, fmt.Errorf("list ghost modules: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for _, n := range names {
		if err := s.DeleteModule(ctx, n); err != nil {
			return names, fmt.Errorf("prune module %s: %w", n, err)
		}
	}
	return names, nil
}

// ResetSourceFiles truncates the fingerprint table — the explicit
// `-rebuild` escape hatch and the destructive-migration invariant:
// any migration that drops compiled data MUST also clear the
// fingerprints, or the validation walk would trust matches against
// an empty store and never repopulate it.
func (s *Store) ResetSourceFiles(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM source_file`); err != nil {
		return fmt.Errorf("reset source files: %w", err)
	}
	return nil
}

// ImportOutcome is one recorded import-pipeline result for the
// management UI. The quarantine dirs + sidecars on disk are the
// source of truth; these rows are display state.
type ImportOutcome struct {
	Name       string
	Status     string // imported | failed | duplicate
	ModuleName string
	Detail     string // destination / reason / existing path
	OccurredAt string
}

// importOutcomeKeep bounds the recent-outcomes list; older rows are
// pruned on insert.
const importOutcomeKeep = 200

// RecordImportOutcome appends an outcome row and prunes old ones.
func (s *Store) RecordImportOutcome(ctx context.Context, o ImportOutcome) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO import_outcome (name, status, module_name, detail)
		VALUES (?, ?, ?, ?)`,
		o.Name, o.Status, o.ModuleName, o.Detail); err != nil {
		return fmt.Errorf("record import outcome: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM import_outcome WHERE id NOT IN
		(SELECT id FROM import_outcome ORDER BY id DESC LIMIT ?)`,
		importOutcomeKeep)
	if err != nil {
		return fmt.Errorf("prune import outcomes: %w", err)
	}
	return nil
}

// ListImportOutcomes returns the most recent outcomes, newest first.
func (s *Store) ListImportOutcomes(ctx context.Context, limit int) ([]ImportOutcome, error) {
	if limit <= 0 || limit > importOutcomeKeep {
		limit = importOutcomeKeep
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, status, module_name, detail, occurred_at
		FROM import_outcome ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list import outcomes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ImportOutcome
	for rows.Next() {
		var o ImportOutcome
		if err := rows.Scan(&o.Name, &o.Status, &o.ModuleName, &o.Detail, &o.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
