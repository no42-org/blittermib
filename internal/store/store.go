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

	"github.com/no42-org/blittermib/internal/correlate"
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
	if err := migrateAddIndexImplied(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate index_implied: %w", err)
	}
	if err := migrateAddReferencePosition(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate reference position: %w", err)
	}
	s := &Store{db: db}
	// One-time: classify an already-ingested corpus whose relationship
	// tables predate the feature (the boot sync skips unchanged MIBs, so
	// ReplaceModule never runs for them).
	if err := s.backfillRelationships(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("backfill relationships: %w", err)
	}
	return s, nil
}

// migrateSymbolKindSplit handles the one-shot Phase-2 migration:
// older databases have `is_table`/`is_table_entry` columns on the
// symbol table that no longer exist in schema.sql. Detect their
// presence, drop the symbol table + its FTS shadow + triggers, then
// re-apply the schema so the new shape is created. The loader's
// startup scan repopulates the table on the same boot — no symbol
// data is preserved across the migration (it's recompiled from the
// on-disk MIB bundle, which is the source-of-truth).

// tableHasColumn reports whether `table` currently has a column named
// `col`, via PRAGMA table_info. Shared by the boot migrations, which
// detect schema generations by column presence. The result rows are
// closed explicitly before returning: the pool is pinned to one
// connection (MaxOpenConns=1), so a lingering Rows iterator could
// stall a caller's subsequent DDL Exec. mattn/go-sqlite3 releases on
// Next() returning false, but the explicit close removes the
// dependency on driver internals.
func tableHasColumn(ctx context.Context, db *sql.DB, table, col string) (bool, error) {
	// #nosec G202 -- table is a compile-time constant supplied by the migrations below, never user input.
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	found := false
	for rows.Next() {
		var (
			cid, notnull, pk  int
			name, ctype, dflt sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan column row: %w", err)
		}
		if name.Valid && name.String == col {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close table_info rows: %w", err)
	}
	return found, nil
}

func migrateSymbolKindSplit(ctx context.Context, db *sql.DB) error {
	hasIsTable, err := tableHasColumn(ctx, db, "symbol", "is_table")
	if err != nil {
		return err
	}
	hasIsEntry, err := tableHasColumn(ctx, db, "symbol", "is_table_entry")
	if err != nil {
		return err
	}
	if !hasIsTable && !hasIsEntry {
		return nil
	}

	slog.Info("migrating symbol table to phase-2 kind split")

	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS symbol_ai`,
		`DROP TRIGGER IF EXISTS symbol_ad`,
		`DROP TRIGGER IF EXISTS symbol_au`,
		`DROP TABLE IF EXISTS symbol_fts`,
		`DROP TABLE IF EXISTS symbol`,
		// INVARIANT: any migration that drops compiled data MUST also
		// clear the source_file fingerprints — the boot validation
		// walk trusts fingerprint matches and would otherwise skip
		// recompiling into the now-empty tables forever.
		`DELETE FROM source_file`,
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

// migrateAddIndexImplied adds the `index_implied` column to the
// symbol table on databases created before SMIv2 IMPLIED was
// plumbed through the model layer. Unlike `migrateSymbolKindSplit`
// this is non-destructive: existing rows get the column-default
// `0` (not implied), which is the safe behavior for any row that
// pre-dated the field. The loader's startup scan re-imports MIBs
// from disk anyway, so any rows that should be `1` are corrected
// on the same boot.
//
// SQLite's `ALTER TABLE ADD COLUMN` is in-place and cheap on a
// table the size we operate on (< 100k rows in any realistic
// MIB corpus); no rewrite is triggered for a NOT NULL DEFAULT 0
// addition.
func migrateAddIndexImplied(ctx context.Context, db *sql.DB) error {
	hasColumn, err := tableHasColumn(ctx, db, "symbol", "index_implied")
	if err != nil {
		return err
	}
	if hasColumn {
		return nil
	}

	slog.Info("adding index_implied column to symbol table")
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE symbol ADD COLUMN index_implied INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return fmt.Errorf("alter table add index_implied: %w", err)
	}
	return nil
}

// migrateAddReferencePosition adds the `position` column to the
// reference table on databases created before OBJECTS-clause ordering
// was needed. Like migrateAddIndexImplied this is a non-destructive
// in-place `ALTER TABLE ADD COLUMN`; pre-existing rows default to 0
// and are corrected on the same boot when the loader re-imports the
// MIB corpus from disk.
func migrateAddReferencePosition(ctx context.Context, db *sql.DB) error {
	hasColumn, err := tableHasColumn(ctx, db, "reference", "position")
	if err != nil {
		return err
	}
	if hasColumn {
		return nil
	}

	slog.Info("adding position column to reference table")
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE reference ADD COLUMN position INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return fmt.Errorf("alter table add position: %w", err)
	}
	return nil
}

// OpenInMemory is a convenience for tests.
func OpenInMemory(ctx context.Context) (*Store, error) {
	return Open(ctx, ":memory:")
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

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
		`DELETE FROM module_import WHERE module_name = ?`, mod.Name,
	); err != nil {
		return fmt.Errorf("delete old imports: %w", err)
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

	if len(mod.Imports) > 0 {
		insImp, err := tx.PrepareContext(ctx, `
			INSERT OR IGNORE INTO module_import
			    (module_name, from_module, symbol, position)
			VALUES (?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare insert import: %w", err)
		}
		defer func() { _ = insImp.Close() }()
		for i, imp := range mod.Imports {
			if _, err := insImp.ExecContext(ctx, mod.Name, imp.FromModule, imp.Symbol, i); err != nil {
				return fmt.Errorf("insert import %s.%s: %w", imp.FromModule, imp.Symbol, err)
			}
		}
	}

	insSym, err := tx.PrepareContext(ctx, `
		INSERT INTO symbol
		    (module_name, name, oid, parent_oid, kind, syntax, access, status,
		     units, reference_text, description, default_value, augments,
		     index_columns, index_implied, enum_values, source_line)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert symbol: %w", err)
	}
	defer func() { _ = insSym.Close() }()

	for i := range syms {
		idxJSON := encodeIndex(syms[i].IndexColumns)
		enumJSON := encodeEnumValues(syms[i].EnumValues)
		impliedFlag := 0
		if syms[i].IndexImplied {
			impliedFlag = 1
		}
		if _, err := insSym.ExecContext(ctx,
			syms[i].ModuleName, syms[i].Name, syms[i].OID, syms[i].ParentOID,
			string(syms[i].Kind), syms[i].Syntax, string(syms[i].Access),
			string(syms[i].Status), syms[i].Units, syms[i].Reference,
			syms[i].Description, syms[i].DefaultValue,
			syms[i].Augments, idxJSON, impliedFlag, enumJSON, syms[i].SourceLine,
		); err != nil {
			return fmt.Errorf("insert symbol %s::%s: %w",
				syms[i].ModuleName, syms[i].Name, err)
		}
	}

	if len(refs) > 0 {
		insRef, err := tx.PrepareContext(ctx, `
			INSERT OR IGNORE INTO reference
			    (source_module, source_name, target_module, target_name, kind, position)
			VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare insert reference: %w", err)
		}
		defer func() { _ = insRef.Close() }()
		for _, r := range refs {
			if _, err := insRef.ExecContext(ctx,
				r.SourceModule, r.SourceName,
				r.TargetModule, r.TargetName,
				string(r.Kind), r.Position,
			); err != nil {
				return fmt.Errorf("insert reference: %w", err)
			}
		}
	}

	// Derived: inferred notification relationships (Notification
	// Intelligence). Best-effort enrichment computed from the symbols
	// and refs we just inserted — a classifier fault must never abort a
	// module's ingest (see classify's recover), and the rows were
	// already cleared by the `DELETE FROM module` cascade above.
	if rels := classify(ctx, mod.Name, syms, refs); len(rels) > 0 {
		if err := writeRelationships(ctx, tx, mod.Name, rels); err != nil {
			return err
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
		defer func() { _ = insDiag.Close() }()
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

// execer is the subset of *sql.Tx / *sql.DB writeRelationships needs,
// so it works both inside ReplaceModule's transaction and the boot
// backfill's per-module transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// writeRelationships inserts a module's inferred relationships and their
// clear→raise pair edges. Shared by ReplaceModule (per-ingest) and the
// boot backfill.
func writeRelationships(ctx context.Context, ex execer, module string, rels []correlate.Relationship) error {
	for _, r := range rels {
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO notification_relationship
			    (module_name, notification_name, classification, confidence, evidence_json)
			VALUES (?, ?, ?, ?, ?)`,
			module, r.Notification, string(r.Class), string(r.Confidence), encodeEvidence(r.Evidence),
		); err != nil {
			return fmt.Errorf("insert relationship %s::%s: %w", module, r.Notification, err)
		}
		for _, raise := range r.Clears {
			if _, err := ex.ExecContext(ctx, `
				INSERT OR IGNORE INTO notification_pair (module_name, clear_name, raise_name)
				VALUES (?, ?, ?)`, module, r.Notification, raise); err != nil {
				return fmt.Errorf("insert pair %s::%s→%s: %w", module, r.Notification, raise, err)
			}
		}
	}
	return nil
}

// relationshipsBackfillVersion gates the one-time (re-)classification of
// an already-ingested corpus. The notification_relationship tables are
// populated on ReplaceModule (ingest), but a corpus cached by a build
// that predates Notification Intelligence — or by any build, since the
// boot sync skips unchanged MIBs — has empty (or stale) tables.
//
// Bump this whenever the correlate engine's output can change so that
// already-backfilled DBs re-classify on the next boot instead of serving
// a stale generation. (v2: re-run after the status-aware pairing /
// confidence calibration so deprecated-duplicate handling is consistent.)
const relationshipsBackfillVersion = 2

// backfillRelationships classifies every already-stored module into the
// relationship tables, once, when the cache predates the feature. It
// re-runs the pure correlate.Classify over stored symbols/refs — no MIB
// recompile — and is gated by PRAGMA user_version so it runs at most
// once per generation.
func (s *Store) backfillRelationships(ctx context.Context) error {
	var ver int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if ver >= relationshipsBackfillVersion {
		return nil
	}
	mods, err := s.ListModules(ctx)
	if err != nil {
		return err
	}
	// Clear any prior generation up front so a version bump re-classifies
	// cleanly: a module that no longer yields relationships must not keep
	// stale rows (a per-module delete inside the loop would skip it).
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notification_relationship`); err != nil {
		return fmt.Errorf("clear relationships: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notification_pair`); err != nil {
		return fmt.Errorf("clear pairs: %w", err)
	}
	for i := range mods {
		name := mods[i].Name
		syms, err := s.ListSymbolsByModule(ctx, name)
		if err != nil {
			return err
		}
		refs, err := s.listReferencesByModule(ctx, name)
		if err != nil {
			return err
		}
		rels := classify(ctx, name, syms, refs)
		if len(rels) == 0 {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := writeRelationships(ctx, tx, name, rels); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", relationshipsBackfillVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}

// classify runs correlate.Classify behind a recover guard. Inference
// is best-effort enrichment, so a classifier panic is logged and
// treated as "no relationships" rather than aborting the module's
// ingest transaction (and thus blocking an otherwise-valid MIB).
func classify(ctx context.Context, module string, syms []model.Symbol, refs []model.Reference) (rels []correlate.Relationship) {
	defer func() {
		if r := recover(); r != nil {
			slog.WarnContext(ctx, "notification inference panicked; skipping module", "module", module, "panic", r)
			rels = nil
		}
	}()
	return correlate.Classify(syms, refs)
}

// encodeEvidence serializes an inference's evidence trail for the
// notification_relationship.evidence_json column. A marshal failure
// degrades to "{}" rather than failing the row (consistent with
// encodeIndex/encodeEnumValues).
func encodeEvidence(ev correlate.Evidence) string {
	b, err := json.Marshal(ev)
	if err != nil {
		return "{}"
	}
	return string(b)
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
	// Always return a non-nil slice for empty-but-valid inputs so
	// `[]EnumValue{}` survives the encode → DB → decode round-trip
	// instead of degrading to `nil`. Callers that just want
	// "is this empty" still pass via `len() == 0`.
	if s == "" || s == "[]" {
		return []model.EnumValue{}
	}
	var out []model.EnumValue
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		slog.Warn("invalid enum_values JSON in symbol row", "value", s, "err", err)
		return []model.EnumValue{}
	}
	if out == nil {
		return []model.EnumValue{}
	}
	return out
}
