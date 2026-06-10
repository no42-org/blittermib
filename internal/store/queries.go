package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/no42-org/blittermib/internal/eventconf"
	"github.com/no42-org/blittermib/internal/model"
)

// ErrNotFound is returned when a lookup matches no rows.
var ErrNotFound = errors.New("not found")

// GetModule returns a single module by name. The IMPORTS clause is
// loaded into `m.Imports` so the workspace overview can render it
// without a second round-trip.
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

	imports, err := s.listImportsByModule(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get module %s imports: %w", name, err)
	}
	m.Imports = imports
	return &m, nil
}

// listImportsByModule returns the IMPORTS clause for a module,
// ordered by source position so the rendered list matches the order
// of the IMPORTS at the top of the MIB file.
func (s *Store) listImportsByModule(ctx context.Context, module string) ([]model.Import, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT from_module, symbol
		FROM module_import WHERE module_name = ?
		ORDER BY position, from_module, symbol`, module)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.Import
	for rows.Next() {
		var imp model.Import
		if err := rows.Scan(&imp.FromModule, &imp.Symbol); err != nil {
			return nil, err
		}
		out = append(out, imp)
	}
	return out, rows.Err()
}

// ClosureEntry is one node in a module's transitive IMPORTS closure.
//
// `Loaded` distinguishes "this module is in the store, with a real
// source file at SourcePath" (true) from "this module name appears
// in some IMPORTS clause but no `module` row exists" (false). The
// false case feeds the bundle download's MISSING.txt manifest so
// the user can see exactly what wasn't included.
//
// `ImportedBy` is the immediate importer — the module that named
// this entry in its IMPORTS clause and caused the BFS visit. For
// closure entries discovered as the root, this is empty. The full
// ancestor chain is intentionally NOT recorded; the spec asks for
// the immediate importer only.
//
// `Symbols` carries the symbol names imported FROM this module by
// the immediate importer (`ImportedBy`). For unloaded entries this
// helps the user understand what they'd lose by not fetching it.
type ClosureEntry struct {
	Module     string
	SourcePath string
	Loaded     bool
	ImportedBy string
	Symbols    []string
}

// ListImportClosure walks the IMPORTS graph rooted at `root` and
// returns one entry per distinct module reachable. A thin wrapper
// over ListImportClosureUnion with a single root.
func (s *Store) ListImportClosure(ctx context.Context, root string) ([]ClosureEntry, error) {
	return s.ListImportClosureUnion(ctx, []string{root})
}

// ListImportClosureUnion walks the IMPORTS graph rooted at every
// module in `roots` and returns one entry per distinct module
// reachable from any of them. Order is BFS: the roots first (in the
// given order), then their direct imports in source order, then
// their imports, and so on. A single visited-set is shared across
// all roots, so a hub module such as SNMPv2-SMI reached from two
// different roots appears exactly once, with `ImportedBy` carrying
// the first parent that named it. Cycles are bounded by that
// visited set (SMI forbids cycles by spec; defensive against
// malformed input).
//
// One DB round-trip per loaded module visited (1 GetModule + 1
// listImportsByModule each). For the union of a handful of roots
// that's still well under a millisecond on local SQLite. A recursive
// CTE would collapse it to one query if profiling shows it as hot.
func (s *Store) ListImportClosureUnion(ctx context.Context, roots []string) ([]ClosureEntry, error) {
	visited := make(map[string]struct{})
	var out []ClosureEntry

	// Frontier holds names whose imports we still need to walk.
	type frontierEntry struct {
		name string
	}
	var frontier []frontierEntry

	// Seed every root. Empty names are skipped (a caller passing a
	// sparse list shouldn't fail the whole walk), and roots already
	// reachable from an earlier root are deduplicated — both by the
	// requested name and, after resolution, by the canonical module
	// name, so two aliases of one module don't yield duplicate rows.
	for _, root := range roots {
		if root == "" {
			continue
		}
		if _, dup := visited[root]; dup {
			continue
		}
		rootMod, err := s.GetModule(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("closure root %s: %w", root, err)
		}
		if _, dup := visited[rootMod.Name]; dup {
			continue
		}
		visited[rootMod.Name] = struct{}{}
		out = append(out, ClosureEntry{
			Module:     rootMod.Name,
			SourcePath: rootMod.SourcePath,
			Loaded:     true,
		})
		frontier = append(frontier, frontierEntry{name: rootMod.Name})
	}

	// Per-importer aggregation of imported symbols, so MISSING.txt
	// can list `(symbols: A, B, C)` rather than one row per symbol.
	type pendingImport struct {
		fromModule string
		importedBy string
		symbols    []string
	}
	pendings := make(map[string]*pendingImport)

	pkey := func(fromModule, importedBy string) string {
		return fromModule + "\x00" + importedBy
	}

	for len(frontier) > 0 {
		// Honor request-context cancellation between BFS rounds —
		// closure walks on hub MIBs (SNMPv2-SMI etc.) can fan out
		// across dozens of GetModule round-trips and shouldn't keep
		// running after a disconnect.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := frontier[0]
		frontier = frontier[1:]

		imports, err := s.listImportsByModule(ctx, cur.name)
		if err != nil {
			return nil, fmt.Errorf("closure imports of %s: %w", cur.name, err)
		}
		for _, imp := range imports {
			k := pkey(imp.FromModule, cur.name)
			p, ok := pendings[k]
			if !ok {
				p = &pendingImport{
					fromModule: imp.FromModule,
					importedBy: cur.name,
				}
				pendings[k] = p
			}
			// Dedup symbols within the same (fromModule, importedBy)
			// pair. SMI doesn't strictly forbid the same symbol
			// appearing twice in an IMPORTS clause; without dedup
			// MISSING.txt would echo the duplicate.
			dup := false
			for _, existing := range p.symbols {
				if existing == imp.Symbol {
					dup = true
					break
				}
			}
			if !dup {
				p.symbols = append(p.symbols, imp.Symbol)
			}
		}

		// Process pendings in source order — iterate `imports`
		// again to keep ordering stable, but only act on the first
		// occurrence per (fromModule, importedBy) pair.
		seenInThisCur := make(map[string]struct{})
		for _, imp := range imports {
			if _, dup := seenInThisCur[imp.FromModule]; dup {
				continue
			}
			seenInThisCur[imp.FromModule] = struct{}{}

			if _, dup := visited[imp.FromModule]; dup {
				continue
			}
			visited[imp.FromModule] = struct{}{}

			p := pendings[pkey(imp.FromModule, cur.name)]

			depMod, err := s.GetModule(ctx, imp.FromModule)
			switch {
			case errors.Is(err, ErrNotFound):
				out = append(out, ClosureEntry{
					Module:     imp.FromModule,
					Loaded:     false,
					ImportedBy: cur.name,
					Symbols:    p.symbols,
				})
			case err != nil:
				return nil, fmt.Errorf("closure dep %s: %w", imp.FromModule, err)
			default:
				out = append(out, ClosureEntry{
					Module:     depMod.Name,
					SourcePath: depMod.SourcePath,
					Loaded:     true,
					ImportedBy: cur.name,
					Symbols:    p.symbols,
				})
				frontier = append(frontier, frontierEntry{name: depMod.Name})
			}
		}
	}

	return out, nil
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
	defer func() { _ = rows.Close() }()

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
//
// Ordered for deterministic rendering — golden-HTML snapshot tests
// rely on stable iteration.
func (s *Store) ListReferencesFrom(ctx context.Context, module, name string) ([]model.Reference, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_module, source_name, target_module, target_name, kind, position
		FROM reference WHERE source_module = ? AND source_name = ?
		ORDER BY target_module, target_name, kind`, module, name)
	if err != nil {
		return nil, err
	}
	return scanReferenceRows(rows)
}

// ListReferencesTo returns references whose target matches.
//
// Ordered for deterministic rendering (see ListReferencesFrom).
func (s *Store) ListReferencesTo(ctx context.Context, module, name string) ([]model.Reference, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_module, source_name, target_module, target_name, kind, position
		FROM reference WHERE target_module = ? AND target_name = ?
		ORDER BY source_module, source_name, kind`, module, name)
	if err != nil {
		return nil, err
	}
	return scanReferenceRows(rows)
}

// ListNotificationsWithObjects returns every NOTIFICATION-TYPE and
// TRAP-TYPE in the module paired with its objects (varbinds) in
// OBJECTS-clause order. Objects are resolved to full symbols via
// GetSymbol; an object whose target module is not loaded is kept as a
// minimal {ModuleName, Name} symbol so positional varbind numbering
// stays correct. Notifications are ordered by OID for stable output.
func (s *Store) ListNotificationsWithObjects(ctx context.Context, module string) ([]eventconf.Notification, error) {
	rows, err := s.db.QueryContext(ctx, symbolSelectColumns+`
		FROM symbol WHERE module_name = ? AND kind IN ('notification-type', 'trap-type')
		ORDER BY oid, name`, module)
	if err != nil {
		return nil, fmt.Errorf("list notifications for %s: %w", module, err)
	}
	notifs, err := scanSymbolRows(rows)
	if err != nil {
		return nil, err
	}

	out := make([]eventconf.Notification, 0, len(notifs))
	for i := range notifs {
		objRefs, err := s.notificationObjectRefs(ctx, notifs[i].ModuleName, notifs[i].Name)
		if err != nil {
			return nil, err
		}
		objs := make([]model.Symbol, 0, len(objRefs))
		for _, ref := range objRefs {
			sym, err := s.GetSymbol(ctx, ref.TargetModule, ref.TargetName)
			switch {
			case errors.Is(err, ErrNotFound):
				// Object imported from a module that isn't loaded:
				// keep a stub so its OBJECTS position is preserved.
				objs = append(objs, model.Symbol{ModuleName: ref.TargetModule, Name: ref.TargetName})
			case err != nil:
				return nil, fmt.Errorf("resolve notification object %s::%s: %w",
					ref.TargetModule, ref.TargetName, err)
			default:
				objs = append(objs, *sym)
			}
		}
		out = append(out, eventconf.Notification{Symbol: notifs[i], Objects: objs})
	}
	return out, nil
}

// notificationObjectRefs returns a notification's object references in
// OBJECTS-clause (position) order.
func (s *Store) notificationObjectRefs(ctx context.Context, module, name string) ([]model.Reference, error) {
	// ORDER BY position first; rowid is a deterministic tiebreak for
	// legacy rows that predate the position column (all defaulting to
	// 0) so object order stays stable even before a re-ingest
	// repopulates positions. Insertion order (rowid) matches the
	// OBJECTS clause.
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_module, source_name, target_module, target_name, kind, position
		FROM reference
		WHERE source_module = ? AND source_name = ? AND kind = 'notification-object'
		ORDER BY position, rowid`, module, name)
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
	defer func() { _ = rows.Close() }()
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

// LookupByName returns every symbol matching the given name across
// all modules. Used by the /s/{name} disambiguation handler when a
// user supplies a bare name without the Module:: qualifier.
func (s *Store) LookupByName(ctx context.Context, name string) ([]model.Symbol, error) {
	rows, err := s.db.QueryContext(ctx, symbolSelectColumns+`
		FROM symbol WHERE name = ? ORDER BY module_name`, name)
	if err != nil {
		return nil, fmt.Errorf("lookup by name %s: %w", name, err)
	}
	return scanSymbolRows(rows)
}

// HasChildren reports whether the given OID has at least one direct child
// in the symbol table. Used by the tree API to decide whether to surface
// an expand chevron without paying for a full children list.
func (s *Store) HasChildren(ctx context.Context, parentOID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM symbol WHERE parent_oid = ? LIMIT 1`,
		parentOID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// HasChildrenBatch returns a map keyed by OID indicating whether
// each parent has at least one direct child in the symbol table.
// One DB query total instead of len(parents) round-trips — the
// workspace handler and the tree-fragment endpoint both need this
// for every row at a level, and `MaxOpenConns(1)` makes serial
// per-row queries measurable on wide modules.
//
// OIDs not in the input slice are absent from the result map; OIDs
// in the input but with no children appear with a `false` value.
func (s *Store) HasChildrenBatch(ctx context.Context, parents []string) (map[string]bool, error) {
	out := make(map[string]bool, len(parents))
	if len(parents) == 0 {
		return out, nil
	}
	for _, p := range parents {
		out[p] = false
	}
	args := make([]any, 0, len(parents))
	placeholders := make([]byte, 0, 2*len(parents))
	for i, p := range parents {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, p)
	}
	// #nosec G202 -- placeholders is a locally-built run of `?` separators; values flow through QueryContext args, not the SQL string.
	q := `SELECT parent_oid FROM symbol WHERE parent_oid IN (` +
		string(placeholders) + `) GROUP BY parent_oid`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("has-children batch: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
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

// CountByFamily returns the per-type-family symbol totals for one
// module. One DB query loads (kind, syntax) tuples; classification
// runs in Go via model.TypeFamily so the same taxonomy serves the
// status bar, the row-class application, and the front-end. The
// counts are independent metrics — they do NOT sum to the module's
// symbol total. The Structs count is also surfaced as "objects" in
// the status bar.
func (s *Store) CountByFamily(ctx context.Context, moduleName string) (*model.FamilyCounts, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, syntax FROM symbol WHERE module_name = ?`, moduleName)
	if err != nil {
		return nil, fmt.Errorf("count by family for %s: %w", moduleName, err)
	}
	defer func() { _ = rows.Close() }()

	var fc model.FamilyCounts
	for rows.Next() {
		var kind, syntax string
		if err := rows.Scan(&kind, &syntax); err != nil {
			return nil, err
		}
		switch model.TypeFamily(model.SymbolKind(kind), syntax, false) {
		case "t-counter":
			fc.Counters++
		case "t-gauge":
			fc.Gauges++
		case "t-int":
			fc.Ints++
		case "t-text":
			fc.Texts++
		case "t-index":
			fc.Indexes++
		case "t-time":
			fc.Times++
		case "t-addr":
			fc.Addrs++
		case "t-bool":
			fc.Bools++
		case "t-notif":
			fc.Notifs++
		case "t-struct":
			fc.Structs++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &fc, nil
}

// OIDPath decodes an OID into one model.OIDStep per ancestor
// prefix, naming each step from a loaded MIB symbol when one
// matches and from the canonical-OID fallback table otherwise.
//
// One database hit total: every prefix is included in a single
// `WHERE oid IN (?, …)` query. Multi-match on the same prefix is
// resolved deterministically by ascending module_name, then name.
//
// Returned slice is empty when oid is empty. OIDs deeper than
// maxOIDDepth segments are rejected with an error so a pathological
// URL can't trip SQLite's parameter-count limit.
func (s *Store) OIDPath(ctx context.Context, oid string) ([]model.OIDStep, error) {
	prefixes := oidPrefixes(oid)
	if len(prefixes) == 0 {
		return nil, nil
	}
	if len(prefixes) > maxOIDDepth {
		return nil, fmt.Errorf("oid path %s: depth %d exceeds cap %d",
			oid, len(prefixes), maxOIDDepth)
	}

	args := make([]any, 0, len(prefixes))
	placeholders := make([]byte, 0, 2*len(prefixes))
	for i, p := range prefixes {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, p)
	}

	// #nosec G202 -- placeholders is a locally-built run of `?` separators; values flow through QueryContext args, not the SQL string.
	q := `SELECT oid, module_name, name, kind FROM symbol WHERE oid IN (` +
		string(placeholders) + `) ORDER BY oid, module_name, name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("oid path %s: %w", oid, err)
	}
	defer func() { _ = rows.Close() }()

	type hit struct {
		module string
		name   string
		kind   model.SymbolKind
	}
	first := make(map[string]hit, len(prefixes))
	for rows.Next() {
		var stepOID, mod, name, kind string
		if err := rows.Scan(&stepOID, &mod, &name, &kind); err != nil {
			return nil, err
		}
		if _, seen := first[stepOID]; seen {
			continue
		}
		first[stepOID] = hit{module: mod, name: name, kind: model.SymbolKind(kind)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]model.OIDStep, 0, len(prefixes))
	for _, p := range prefixes {
		if h, ok := first[p]; ok {
			out = append(out, model.OIDStep{
				Prefix: p, Name: h.name, Module: h.module, Kind: h.kind,
			})
			continue
		}
		if name, ok := canonicalName(p); ok {
			out = append(out, model.OIDStep{
				Prefix: p, Name: name, Canonical: true,
			})
			continue
		}
		// Bare numeric — last segment only, no name.
		out = append(out, model.OIDStep{Prefix: p})
	}
	return out, nil
}

// --- internal helpers --------------------------------------------------

const symbolSelectColumns = `
	SELECT id, module_name, name, oid, parent_oid, kind, syntax, access, status,
	       units, reference_text, description, default_value, augments,
	       index_columns, index_implied, enum_values, source_line `

func scanSymbol(scan func(...any) error) (*model.Symbol, error) {
	var s model.Symbol
	var kind, access, status, idxJSON, enumJSON string
	var impliedFlag int
	if err := scan(&s.ID, &s.ModuleName, &s.Name, &s.OID, &s.ParentOID,
		&kind, &s.Syntax, &access, &status, &s.Units, &s.Reference,
		&s.Description, &s.DefaultValue,
		&s.Augments, &idxJSON, &impliedFlag, &enumJSON, &s.SourceLine); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.Kind = model.SymbolKind(kind)
	s.Access = model.Access(access)
	s.Status = model.Status(status)
	s.IndexColumns = decodeIndex(idxJSON)
	s.IndexImplied = impliedFlag != 0
	s.EnumValues = decodeEnumValues(enumJSON)
	return &s, nil
}

func scanSymbolRows(rows *sql.Rows) ([]model.Symbol, error) {
	defer func() { _ = rows.Close() }()
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
	defer func() { _ = rows.Close() }()
	var out []model.Reference
	for rows.Next() {
		var r model.Reference
		var kind string
		if err := rows.Scan(&r.SourceModule, &r.SourceName,
			&r.TargetModule, &r.TargetName, &kind, &r.Position); err != nil {
			return nil, err
		}
		r.Kind = model.ReferenceKind(kind)
		out = append(out, r)
	}
	return out, rows.Err()
}
