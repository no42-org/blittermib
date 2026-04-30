package web

import (
	"strconv"

	"github.com/a-h/templ"
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
