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

// WalkResultsView backs the POST /walk/decode results page. The page is
// a per-module summary + launcher (Decision 12): the decoded values
// themselves are explored in each module's workspace via the walk
// overlay, not listed here.
type WalkResultsView struct {
	Summary       string              // "12 entries · 9 resolved · 2 modules"
	SkippedLines  int                 // lines the parser couldn't read
	ParserNotes   []string            // soft warnings
	Modules       []WalkModuleSummary // one row per resolved module
	Unresolved    []WalkUnresolvedRow // OIDs no loaded module covers
	Notifications []WalkNotifModule   // §5 derived notification/trap modules
	WalkText      string              // echoed back into the bundle form
	WalkDataJSON  string              // {"oids":{oid:value}} for the sessionStorage overlay
	HasResults    bool                // any resolved module or unresolved row
}

// WalkModuleSummary is one clickable row in the results launcher: a
// module the walk touched, with counts, linking into its workspace.
// ObjectCount/ValueCount are exposed as row data attributes so the
// results page can be sorted client-side.
type WalkModuleSummary struct {
	Module      string
	ObjectCount int
	ValueCount  int
	Counts      string // "4 objects · 10 values"
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

// moduleWalkHref is the workspace link (see moduleURL) plus the
// `#in-walk` hash, which the walk overlay reads on the workspace to
// pre-apply the "in walk" filter. The hash never reaches the server
// (Decision 5/12).
func moduleWalkHref(module string) templ.SafeURL {
	return templ.URL("/m/" + module + "#in-walk")
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
