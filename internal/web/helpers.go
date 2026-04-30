package web

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"unicode"

	"github.com/a-h/templ"

	"github.com/no42-org/blittermib/internal/model"
)

// moduleURL returns the canonical URL for a module's detail page.
//
// templ.SafeURL marks the value as already safe for href attributes;
// our inputs are SMI module names (alphanumeric + dash) and are
// therefore URL-safe without further escaping.
func moduleURL(name string) templ.SafeURL {
	return templ.SafeURL("/m/" + name)
}

// symbolURL returns the canonical URL for a symbol's detail page.
func symbolURL(module, name string) templ.SafeURL {
	return templ.SafeURL("/s/" + module + "::" + name)
}

// moduleSourceURL returns the URL for a module's raw-source page.
func moduleSourceURL(module string) templ.SafeURL {
	return templ.SafeURL("/m/" + module + "/source")
}

// workspaceURL returns the URL for a workspace selection. SMI
// module names are alphanumeric + dash and OIDs are digits + dot,
// so neither input needs URL escaping.
//
// Symbols with no OID (textual conventions, some object groups)
// can't deep-link via /m/{name}/{oid}; for those we fall back to
// the canonical /s/{module}::{name} so detail still renders. The
// alternative — landing on the empty workspace — silently drops
// the user's selection.
func workspaceURL(module, oid string) templ.SafeURL {
	if oid == "" {
		return symbolURL(module, "")
	}
	return templ.SafeURL("/m/" + module + "/" + oid)
}

// workspaceSymbolURL is `workspaceURL` with the name available so the
// fall-back to /s/{module}::{name} can be properly qualified when
// the symbol has no OID.
func workspaceSymbolURL(module, name, oid string) templ.SafeURL {
	if oid == "" {
		return symbolURL(module, name)
	}
	return templ.SafeURL("/m/" + module + "/" + oid)
}

// WorkspaceSymbolURL is the exported form of workspaceSymbolURL,
// used by handlers (which sit outside the package's templ files).
func WorkspaceSymbolURL(module, name, oid string) templ.SafeURL {
	return workspaceSymbolURL(module, name, oid)
}

// treeFragmentURL is the HTMX target that returns the children of
// an OID rendered as workspace tree-rows.
func treeFragmentURL(parentOID string) templ.SafeURL {
	return templ.SafeURL("/api/v1/tree/fragment?parent=" + parentOID)
}

// stepDisplayName picks the readable label for an OID-decode step.
// Falls back to the bare numeric segment when neither a loaded
// symbol nor the canonical table covers the prefix.
func stepDisplayName(s model.OIDStep) string {
	if s.Name != "" {
		return s.Name
	}
	return lastSegment(s.Prefix)
}

// lastSegment returns the final dotted segment of an OID, or the
// full string when there's no dot.
func lastSegment(oid string) string {
	if i := strings.LastIndex(oid, "."); i >= 0 {
		return oid[i+1:]
	}
	return oid
}

// IsTabular reports whether the kind names a symbol that participates
// in SMIv2 conceptual-row table rendering. The three answers — table,
// table-entry, column — are grouped here so templates and handlers
// don't fan out into kind-by-kind switches when they want a coarse
// "is this part of a table" predicate.
func IsTabular(k model.SymbolKind) bool {
	switch k {
	case model.KindTable, model.KindTableEntry, model.KindColumn:
		return true
	}
	return false
}

// FamilyClass returns the type-family CSS class for a symbol —
// `t-counter`, `t-gauge`, `t-int`, `t-text`, `t-index`, `t-time`,
// `t-addr`, `t-bool`, `t-notif`, or `t-struct`. Templates emit
// `class={ "row " + FamilyClass(s) }` so Phase-1's `--c-*` color
// tokens reach the rendered DOM.
//
// isIndex defaults to false here because most call sites don't have
// the parent entry's IndexColumns in scope. The status-bar count
// helper passes false too. A future refinement can thread the bool
// through tree/list rendering when accessing the parent row's
// IndexColumns is cheap.
func FamilyClass(s *model.Symbol) string {
	if s == nil {
		return "t-struct"
	}
	return model.TypeFamily(s.Kind, s.Syntax, false)
}

// fmtLine renders a line number for diagnostics templates without
// inlining strconv.Itoa noise into every template.
func fmtLine(n int) string {
	return strconv.Itoa(n)
}

// fmtInt64 renders an int64 in base 10 for templ expressions. Used
// for enum-value numbers (`name(value)`) — a separate helper from
// fmtLine so changes to diagnostic line formatting don't silently
// reshape enum rendering, and so model.EnumValue.Number's full
// int64 range survives without truncation on 32-bit builds.
func fmtInt64(n int64) string {
	return strconv.FormatInt(n, 10)
}

// SymbolRef is a lightweight cross-reference shape for in-template
// linking — keeps templates independent of the bigger model.Symbol.
type SymbolRef struct {
	Module string
	Name   string
}

// SymbolContext captures "where in the SMI tree does this symbol sit"
// for the in-context block on the symbol page (Column N of X table,
// Indexed by …, Augments …).
type SymbolContext struct {
	ColumnNumber string
	ParentTable  *SymbolRef
	IndexedBy    []SymbolRef
	Augments     *SymbolRef
}

// TableColumn is one row in the table-of-tables rendering on a SMIv2
// table's symbol page.
type TableColumn struct {
	Position string
	Module   string
	Name     string
	Syntax   string
	Access   string
	Status   string
	Units    string
	IsIndex  bool
}

// TreeRow is one node in the workspace's left-rail OID tree. The
// initial paint includes only the module's top-level OID children;
// HasChildren drives whether a chevron renders so the user can
// drill in via lazy HTMX-fragment expansion.
type TreeRow struct {
	Symbol      model.Symbol
	HasChildren bool
}

// WorkspaceView aggregates everything the workspace shell needs for
// a single page render. Built by Server.handleWorkspace.
type WorkspaceView struct {
	Module   *model.Module
	Counts   *model.FamilyCounts
	TreeRows []TreeRow
	ListRows []model.Symbol
	Selected *SymbolView // nil → empty-state right pane
	OIDPath  []model.OIDStep
	Modules  []model.Module // preloaded for the status-bar picker
	// MissingOID is set when the URL specifies an OID the module
	// doesn't cover; the workspace renders without selection and a
	// soft hint in the right pane.
	MissingOID string
}

// SummarizeSymbol produces the one-sentence plain-language lede that
// sits between the symbol name and its OID on the symbol page —
// design.md's "novel for this product" entry-point line.
//
// Heuristic, in order of preference:
//   - first sentence of Description (up to 200 chars; truncated at a
//     word boundary if the sentence runs long)
//   - "{kind} in {module}" fallback when no description is present
//
// The sentence finder respects "." inside quoted text only as a
// best-effort: SMI descriptions occasionally embed example dotted
// notation, but the visible truncation is acceptable cost for a
// summary that almost always reads cleanly.
func SummarizeSymbol(s *model.Symbol) string {
	if s == nil {
		return ""
	}
	desc := strings.TrimSpace(collapseWhitespace(s.Description))
	if desc == "" {
		return fmt.Sprintf("%s in %s.", string(s.Kind), s.ModuleName)
	}
	first := firstSentence(desc)
	if utf8Count(first) > 200 {
		first = truncateWord(first, 200) + "…"
	}
	return first
}

// firstSentence returns the prefix of s up to the first sentence-
// ending punctuation, inclusive. If no terminator is found, the
// whole string is returned.
func firstSentence(s string) string {
	for i, r := range s {
		switch r {
		case '.', '!', '?':
			// Avoid splitting on something like "v2." inside a phrase
			// — only stop if followed by whitespace or end-of-string.
			next := i + 1
			if next >= len(s) {
				return s[:i+1]
			}
			if unicode.IsSpace(rune(s[next])) {
				return s[:i+1]
			}
		}
	}
	return s
}

// truncateWord truncates s to at most n runes at the nearest preceding
// word boundary, dropping any trailing whitespace.
func truncateWord(s string, n int) string {
	if utf8Count(s) <= n {
		return s
	}
	out := []rune(s)[:n]
	for i := len(out) - 1; i > 0; i-- {
		if unicode.IsSpace(out[i]) {
			return strings.TrimRightFunc(string(out[:i]), unicode.IsSpace)
		}
	}
	return string(out)
}

// collapseWhitespace replaces runs of whitespace with a single space.
// SMI descriptions are typically wrapped to ~70 chars with hard
// newlines; rendering them to a one-line summary requires unwrapping.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

func utf8Count(s string) int {
	return len([]rune(s))
}

// FormatOIDHTML wraps each `.` separator in `<span class="dot">.</span>`
// so the design system's accent CSS rule (`.oid .dot { color: var(--accent) }`)
// applies. The input is HTML-escaped first; OIDs are restricted to
// digits and dots in practice, but defending against contamination
// is cheap.
//
// Returned string is safe to render via templ.Raw.
func FormatOIDHTML(oid string) string {
	if oid == "" {
		return ""
	}
	safe := html.EscapeString(oid)
	return strings.ReplaceAll(safe, ".", `<span class="dot">.</span>`)
}

// SanitizeSnippet HTML-escapes the FTS5 snippet body while preserving
// the inserted <mark>...</mark> highlight tags. SQLite's snippet()
// emits the raw column contents (which may contain `<` or `>` from a
// MIB description) wrapped with the markers we passed in — without
// this sanitisation, we'd be embedding unescaped MIB text in HTML.
//
// Returned string is safe to render via templ.Raw.
func SanitizeSnippet(s string) string {
	if s == "" {
		return ""
	}
	const (
		openSentinel  = "\x01MARK_OPEN\x01"
		closeSentinel = "\x02MARK_CLOSE\x02"
	)
	s = strings.ReplaceAll(s, "<mark>", openSentinel)
	s = strings.ReplaceAll(s, "</mark>", closeSentinel)
	s = html.EscapeString(s)
	s = strings.ReplaceAll(s, openSentinel, "<mark>")
	s = strings.ReplaceAll(s, closeSentinel, "</mark>")
	return s
}
