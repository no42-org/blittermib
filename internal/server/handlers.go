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
	render(w, r, http.StatusOK, web.Landing(modCount, symCount))
}

func (s *Server) handleModule(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/m")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		s.handleModuleIndex(w, r)
		return
	}
	s.handleModuleDetail(w, r, rest)
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
	module, name, ok := splitQualified(rest)
	if !ok {
		http.Error(w, "ambiguous name; use /s/Module::Name", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	sym, err := s.store.GetSymbol(ctx, module, name)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	usedBy, err := s.store.ListReferencesTo(ctx, module, name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	render(w, r, http.StatusOK, web.SymbolDetail(sym, usedBy))
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
	hits, err := s.store.Search(r.Context(), q, 50)
	if err != nil {
		s.internalError(w, r, err)
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

// --- JSON API --------------------------------------------------------

func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"hits": []any{}})
		return
	}
	hits, err := s.store.Search(r.Context(), q, 25)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func (s *Server) handleAPISymbol(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/symbol/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected /api/v1/symbol/{module}/{name}"})
		return
	}
	sym, err := s.store.GetSymbol(r.Context(), parts[0], parts[1])
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "symbol not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sym)
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

// render writes a templ component to w with the right Content-Type and
// status. Any rendering error is logged but cannot be reported to the
// client because headers and partial body have already been written.
func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		slog.Error("render failed", "path", r.URL.Path, "err", err)
	}
}

// toWebHits converts store.SearchHit (which carries DB-shape rank /
// snippet types) to the web.SearchHit shape the template expects.
// Keeping the two types separate avoids leaking DB types into the
// rendering layer.
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

// _ keeps context import in use even when no template currently
// constructs a context-derived value directly in handlers.
var _ = context.Background
