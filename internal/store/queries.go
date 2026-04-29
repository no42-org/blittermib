package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/no42-org/blittermib/internal/model"
)

// ErrNotFound is returned when a lookup matches no rows.
var ErrNotFound = errors.New("not found")

// GetModule returns a single module by name.
func (s *Store) GetModule(ctx context.Context, name string) (*model.Module, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, oid_root, organization, contact_info, description,
		       last_updated, source_path, parse_status
		FROM module WHERE name = ?`, name)
	var m model.Module
	var status string
	if err := row.Scan(&m.Name, &m.OIDRoot, &m.Organization, &m.ContactInfo,
		&m.Description, &m.LastUpdated, &m.SourcePath, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get module %s: %w", name, err)
	}
	m.ParseStatus = model.ParseStatus(status)
	return &m, nil
}

// ListModules returns all modules ordered by name.
func (s *Store) ListModules(ctx context.Context) ([]model.Module, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, oid_root, organization, contact_info, description,
		       last_updated, source_path, parse_status
		FROM module ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err)
	}
	defer rows.Close()

	var out []model.Module
	for rows.Next() {
		var m model.Module
		var status string
		if err := rows.Scan(&m.Name, &m.OIDRoot, &m.Organization, &m.ContactInfo,
			&m.Description, &m.LastUpdated, &m.SourcePath, &status); err != nil {
			return nil, err
		}
		m.ParseStatus = model.ParseStatus(status)
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetSymbol returns a symbol by qualified name.
func (s *Store) GetSymbol(ctx context.Context, module, name string) (*model.Symbol, error) {
	row := s.db.QueryRowContext(ctx, symbolSelectColumns+`
		FROM symbol WHERE module_name = ? AND name = ?`, module, name)
	return scanSymbol(row.Scan)
}

// GetSymbolByOID returns the (single) symbol attached to a given OID.
// If multiple symbols share the OID (rare; would indicate a parse
// anomaly), only the first is returned.
func (s *Store) GetSymbolByOID(ctx context.Context, oid string) (*model.Symbol, error) {
	row := s.db.QueryRowContext(ctx, symbolSelectColumns+`
		FROM symbol WHERE oid = ? ORDER BY id LIMIT 1`, oid)
	return scanSymbol(row.Scan)
}

// ListSymbolsByModule returns all symbols belonging to a module, ordered
// by their OID (numeric ordering would require splitting; lexical
// ordering is good enough at view-time).
func (s *Store) ListSymbolsByModule(ctx context.Context, module string) ([]model.Symbol, error) {
	rows, err := s.db.QueryContext(ctx, symbolSelectColumns+`
		FROM symbol WHERE module_name = ? ORDER BY oid, name`, module)
	if err != nil {
		return nil, fmt.Errorf("list symbols by module %s: %w", module, err)
	}
	return scanSymbolRows(rows)
}

// ListChildren returns symbols whose parent_oid matches.
func (s *Store) ListChildren(ctx context.Context, parentOID string) ([]model.Symbol, error) {
	rows, err := s.db.QueryContext(ctx, symbolSelectColumns+`
		FROM symbol WHERE parent_oid = ? ORDER BY oid, name`, parentOID)
	if err != nil {
		return nil, fmt.Errorf("list children of %s: %w", parentOID, err)
	}
	return scanSymbolRows(rows)
}

// ListReferencesFrom returns references whose source matches.
func (s *Store) ListReferencesFrom(ctx context.Context, module, name string) ([]model.Reference, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_module, source_name, target_module, target_name, kind
		FROM reference WHERE source_module = ? AND source_name = ?`, module, name)
	if err != nil {
		return nil, err
	}
	return scanReferenceRows(rows)
}

// ListReferencesTo returns references whose target matches.
func (s *Store) ListReferencesTo(ctx context.Context, module, name string) ([]model.Reference, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_module, source_name, target_module, target_name, kind
		FROM reference WHERE target_module = ? AND target_name = ?`, module, name)
	if err != nil {
		return nil, err
	}
	return scanReferenceRows(rows)
}

// ListDiagnosticsByModule returns parse diagnostics for a module.
func (s *Store) ListDiagnosticsByModule(ctx context.Context, module string) ([]model.Diagnostic, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT file, line, severity, code, message
		FROM diagnostic WHERE module_name = ? ORDER BY line`, module)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Diagnostic
	for rows.Next() {
		var d model.Diagnostic
		var sev string
		if err := rows.Scan(&d.File, &d.Line, &sev, &d.Code, &d.Message); err != nil {
			return nil, err
		}
		d.Severity = model.DiagnosticSeverity(sev)
		d.Module = module
		out = append(out, d)
	}
	return out, rows.Err()
}

// CountSymbols returns the total number of symbols across all modules.
func (s *Store) CountSymbols(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbol`).Scan(&n)
	return n, err
}

// CountModules returns the total number of modules.
func (s *Store) CountModules(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM module`).Scan(&n)
	return n, err
}

// --- internal helpers --------------------------------------------------

const symbolSelectColumns = `
	SELECT id, module_name, name, oid, parent_oid, kind, syntax, access, status,
	       units, reference_text, description, default_value, is_table,
	       is_table_entry, augments, index_columns, source_line `

func scanSymbol(scan func(...any) error) (*model.Symbol, error) {
	var s model.Symbol
	var kind, access, status, idxJSON string
	var isTable, isEntry int
	if err := scan(&s.ID, &s.ModuleName, &s.Name, &s.OID, &s.ParentOID,
		&kind, &s.Syntax, &access, &status, &s.Units, &s.Reference,
		&s.Description, &s.DefaultValue, &isTable, &isEntry,
		&s.Augments, &idxJSON, &s.SourceLine); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.Kind = model.SymbolKind(kind)
	s.Access = model.Access(access)
	s.Status = model.Status(status)
	s.IsTable = intBool(isTable)
	s.IsTableEntry = intBool(isEntry)
	s.IndexColumns = decodeIndex(idxJSON)
	return &s, nil
}

func scanSymbolRows(rows *sql.Rows) ([]model.Symbol, error) {
	defer rows.Close()
	var out []model.Symbol
	for rows.Next() {
		s, err := scanSymbol(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func scanReferenceRows(rows *sql.Rows) ([]model.Reference, error) {
	defer rows.Close()
	var out []model.Reference
	for rows.Next() {
		var r model.Reference
		var kind string
		if err := rows.Scan(&r.SourceModule, &r.SourceName,
			&r.TargetModule, &r.TargetName, &kind); err != nil {
			return nil, err
		}
		r.Kind = model.ReferenceKind(kind)
		out = append(out, r)
	}
	return out, rows.Err()
}
