package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/source"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/web"
)

// --- ops endpoints ---------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.CountModules(r.Context()); err != nil {
		http.Error(w, "store unhealthy", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(s.version + "\n"))
}

// --- page handlers ---------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	modCount, _ := s.store.CountModules(ctx)
	symCount, _ := s.store.CountSymbols(ctx)
	if modCount == 0 {
		render(w, r, http.StatusOK, web.LandingEmpty(s.mibsDir))
		return
	}
	render(w, r, http.StatusOK, web.Landing(modCount, symCount))
}

func (s *Server) handleModule(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/m")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		s.handleModuleIndex(w, r)
		return
	}
	// Slash-first dispatch — three branches:
	//   /m/{name}                → workspace empty
	//   /m/{name}/source         → raw MIB source
	//   /m/{name}/{oid…}         → workspace with selection
	//
	// The earlier suffix-first check (`HasSuffix("/source")`) caught
	// /m/{name}/{oid}/source as a source request with the OID
	// embedded in the module name, which then 404'd. The slash-first
	// split makes the source endpoint exactly `/m/{name}/source`
	// and the workspace path everything else.
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		s.handleWorkspace(w, r, rest, "")
		return
	}
	name, tail := rest[:i], rest[i+1:]
	if tail == "source" {
		s.handleModuleSource(w, r, name)
		return
	}
	s.handleWorkspace(w, r, name, tail)
}

// handleModuleSource serves the raw MIB source file for a module as
// text/plain. http.ServeFile streams the file (handles range,
// etag, and if-modified-since for free) — better than reading
// the whole MIB into memory before writing.
func (s *Server) handleModuleSource(w http.ResponseWriter, r *http.Request, name string) {
	mod, err := s.store.GetModule(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if mod.SourcePath == "" {
		// Module is loaded but libsmi resolved it without a file
		// path (e.g. embedded module).
		s.notFound(w, r)
		return
	}
	// Pre-set the headers — http.ServeFile leaves them alone if
	// they're already populated. .mib files would otherwise default
	// to application/octet-stream which would prompt downloads.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, mod.SourcePath)
}

func (s *Server) handleModuleIndex(w http.ResponseWriter, r *http.Request) {
	mods, err := s.store.ListModules(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, web.ModuleIndex(mods))
}

// handleWorkspace serves the 3-pane workspace shell at /m/{name}
// and /m/{name}/{oid}. When oid is empty the right pane shows an
// empty-state hint; when oid resolves to a symbol the right pane
// renders the canonical detail body plus an OID-decode breadcrumb;
// when oid is non-empty but doesn't match anything in the module
// the workspace renders without a selection plus a soft missing-OID
// notice.
func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request, name, oid string) {
	ctx := r.Context()
	mod, err := s.store.GetModule(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	syms, err := s.store.ListSymbolsByModule(ctx, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	// Top-level tree rows are the module's "entry points into the OID
	// space" — symbols whose parent OID is NOT itself a symbol of
	// this module. The simpler `ListChildren(mod.OIDRoot)` strategy
	// fails the common case where MODULE-IDENTITY anchors as a
	// sysObjectID-style sentinel (e.g. ifMIB at 1.3.6.1.2.1.31.1)
	// while the actual symbols hang off mib-2 children (interfaces
	// at 1.3.6.1.2.1.2). Computing the orphan set in Go over the
	// already-loaded `syms` slice keeps this on the per-page hot
	// path without a second SQL round trip.
	moduleOIDs := make(map[string]struct{}, len(syms))
	for i := range syms {
		moduleOIDs[syms[i].OID] = struct{}{}
	}
	var topLevel []model.Symbol
	for i := range syms {
		if _, internal := moduleOIDs[syms[i].ParentOID]; internal {
			continue
		}
		topLevel = append(topLevel, syms[i])
	}

	// Single batched HasChildren query — replaces an N+1 round-trip
	// (with MaxOpenConns=1, those serialize and added up on wide
	// modules).
	parentOIDs := make([]string, 0, len(topLevel))
	for i := range topLevel {
		parentOIDs = append(parentOIDs, topLevel[i].OID)
	}
	hasChildren, err := s.store.HasChildrenBatch(ctx, parentOIDs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	treeRows := make([]web.TreeRow, 0, len(topLevel))
	for i := range topLevel {
		treeRows = append(treeRows, web.TreeRow{
			Symbol:      topLevel[i],
			HasChildren: hasChildren[topLevel[i].OID],
		})
	}

	counts, err := s.store.CountByFamily(ctx, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	allModules, err := s.store.ListModules(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	// When the URL specifies an OID, narrow the center-pane list to
	// symbols at or under that OID. The "View all in module" link
	// in the list-pane chrome navigates back to the unscoped URL.
	listRows := syms
	if oid != "" {
		listRows = listRows[:0:0]
		for i := range syms {
			if web.OIDUnderPrefix(syms[i].OID, oid) {
				listRows = append(listRows, syms[i])
			}
		}
	}

	view := &web.WorkspaceView{
		Module:   mod,
		Counts:   counts,
		TreeRows: treeRows,
		ListRows: listRows,
		Modules:  allModules,
		ScopeOID: oid,
	}

	if oid != "" {
		sym, err := s.store.GetSymbolByOID(ctx, oid)
		switch {
		case errors.Is(err, store.ErrNotFound):
			view.MissingOID = oid
		case err != nil:
			s.internalError(w, r, err)
			return
		default:
			selected := &web.SymbolView{Symbol: sym}
			selected.Context = s.buildSymbolContext(ctx, sym)
			if sym.Kind == model.KindTable {
				selected.Columns = s.buildTableColumns(ctx, sym)
			}
			usedBy, err := s.store.ListReferencesTo(ctx, sym.ModuleName, sym.Name)
			if err != nil {
				s.internalError(w, r, err)
				return
			}
			selected.UsedBy = usedBy
			if symMod, err := s.store.GetModule(ctx, sym.ModuleName); err == nil && symMod.SourcePath != "" && sym.SourceLine > 0 {
				if slice, err := source.Slice(symMod.SourcePath, sym.SourceLine, source.DefaultWindow); err == nil && slice != "" {
					selected.SourceText = slice
					selected.SourcePath = symMod.SourcePath
				}
			}
			view.Selected = selected
			path, err := s.store.OIDPath(ctx, oid)
			if err != nil {
				s.internalError(w, r, err)
				return
			}
			view.OIDPath = path
		}
	}

	render(w, r, http.StatusOK, web.Workspace(view))
}

func (s *Server) handleSymbol(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	if rest == "" {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	module, name, ok := splitQualified(rest)
	if !ok {
		s.handleSymbolDisambiguation(w, r, rest)
		return
	}
	sym, err := s.store.GetSymbol(ctx, module, name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	view := &web.SymbolView{Symbol: sym}
	view.Context = s.buildSymbolContext(ctx, sym)
	if sym.Kind == model.KindTable {
		view.Columns = s.buildTableColumns(ctx, sym)
	}
	usedBy, err := s.store.ListReferencesTo(ctx, module, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	view.UsedBy = usedBy

	// Source slice for the "Show full SMI source" disclosure.
	if mod, err := s.store.GetModule(ctx, module); err == nil && mod.SourcePath != "" && sym.SourceLine > 0 {
		if slice, err := source.Slice(mod.SourcePath, sym.SourceLine, source.DefaultWindow); err == nil && slice != "" {
			view.SourceText = slice
			view.SourcePath = mod.SourcePath
		}
	}

	render(w, r, http.StatusOK, web.SymbolDetail(view))
}

// handleSymbolDisambiguation handles the `/s/{name}` form (no
// `Module::` prefix). One match → 302 to the canonical URL; multiple
// matches → chooser page; zero → 404. Spec R5 / spec scenario
// "Search by exact symbol".
func (s *Server) handleSymbolDisambiguation(w http.ResponseWriter, r *http.Request, name string) {
	matches, err := s.store.LookupByName(r.Context(), name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	switch len(matches) {
	case 0:
		s.notFound(w, r)
	case 1:
		// Single-match redirect lands in the workspace selection so
		// `/s/{name}` is consistent with the other Phase-3 nav
		// surfaces (search hits, ⌘K palette, /o/{oid}). Symbols
		// without an OID still resolve to /s/... via the helper.
		http.Redirect(w, r, string(web.WorkspaceSymbolURL(matches[0].ModuleName, matches[0].Name, matches[0].OID)), http.StatusFound)
	default:
		render(w, r, http.StatusOK, web.SymbolDisambiguation(name, matches))
	}
}

// buildSymbolContext computes the in-context block for a symbol —
// "Column N of X table, Indexed by Y, Augments Z" — entirely from
// stored data (parent_oid, IndexColumns, Augments).
func (s *Server) buildSymbolContext(ctx context.Context, sym *model.Symbol) *web.SymbolContext {
	out := &web.SymbolContext{}
	any := false

	// Walk up to find the table-entry parent (if column) or table parent.
	if sym.ParentOID != "" {
		parent, err := s.store.GetSymbolByOID(ctx, sym.ParentOID)
		if err == nil {
			switch {
			case parent.Kind == model.KindTableEntry:
				// We're a column. The table is parent's parent.
				if parent.ParentOID != "" {
					if grand, err := s.store.GetSymbolByOID(ctx, parent.ParentOID); err == nil && grand.Kind == model.KindTable {
						out.ParentTable = &web.SymbolRef{Module: grand.ModuleName, Name: grand.Name}
						out.ColumnNumber = lastOIDSegment(sym.OID)
						any = true
					}
				}
				// Inherit the index columns from the entry.
				for _, idx := range parent.IndexColumns {
					out.IndexedBy = append(out.IndexedBy, web.SymbolRef{Module: parent.ModuleName, Name: idx})
				}
				if len(parent.IndexColumns) > 0 {
					any = true
				}
			case parent.Kind == model.KindTable && sym.Kind == model.KindTableEntry:
				// We're an entry — point to the parent table.
				out.ParentTable = &web.SymbolRef{Module: parent.ModuleName, Name: parent.Name}
				any = true
			}
		}
	}

	// Direct entry-row data.
	if sym.Kind == model.KindTableEntry {
		for _, idx := range sym.IndexColumns {
			out.IndexedBy = append(out.IndexedBy, web.SymbolRef{Module: sym.ModuleName, Name: idx})
		}
		if len(sym.IndexColumns) > 0 {
			any = true
		}
	}

	if sym.Augments != "" {
		mod, name, ok := splitQualified(sym.Augments)
		if !ok {
			mod, name = sym.ModuleName, sym.Augments
		}
		out.Augments = &web.SymbolRef{Module: mod, Name: name}
		any = true
	}

	if !any {
		return nil
	}
	return out
}

// buildTableColumns returns the column rows for a SMIv2 table's
// symbol page. Columns are the children of the entry row, ordered by
// OID. Index columns get the IsIndex flag set.
func (s *Server) buildTableColumns(ctx context.Context, table *model.Symbol) []web.TableColumn {
	if table.Kind != model.KindTable {
		return nil
	}
	rows, err := s.store.ListChildren(ctx, table.OID)
	if err != nil {
		return nil
	}
	var entry *model.Symbol
	for i := range rows {
		if rows[i].Kind == model.KindTableEntry {
			entry = &rows[i]
			break
		}
	}
	if entry == nil {
		return nil
	}
	indexSet := make(map[string]bool, len(entry.IndexColumns))
	for _, n := range entry.IndexColumns {
		indexSet[n] = true
	}
	cols, err := s.store.ListChildren(ctx, entry.OID)
	if err != nil {
		return nil
	}
	out := make([]web.TableColumn, 0, len(cols))
	for _, c := range cols {
		out = append(out, web.TableColumn{
			Position: lastOIDSegment(c.OID),
			Module:   c.ModuleName,
			Name:     c.Name,
			Syntax:   c.Syntax,
			Access:   string(c.Access),
			Status:   string(c.Status),
			Units:    c.Units,
			IsIndex:  indexSet[c.Name],
		})
	}
	return out
}

func (s *Server) handleOID(w http.ResponseWriter, r *http.Request) {
	oid := strings.TrimPrefix(r.URL.Path, "/o/")
	if oid == "" {
		s.notFound(w, r)
		return
	}
	sym, err := s.store.GetSymbolByOID(r.Context(), oid)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// Redirect to the workspace selection rather than the canonical
	// /s/... page so the user lands in the navigation context that
	// owns the OID. The /s/... page remains for direct deep links.
	http.Redirect(w, r, "/m/"+sym.ModuleName+"/"+sym.OID, http.StatusFound)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		render(w, r, http.StatusOK, web.SearchEmpty())
		return
	}
	ctx := r.Context()
	hits := s.searchWithExactMatch(ctx, q, 50)
	if len(hits) == 0 {
		// Fall through to "did you mean": Levenshtein-against-name
		// candidates. Errors here are non-fatal — the no-results
		// page is still useful without suggestions.
		suggestions, err := s.store.DidYouMean(ctx, q, 5)
		if err != nil {
			slog.Warn("did-you-mean failed", "q", q, "err", err)
		}
		render(w, r, http.StatusOK, web.SearchNoResults(q, toWebHits(suggestions)))
		return
	}
	render(w, r, http.StatusOK, web.SearchResults(q, toWebHits(hits)))
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mods, err := s.store.ListModules(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var groups []web.ModuleDiagnostics
	for _, m := range mods {
		if m.ParseStatus == model.ParseStatusClean {
			continue
		}
		diags, err := s.store.ListDiagnosticsByModule(ctx, m.Name)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		groups = append(groups, web.ModuleDiagnostics{Module: m, Diagnostics: diags})
	}
	render(w, r, http.StatusOK, web.Diagnostics(groups))
}

// --- tree page -------------------------------------------------------

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/tree")
	rest = strings.TrimPrefix(rest, "/")
	render(w, r, http.StatusOK, web.TreePage(rest))
}

// --- JSON API --------------------------------------------------------

func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"hits": []any{}})
		return
	}
	hits := s.searchWithExactMatch(r.Context(), q, 25)
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

// handleAPITreeFragment returns the immediate children of an OID
// as an HTML <ul> fragment, suitable for HTMX `beforeend` swap into
// the workspace tree row that triggered the expansion. The
// JSON-returning sibling `handleAPITree` is preserved for the
// standalone tree page.
func (s *Server) handleAPITreeFragment(w http.ResponseWriter, r *http.Request) {
	parent := strings.TrimSpace(r.URL.Query().Get("parent"))
	if parent == "" {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	children, err := s.store.ListChildren(ctx, parent)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	parentOIDs := make([]string, 0, len(children))
	for i := range children {
		parentOIDs = append(parentOIDs, children[i].OID)
	}
	hasChildren, err := s.store.HasChildrenBatch(ctx, parentOIDs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	rows := make([]web.TreeRow, 0, len(children))
	for i := range children {
		rows = append(rows, web.TreeRow{
			Symbol:      children[i],
			HasChildren: hasChildren[children[i].OID],
		})
	}
	render(w, r, http.StatusOK, web.WorkspaceTreeFragment(rows))
}

// handleAPITree returns the immediate children of an OID as JSON,
// suitable for lazy-load expansion in the tree.js island.
//
// The default parent is "1" (the root of the OID space). For each
// child we report whether it has further descendants so the client
// can decide whether to render an expand chevron.
func (s *Server) handleAPITree(w http.ResponseWriter, r *http.Request) {
	parent := strings.TrimSpace(r.URL.Query().Get("parent"))
	if parent == "" {
		parent = "1"
	}
	ctx := r.Context()

	children, err := s.store.ListChildren(ctx, parent)
	if err != nil {
		s.apiError(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}

	type item struct {
		OID         string `json:"oid"`
		Name        string `json:"name"`
		Module      string `json:"module"`
		Kind        string `json:"kind"`
		HasChildren bool   `json:"hasChildren"`
		Position    string `json:"position"`
	}
	out := make([]item, 0, len(children))
	for _, c := range children {
		hc, _ := s.store.HasChildren(ctx, c.OID)
		out = append(out, item{
			OID:         c.OID,
			Name:        c.Name,
			Module:      c.ModuleName,
			Kind:        string(c.Kind),
			HasChildren: hc,
			Position:    lastOIDSegment(c.OID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"parent":   parent,
		"children": out,
	})
}

func (s *Server) handleAPISymbol(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/symbol/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		s.apiError(w, r, http.StatusBadRequest, "expected /api/v1/symbol/{module}/{name}", nil)
		return
	}
	sym, err := s.store.GetSymbol(r.Context(), parts[0], parts[1])
	if errors.Is(err, store.ErrNotFound) {
		s.apiError(w, r, http.StatusNotFound, "symbol not found", nil)
		return
	}
	if err != nil {
		s.apiError(w, r, http.StatusInternalServerError, "internal error", err)
		return
	}
	writeJSON(w, http.StatusOK, sym)
}

// searchWithExactMatch first tries to interpret the query as a
// qualified Module::Name lookup; if it hits, the exact match is
// prepended to the FTS5 results so the user always sees their typed
// symbol on top. FTS5 BM25 alone doesn't guarantee exact-match-first
// ranking — see spec R5 scenario "Search by exact symbol".
//
// Queries that look like an OID prefix (digits and dots, optionally
// led by a single dot) bypass FTS5 entirely — FTS5's tokenizer
// strips dots, so an OID-shaped query against the inverted index
// would either match nothing or wildly over-match. The store's
// SearchByOIDPrefix uses LIKE on the indexed `oid` column instead.
func (s *Server) searchWithExactMatch(ctx context.Context, q string, limit int) []store.SearchHit {
	if prefix, ok := oidPrefixQuery(q); ok {
		hits, err := s.store.SearchByOIDPrefix(ctx, prefix, limit)
		if err != nil {
			slog.Warn("oid prefix search failed", "q", q, "err", err)
			return nil
		}
		return hits
	}

	hits, err := s.store.Search(ctx, q, limit)
	if err != nil {
		slog.Warn("search failed", "q", q, "err", err)
	}

	if module, name, ok := splitQualified(q); ok {
		if sym, err := s.store.GetSymbol(ctx, module, name); err == nil {
			exact := store.SearchHit{
				SymbolID: sym.ID,
				Module:   sym.ModuleName,
				Name:     sym.Name,
				OID:      sym.OID,
				Kind:     string(sym.Kind),
			}
			for i, h := range hits {
				if h.SymbolID == sym.ID {
					hits = append(hits[:i], hits[i+1:]...)
					break
				}
			}
			hits = append([]store.SearchHit{exact}, hits...)
		}
	}
	return hits
}

// --- error pages -----------------------------------------------------

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusNotFound, web.NotFound())
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("handler failed", "path", r.URL.Path, "err", err)
	render(w, r, http.StatusInternalServerError, web.InternalError(err.Error()))
}

// --- helpers ---------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// apiError writes a sanitised JSON error body. The public message is
// what the API client sees; if err is non-nil it goes to slog only —
// preventing internal paths, identifiers, or query fragments from
// leaking through `/api/v1/*`.
func (s *Server) apiError(w http.ResponseWriter, r *http.Request, status int, public string, err error) {
	if err != nil {
		slog.Error("api error",
			"path", r.URL.Path,
			"status", status,
			"err", err,
		)
	}
	writeJSON(w, status, map[string]any{"error": public})
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		slog.Error("render failed", "path", r.URL.Path, "err", err)
	}
}

func toWebHits(hits []store.SearchHit) []web.SearchHit {
	out := make([]web.SearchHit, len(hits))
	for i, h := range hits {
		out[i] = web.SearchHit{
			Module: h.Module,
			Name:   h.Name,
			OID:    h.OID,
			Kind:   h.Kind,
			// Sanitise the FTS5 snippet — preserves <mark>...</mark>
			// markers, escapes everything else. Rendered via
			// templ.Raw in SearchResults, so without this the
			// description text would XSS.
			Snippet: web.SanitizeSnippet(h.Snippet),
		}
	}
	return out
}

// splitQualified parses "Module::Name" into its parts. If only a bare
// name is provided (no "::"), returns ok=false and the caller should
// fall back to a search-by-name strategy.
func splitQualified(s string) (module, name string, ok bool) {
	i := strings.Index(s, "::")
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+2:], true
}

// oidPrefixQuery returns (prefix, true) when q looks like a numeric
// OID — bare digits like "1.3.6.1" or with a leading dot like ".1".
// The store's SearchByOIDPrefix expects the leading dot stripped.
//
// Returning false here means the query goes through FTS5; an empty
// or single-dot input is rejected so we don't widen the search to
// every symbol in the database.
func oidPrefixQuery(q string) (string, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", false
	}
	q = strings.TrimPrefix(q, ".")
	if q == "" {
		return "", false
	}
	for _, r := range q {
		if !(r >= '0' && r <= '9') && r != '.' {
			return "", false
		}
	}
	if strings.HasPrefix(q, ".") || strings.HasSuffix(q, ".") || strings.Contains(q, "..") {
		return "", false
	}
	return q, true
}

// lastOIDSegment returns the trailing dot-separated component of an
// OID — e.g. "10" for "1.3.6.1.2.1.2.2.1.10". Used as the column
// position number on the table-of-tables rendering.
func lastOIDSegment(oid string) string {
	if i := strings.LastIndex(oid, "."); i >= 0 {
		return oid[i+1:]
	}
	return oid
}
