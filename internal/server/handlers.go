package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/no42-org/blittermib/internal/store"
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

// --- page handlers (placeholder HTML — templ replaces these) --------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	modCount, _ := s.store.CountModules(ctx)
	symCount, _ := s.store.CountSymbols(ctx)
	renderPage(w, http.StatusOK, "blittermib", fmt.Sprintf(
		`<h1 class="hero-brand">blittermib<span class="brand-dot">.</span></h1>
<p class="hero-tagline">Browse SNMP MIBs, beautifully.</p>
<p class="hero-stats"><strong>%d</strong> modules · <strong>%d</strong> symbols</p>`,
		modCount, symCount))
}

func (s *Server) handleModule(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/m")
	rest = strings.TrimPrefix(rest, "/")
	switch {
	case rest == "":
		s.handleModuleIndex(w, r)
	default:
		s.handleModuleDetail(w, r, rest)
	}
}

func (s *Server) handleModuleIndex(w http.ResponseWriter, r *http.Request) {
	mods, err := s.store.ListModules(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString(`<h1>Modules</h1><ul>`)
	for _, m := range mods {
		fmt.Fprintf(&b, `<li><a href="/m/%s">%s</a> <span class="muted">%s</span></li>`,
			m.Name, m.Name, m.ParseStatus)
	}
	b.WriteString(`</ul>`)
	renderPage(w, http.StatusOK, "Modules · blittermib", b.String())
}

func (s *Server) handleModuleDetail(w http.ResponseWriter, r *http.Request, name string) {
	mod, err := s.store.GetModule(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	syms, _ := s.store.ListSymbolsByModule(r.Context(), name)

	var b strings.Builder
	fmt.Fprintf(&b, `<nav class="breadcrumb"><a href="/m">Modules</a> › %s</nav>`, mod.Name)
	fmt.Fprintf(&b, `<h1 class="symbol-name">%s</h1>`, mod.Name)
	if mod.Description != "" {
		fmt.Fprintf(&b, `<p class="summary">%s</p>`, htmlEscape(mod.Description))
	}
	fmt.Fprintf(&b, `<p class="oid">%s</p>`, mod.OIDRoot)
	fmt.Fprintf(&b, `<h2 class="section-label">Symbols (%d)</h2><ul>`, len(syms))
	for _, sy := range syms {
		fmt.Fprintf(&b, `<li><a href="/s/%s::%s"><code>%s</code></a> <span class="muted">%s</span></li>`,
			sy.ModuleName, sy.Name, sy.Name, sy.Kind)
	}
	b.WriteString(`</ul>`)
	renderPage(w, http.StatusOK, mod.Name+" · blittermib", b.String())
}

func (s *Server) handleSymbol(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/s/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	module, name, ok := splitQualified(rest)
	if !ok {
		http.Error(w, "ambiguous name; use /s/Module::Name", http.StatusBadRequest)
		return
	}
	sym, err := s.store.GetSymbol(r.Context(), module, name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	usedBy, _ := s.store.ListReferencesTo(r.Context(), module, name)

	var b strings.Builder
	fmt.Fprintf(&b, `<nav class="breadcrumb"><a href="/m/%s">%s</a> › %s</nav>`, sym.ModuleName, sym.ModuleName, sym.Name)
	fmt.Fprintf(&b, `<h1 class="symbol-name">%s</h1>`, sym.Name)
	fmt.Fprintf(&b, `<div class="oid">%s</div>`, sym.OID)
	fmt.Fprintf(&b, `<div class="type-line">%s · %s · %s`, sym.Syntax, sym.Access, sym.Status)
	if sym.Units != "" {
		fmt.Fprintf(&b, ` · units: %s`, sym.Units)
	}
	b.WriteString(`</div>`)
	if sym.Description != "" {
		fmt.Fprintf(&b, `<h2 class="section-label">Description</h2><div class="prose"><p>%s</p></div>`, htmlEscape(sym.Description))
	}
	if len(usedBy) > 0 {
		b.WriteString(`<h2 class="section-label">Used by</h2><table class="refs"><tbody>`)
		for _, ref := range usedBy {
			fmt.Fprintf(&b, `<tr><td><a href="/s/%s::%s"><code>%s</code></a></td><td class="kind">%s</td><td class="module">%s</td></tr>`,
				ref.SourceModule, ref.SourceName, ref.SourceName, ref.Kind, ref.SourceModule)
		}
		b.WriteString(`</tbody></table>`)
	}
	renderPage(w, http.StatusOK, sym.QualifiedName()+" · blittermib", b.String())
}

func (s *Server) handleOID(w http.ResponseWriter, r *http.Request) {
	oid := strings.TrimPrefix(r.URL.Path, "/o/")
	if oid == "" {
		http.NotFound(w, r)
		return
	}
	sym, err := s.store.GetSymbolByOID(r.Context(), oid)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/s/"+sym.QualifiedName(), http.StatusFound)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		renderPage(w, http.StatusOK, "Search · blittermib",
			`<h1>Search</h1><p class="muted">Enter a query in the URL ?q=…</p>`)
		return
	}
	hits, err := s.store.Search(r.Context(), q, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<h1>Search results for <code>%s</code></h1>`, htmlEscape(q))
	if len(hits) == 0 {
		b.WriteString(`<p class="muted">No matches.</p>`)
	} else {
		b.WriteString(`<ul>`)
		for _, h := range hits {
			fmt.Fprintf(&b, `<li><a href="/s/%s::%s"><code>%s</code></a> <span class="muted">%s · %s</span><br>%s</li>`,
				h.Module, h.Name, h.Name, h.Module, h.Kind, h.Snippet)
		}
		b.WriteString(`</ul>`)
	}
	renderPage(w, http.StatusOK, "Search · blittermib", b.String())
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	// For v1: list modules and surface those with non-clean parse status.
	mods, err := s.store.ListModules(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString(`<h1>Diagnostics</h1>`)
	hasIssues := false
	for _, m := range mods {
		if m.ParseStatus == "clean" {
			continue
		}
		hasIssues = true
		diags, _ := s.store.ListDiagnosticsByModule(r.Context(), m.Name)
		fmt.Fprintf(&b, `<h2><a href="/m/%s">%s</a> <span class="muted">%s</span></h2>`, m.Name, m.Name, m.ParseStatus)
		b.WriteString(`<ul>`)
		for _, d := range diags {
			fmt.Fprintf(&b, `<li><code>%s:%d</code> %s: %s</li>`,
				d.File, d.Line, d.Severity, htmlEscape(d.Message))
		}
		b.WriteString(`</ul>`)
	}
	if !hasIssues {
		b.WriteString(`<p class="muted">All modules parsed cleanly.</p>`)
	}
	renderPage(w, http.StatusOK, "Diagnostics · blittermib", b.String())
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

// --- helpers ---------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// renderPage wraps body content in a minimal HTML shell that matches
// the prototype's CSS hooks. templ templates will replace this once
// generation is wired up; until then it lets the routes return real
// pages backed by store data.
func renderPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en"><head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<link rel="stylesheet" href="/static/styles.css">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600&family=Geist+Mono:wght@400;500&display=swap" rel="stylesheet">
</head><body>
<header class="topbar">
  <a href="/" class="brand">blittermib<span class="brand-dot">.</span></a>
  <span class="brand-tagline">Pixelperfect MIB browser</span>
  <div class="topbar-spacer"></div>
</header>
<main class="page"><div class="content-inner">%s</div></main>
<footer class="footer">
  <span><a href="https://github.com/no42-org/blittermib" target="_blank" rel="noopener noreferrer">blittermib</a> · runs entirely on your server</span>
  <span>Made with AI for Open Source in Europe</span>
</footer>
</body></html>`, htmlEscape(title), body)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
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
