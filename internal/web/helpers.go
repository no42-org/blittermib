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

// fmtLine renders a line number for diagnostics templates without
// inlining strconv.Itoa noise into every template.
func fmtLine(n int) string {
	return strconv.Itoa(n)
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
