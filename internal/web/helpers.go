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
