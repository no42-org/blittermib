package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite"

	"github.com/no42-org/blittermib/internal/model"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps a SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at path and applies the schema.
//
// path may be ":memory:" for an ephemeral test database; the file form
// uses WAL mode for better read concurrency.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite PRAGMAs are PER-CONNECTION. Pinning the pool to a single
	// connection lets us set them once and have every query observe
	// the same enforcement (FK cascades, WAL, busy timeout). At our
	// self-hosted single-server scale the read-concurrency cost of
	// max-1 is not measurable; SQLite serializes writes regardless.
	db.SetMaxOpenConns(1)

	for _, p := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous  = NORMAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(ctx, p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply pragma %q: %w", p, err)
		}
	}

	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	if err := migrateSymbolKindSplit(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate symbol table: %w", err)
	}
	return &Store{db: db}, nil
}

// migrateSymbolKindSplit handles the one-shot Phase-2 migration:
// older databases have `is_table`/`is_table_entry` columns on the
// symbol table that no longer exist in schema.sql. Detect their
// presence, drop the symbol table + its FTS shadow + triggers, then
// re-apply the schema so the new shape is created. The loader's
// startup scan repopulates the table on the same boot — no symbol
// data is preserved across the migration (it's recompiled from the
// on-disk MIB bundle, which is the source-of-truth).
func migrateSymbolKindSplit(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(symbol)`)
	if err != nil {
		return fmt.Errorf("inspect symbol columns: %w", err)
	}
	hasOldFlags := false
	for rows.Next() {
		var (
			cid, notnull, pk  int
			name, ctype, dflt sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan column row: %w", err)
		}
		if name.Valid && (name.String == "is_table" || name.String == "is_table_entry") {
			hasOldFlags = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	// Close explicitly before issuing DDL: the pool is pinned to one
	// connection (MaxOpenConns=1), so an open Rows iterator could
	// stall the subsequent Exec. mattn/go-sqlite3 releases on Next()
	// returning false, but the explicit close removes the dependency
	// on driver internals.
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close table_info rows: %w", err)
	}
	if !hasOldFlags {
		return nil
	}

	slog.Info("migrating symbol table to phase-2 kind split")

	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS symbol_ai`,
		`DROP TRIGGER IF EXISTS symbol_ad`,
		`DROP TRIGGER IF EXISTS symbol_au`,
		`DROP TABLE IF EXISTS symbol_fts`,
		`DROP TABLE IF EXISTS symbol`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("recreate schema: %w", err)
	}
	return nil
}

// OpenInMemory is a convenience for tests.
func OpenInMemory(ctx context.Context) (*Store, error) {
	return Open(ctx, ":memory:")
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying *sql.DB for advanced uses (e.g. backups).
// Callers should generally prefer the typed methods on Store.
func (s *Store) DB() *sql.DB { return s.db }

// ReplaceModule atomically replaces a module's data in a single
// transaction: the old module rows are removed (cascading to symbols
// via FK), the new rows are written, and the module's outgoing
// cross-references and diagnostics are rewritten.
//
// References INTO this module from OTHER modules are unaffected because
// they're keyed by qualified name, not by the symbol IDs that change
// across reloads.
func (s *Store) ReplaceModule(
	ctx context.Context,
	mod *model.Module,
	syms []model.Symbol,
	refs []model.Reference,
	diags []model.Diagnostic,
) error {
	if mod == nil {
		return errors.New("ReplaceModule: nil module")
	}
	if mod.Name == "" {
		return errors.New("ReplaceModule: module has empty name")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM module WHERE name = ?`, mod.Name); err != nil {
		return fmt.Errorf("delete old module: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM reference WHERE source_module = ?`, mod.Name,
	); err != nil {
		return fmt.Errorf("delete old refs: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM diagnostic WHERE module_name = ?`, mod.Name,
	); err != nil {
		return fmt.Errorf("delete old diagnostics: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO module
		    (name, oid_root, organization, contact_info, description, last_updated, source_path, parse_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mod.Name, mod.OIDRoot, mod.Organization, mod.ContactInfo,
		mod.Description, mod.LastUpdated, mod.SourcePath, string(mod.ParseStatus),
	); err != nil {
		return fmt.Errorf("insert module: %w", err)
	}

	insSym, err := tx.PrepareContext(ctx, `
		INSERT INTO symbol
		    (module_name, name, oid, parent_oid, kind, syntax, access, status,
		     units, reference_text, description, default_value, augments,
		     index_columns, enum_values, source_line)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert symbol: %w", err)
	}
	defer insSym.Close()

	for i := range syms {
		idxJSON := encodeIndex(syms[i].IndexColumns)
		enumJSON := encodeEnumValues(syms[i].EnumValues)
		if _, err := insSym.ExecContext(ctx,
			syms[i].ModuleName, syms[i].Name, syms[i].OID, syms[i].ParentOID,
			string(syms[i].Kind), syms[i].Syntax, string(syms[i].Access),
			string(syms[i].Status), syms[i].Units, syms[i].Reference,
			syms[i].Description, syms[i].DefaultValue,
			syms[i].Augments, idxJSON, enumJSON, syms[i].SourceLine,
		); err != nil {
			return fmt.Errorf("insert symbol %s::%s: %w",
				syms[i].ModuleName, syms[i].Name, err)
		}
	}

	if len(refs) > 0 {
		insRef, err := tx.PrepareContext(ctx, `
			INSERT OR IGNORE INTO reference
			    (source_module, source_name, target_module, target_name, kind)
			VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare insert reference: %w", err)
		}
		defer insRef.Close()
		for _, r := range refs {
			if _, err := insRef.ExecContext(ctx,
				r.SourceModule, r.SourceName,
				r.TargetModule, r.TargetName,
				string(r.Kind),
			); err != nil {
				return fmt.Errorf("insert reference: %w", err)
			}
		}
	}

	if len(diags) > 0 {
		insDiag, err := tx.PrepareContext(ctx, `
			INSERT INTO diagnostic
			    (module_name, file, line, severity, code, message)
			VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare insert diagnostic: %w", err)
		}
		defer insDiag.Close()
		for _, d := range diags {
			if _, err := insDiag.ExecContext(ctx,
				mod.Name, d.File, d.Line,
				string(d.Severity), d.Code, d.Message,
			); err != nil {
				return fmt.Errorf("insert diagnostic: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func encodeIndex(cols []string) string {
	if len(cols) == 0 {
		return ""
	}
	b, err := json.Marshal(cols)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeIndex(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		// Log and degrade rather than failing the whole row read.
		// Corrupt index_columns shouldn't take down a symbol page.
		slog.Warn("invalid index_columns JSON in symbol row", "value", s, "err", err)
		return nil
	}
	return out
}

func encodeEnumValues(vs []model.EnumValue) string {
	if len(vs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(vs)
	if err != nil {
		// json.Marshal of []EnumValue cannot fail in practice; if it
		// ever does, log noisily rather than silently writing "[]"
		// and losing data.
		slog.Warn("failed to marshal enum_values; persisting as empty array",
			"count", len(vs), "err", err)
		return "[]"
	}
	return string(b)
}

func decodeEnumValues(s string) []model.EnumValue {
	if s == "" {
		return nil
	}
	var out []model.EnumValue
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		slog.Warn("invalid enum_values JSON in symbol row", "value", s, "err", err)
		return nil
	}
	return out
}
