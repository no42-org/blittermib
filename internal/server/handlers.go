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
	if strings.HasSuffix(rest, "/source") {
		s.handleModuleSource(w, r, strings.TrimSuffix(rest, "/source"))
		return
	}
	s.handleModuleDetail(w, r, rest)
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

func (s *Server) handleModuleDetail(w http.ResponseWriter, r *http.Request, name string) {
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
	render(w, r, http.StatusOK, web.ModuleDetail(mod, syms))
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
	if sym.IsTable {
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
		http.Redirect(w, r, "/s/"+matches[0].QualifiedName(), http.StatusFound)
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
			case parent.IsTableEntry:
				// We're a column. The table is parent's parent.
				if parent.ParentOID != "" {
					if grand, err := s.store.GetSymbolByOID(ctx, parent.ParentOID); err == nil && grand.IsTable {
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
			case parent.IsTable && sym.IsTableEntry:
				// We're an entry — point to the parent table.
				out.ParentTable = &web.SymbolRef{Module: parent.ModuleName, Name: parent.Name}
				any = true
			}
		}
	}

	// Direct entry-row data.
	if sym.IsTableEntry {
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
	if !table.IsTable {
		return nil
	}
	rows, err := s.store.ListChildren(ctx, table.OID)
	if err != nil {
		return nil
	}
	var entry *model.Symbol
	for i := range rows {
		if rows[i].IsTableEntry {
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
	http.Redirect(w, r, "/s/"+sym.QualifiedName(), http.StatusFound)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		render(w, r, http.StatusOK, web.SearchEmpty())
		return
	}
	hits := s.searchWithExactMatch(r.Context(), q, 50)
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
func (s *Server) searchWithExactMatch(ctx context.Context, q string, limit int) []store.SearchHit {
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
			Module:  h.Module,
			Name:    h.Name,
			OID:     h.OID,
			Kind:    h.Kind,
			Snippet: h.Snippet,
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

// lastOIDSegment returns the trailing dot-separated component of an
// OID — e.g. "10" for "1.3.6.1.2.1.2.2.1.10". Used as the column
// position number on the table-of-tables rendering.
func lastOIDSegment(oid string) string {
	if i := strings.LastIndex(oid, "."); i >= 0 {
		return oid[i+1:]
	}
	return oid
}
