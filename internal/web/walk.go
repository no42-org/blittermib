package web

import (
	"fmt"

	"github.com/a-h/templ"
)

// View models for the walk decoder pages. All display strings are
// precomputed by the handler so the templates stay logic-free.

// WalkUploadView backs the GET /walk upload page.
type WalkUploadView struct {
	Error string // non-empty after a rejected submit (e.g. empty/oversized)
}

// WalkResultsView backs the POST /walk/decode results page.
type WalkResultsView struct {
	Summary       string              // "12 entries · 9 resolved · 2 modules"
	SkippedLines  int                 // lines the parser couldn't read
	ParserNotes   []string            // soft warnings
	Groups        []WalkModuleGroup   // resolved entries grouped by module
	Unresolved    []WalkUnresolvedRow // OIDs no loaded module covers
	Notifications []WalkNotifModule   // §5 derived notification/trap modules
	WalkText      string              // echoed back into the bundle form
	WalkDataJSON  string              // {"oids":{oid:value}} for the localStorage overlay
	HasResults    bool                // any resolved or unresolved rows at all
}

// WalkModuleGroup is the resolved rows of one module.
type WalkModuleGroup struct {
	Module string
	Rows   []WalkRow
}

// WalkRow is one resolved walk entry, rendered as a table row.
type WalkRow struct {
	OID        string
	Symbol     string
	Index      string // "ifIndex=1", "suffix=.1", ".0", or ""
	Type       string
	Value      string
	NotPresent bool
}

// WalkUnresolvedRow aggregates occurrences of an OID prefix that no
// loaded module covers, with the PEN/canonical guidance.
type WalkUnresolvedRow struct {
	OID   string
	Count int
	Hint  string // "PEN 9 (ciscoSystems) — load a vendor MIB", etc.
}

// WalkNotifModule names a module relevant to the device that defines
// notifications or traps.
type WalkNotifModule struct {
	Module string
	Count  string // "2 notification/trap definitions"
}

// moduleHref builds the workspace link for a module name. The name has
// already passed SMI-grammar validation upstream, so this is a plain
// join wrapped in templ's URL sanitiser.
func moduleHref(module string) templ.SafeURL {
	return templ.URL("/m/" + module)
}

func skippedNote(n int) string {
	return fmt.Sprintf("%d line(s) in the capture were not recognised and were skipped.", n)
}

func countLabel(n int) string {
	if n == 1 {
		return "1 occurrence"
	}
	return fmt.Sprintf("%d occurrences", n)
}
