package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
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

// moduleDownloadURL returns the URL for the single-MIB download
// endpoint. Module names are URL-safe per RFC 1212 §4.1.6 — no
// encoding needed in practice — but `url.PathEscape` keeps the
// surface consistent with the other URL builders.
func moduleDownloadURL(module string) templ.SafeURL {
	return templ.SafeURL("/m/" + url.PathEscape(module) + "/download")
}

// moduleBundleURL returns the URL for the bundle ZIP download.
func moduleBundleURL(module string) templ.SafeURL {
	return templ.SafeURL("/m/" + url.PathEscape(module) + "/download.zip")
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

// KindHasChildren is the kind-based heuristic for "does clicking
// this row drill INTO a subtree, or does it just select a leaf?"
// Containers (table / table-entry / object-identity / module-
// identity) drill in (scope change). Everything else is a leaf.
//
// The heuristic matches the SMI tree shape almost universally,
// with the rare exception of an OBJECT-IDENTITY anchor that has
// no children — clicking it would scope to itself and show an
// empty list, which is harmless.
func KindHasChildren(k model.SymbolKind) bool {
	switch k {
	case model.KindTable, model.KindTableEntry,
		model.KindObjectIdentity, model.KindModuleIdentity:
		return true
	}
	return false
}

// WorkspaceRowURL builds the URL for clicking a row in either the
// workspace tree or the list pane. The two surfaces share the same
// click semantic — keep the UI predictable — so they share this
// builder.
//
// Behavior:
//   - Container kinds (table, entry, object-identity, module-identity)
//     navigate as a scope change: `/m/{name}/{oid}`. The list and
//     breadcrumb both refocus on the new scope.
//   - Leaf kinds (column, scalar, notification, etc.) preserve the
//     current scope ONLY when the leaf is actually under that
//     scope — `/m/{name}/{currentScope}?sel={oid}`. This matches
//     the handoff workflow where clicking a column inside a scoped
//     table just swaps the detail pane while keeping the list put.
//   - When the leaf is in a different subtree (the user clicked a
//     notification while scoped to an unrelated table, or there's
//     no scope at all), the URL switches scope to the leaf's parent
//     OID so the list pane shows the leaf's siblings with the leaf
//     highlighted. Clicking `linkDown` from `/m/IF-MIB/{interfaces}`
//     lands on `/m/IF-MIB/{snmpTraps}?sel={linkDown}` — the
//     "browse onward" workflow the user expects (here are the trap
//     OIDs, you clicked this one). Without this, the previous scope
//     would persist and the list would show interfaces while the
//     right pane shows linkDown — visibly disjoint.
//   - Leaves with no parent OID (rare: a top-level OID-bearing
//     symbol) fall back to the module-root view with the symbol
//     selected.
//   - Symbols with no OID (textual conventions, some object groups)
//     are module-level definitions and don't slot into an OID-
//     keyed scope path. They ALWAYS navigate to `/m/{module}?sel={name}`
//     — the current scope is cleared so the list pane shows the
//     full module symbol set with the no-OID symbol highlighted,
//     rather than stranding the user on an unrelated scoped list
//     where the symbol can't appear. The handler distinguishes
//     OID vs. name selectors by whether the value starts with a
//     digit — SMI names must start with a letter per RFC 1212
//     §4.1.6 / RFC 2578 §3.1, so the first-char check is
//     unambiguous. This keeps TC clicks inside the workspace
//     shell instead of bouncing the user to the canonical
//     `/s/{module}::{name}` page (which loses the workspace
//     chrome and the navigation context).
func WorkspaceRowURL(view *WorkspaceView, s *model.Symbol) templ.SafeURL {
	module := viewModuleName(view)
	scope := viewScopeOID(view)
	if s == nil {
		return moduleURL(module)
	}
	// All path / query components go through `url.PathEscape` /
	// `url.QueryEscape`. SMI module names and OIDs are URL-safe in
	// practice, but `templ.SafeURL` bypasses templ's HTML-escape on
	// href attributes — any byte that breaks attribute quoting (the
	// rare malformed MIB with a stray space, ampersand, or quote in
	// a name) would otherwise inject markup. Defense in depth.
	if s.OID == "" {
		// No-OID symbols (Textual Conventions, some object groups)
		// are module-level definitions — they don't live under any
		// OID subtree, so preserving the current scope is
		// meaningless. Always navigate to the module root with
		// `?sel={name}` so the workspace's list pane shows the full
		// module symbol set with the TC highlighted, instead of
		// stranding the user on an unrelated scoped list with the
		// detail pane the only thing reflecting the click.
		return templ.SafeURL("/m/" + url.PathEscape(module) + "?sel=" + url.QueryEscape(s.Name))
	}
	if !KindHasChildren(s.Kind) {
		// Preserve the current scope only when the clicked leaf
		// actually lives under it. Setting scope to the leaf
		// itself would narrow the list to just-itself; using a
		// scope that doesn't enclose the leaf strands the list
		// pane on an unrelated subtree.
		if scope != "" && OIDUnderPrefix(s.OID, scope) {
			return templ.SafeURL("/m/" + url.PathEscape(module) + "/" + url.PathEscape(scope) + "?sel=" + url.QueryEscape(s.OID))
		}
		if s.ParentOID != "" {
			return templ.SafeURL("/m/" + url.PathEscape(module) + "/" + url.PathEscape(s.ParentOID) + "?sel=" + url.QueryEscape(s.OID))
		}
		return templ.SafeURL("/m/" + url.PathEscape(module) + "?sel=" + url.QueryEscape(s.OID))
	}
	// Container — drill in (scope change).
	return templ.SafeURL("/m/" + url.PathEscape(s.ModuleName) + "/" + url.PathEscape(s.OID))
}

// SyntaxShort returns the short display form of a symbol's
// Syntax for the list pane's narrow SYNTAX column. The compile
// layer expands `Enumeration` TCs to inline their named-number
// list (`Enumeration { up(1), down(2), … }`) so the right pane's
// type pill carries the full detail at a glance — but in the
// 110px column that string truncates to "Enumeration { o" with
// the rest of the type unreadable. Strip inline `{…}` bodies for
// the column; the full string still ships in `s.Syntax` and the
// EnumValues section renders the same named numbers as a table.
func SyntaxShort(s string) string {
	if i := strings.IndexByte(s, '{'); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// SelectorLooksLikeOID reports whether the `sel=` query value is
// an OID (digits + dots) rather than an SMI symbol name. SMI names
// must start with a letter per RFC 1212 §4.1.6 / RFC 2578 §3.1.
//
// Beyond the first-char digit gate (which is enough to disambiguate
// names from OIDs), every byte is verified to be a digit or dot.
// `1abc` and `1<script>` would otherwise pass the first-char test
// and reach `GetSymbolByOID` as garbage strings, where they'd land
// in `view.MissingOID` and propagate further into the page.
func SelectorLooksLikeOID(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ImportGroup is one source-module entry in the module overview's
// Imports section. The flat `[]model.Import` from the parser is
// aggregated by source module so the renderer can show "SNMPv2-SMI:
// Counter32, Gauge32, ..." as a single row instead of N near-
// duplicate rows.
type ImportGroup struct {
	Module  string
	Symbols []string
}

// GroupImports collapses a flat `[]model.Import` into one row per
// source module. Order is preserved by first occurrence so the
// rendered list reflects the import order at the top of the MIB
// source — readable to anyone who knows the file.
func GroupImports(imports []model.Import) []ImportGroup {
	if len(imports) == 0 {
		return nil
	}
	idx := make(map[string]int, len(imports))
	out := make([]ImportGroup, 0, len(imports))
	for _, imp := range imports {
		i, ok := idx[imp.FromModule]
		if !ok {
			idx[imp.FromModule] = len(out)
			out = append(out, ImportGroup{Module: imp.FromModule, Symbols: []string{imp.Symbol}})
			continue
		}
		out[i].Symbols = append(out[i].Symbols, imp.Symbol)
	}
	return out
}

// NotifyObjectURL builds the URL for a notification's OBJECTS-clause
// entry — `linkDown`'s `ifIndex` / `ifAdminStatus` / `ifOperStatus`
// each become clickable links into that object's workspace page.
// Uses the name-based selector so the click works regardless of
// whether the target object's OID is loaded as a separate row in
// the same module (it usually isn't — `linkDown` lives in IF-MIB
// alongside `ifAdminStatus`, but the notification-objects pattern
// often crosses module boundaries).
func NotifyObjectURL(module, name string) templ.SafeURL {
	return templ.SafeURL("/m/" + url.PathEscape(module) + "?sel=" + url.QueryEscape(name))
}

// treeFragmentURL is the HTMX target that returns the children of
// an OID rendered as workspace tree-rows. The `module` + `scope`
// query params let the fragment handler rebuild a synthetic
// `*WorkspaceView` for `WorkspaceRowURL` so leaf clicks inside a
// freshly-expanded subtree preserve the URL scope (matching list-
// row behavior). Both are URL-safe (alphanumeric + dash for module
// names, digits + dots for OIDs) so no escaping is needed.
func treeFragmentURL(module, scope, parentOID string) templ.SafeURL {
	u := "/api/v1/tree/fragment?parent=" + parentOID
	if module != "" {
		u += "&module=" + module
	}
	if scope != "" {
		u += "&scope=" + scope
	}
	return templ.SafeURL(u)
}

// viewModuleName / viewScopeOID are nil-safe accessors used by the
// templ when rendering a tree-row outside a full workspace render
// (e.g. WorkspaceTreeFragment with view==nil during early callers
// — left over for safety, but every fragment now constructs a
// synthetic view).
func viewModuleName(v *WorkspaceView) string {
	if v == nil || v.Module == nil {
		return ""
	}
	return v.Module.Name
}

func viewScopeOID(v *WorkspaceView) string {
	if v == nil {
		return ""
	}
	return v.ScopeOID
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

// selectedOID returns the OID of the workspace view's currently
// selected symbol, or "" when nothing is selected. Threaded into
// the list pane so the matching row can carry a `selected` class
// for the design's tinted-background highlight.
func selectedOID(v *WorkspaceView) string {
	if v == nil || v.Selected == nil || v.Selected.Symbol == nil {
		return ""
	}
	return v.Selected.Symbol.OID
}

// BreadcrumbStep is one segment in the workspace's scope-path
// breadcrumb above the list-pane column header. The chain reads
// `axMgmt / axSystem / axSysCpu / axSysCpuTable` per the handoff
// design — each segment is a clickable link that scopes the list
// to that level, and the trailing entry (the current scope) is
// marked IsLast for the accent styling.
type BreadcrumbStep struct {
	Name   string
	OID    string
	Module string
	IsLast bool
}

// ScopeBreadcrumb returns the named SMI symbols on the path from
// the module's root down to the current scope, suitable for
// rendering as the breadcrumb chain above the list pane's column
// header. Canonical-table entries (iso/org/dod/internet/private/
// enterprises/…) are filtered out — they're context-free noise
// for a workspace already pinned to a specific module.
//
// `view.OIDPath` is the decoded path of the SELECTED symbol, which
// can extend past the SCOPE when the user is selecting a leaf
// inside a scoped subtree (e.g. clicking a column inside a table:
// scope = table, selection = column). The chain is therefore
// truncated at `view.ScopeOID` so the breadcrumb terminates at the
// scope, not the selection.
//
// Returns nil when there's no scope or when the resolved path has
// no named entries, which suppresses the breadcrumb strip in the
// templ entirely so unscoped views render flush.
func ScopeBreadcrumb(v *WorkspaceView) []BreadcrumbStep {
	if v == nil || v.ScopeOID == "" || len(v.OIDPath) == 0 {
		return nil
	}
	var out []BreadcrumbStep
	for _, st := range v.OIDPath {
		if st.Canonical || st.Name == "" {
			if st.Prefix == v.ScopeOID {
				break
			}
			continue
		}
		out = append(out, BreadcrumbStep{
			Name:   st.Name,
			OID:    st.Prefix,
			Module: st.Module,
		})
		if st.Prefix == v.ScopeOID {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	out[len(out)-1].IsLast = true
	return out
}

// SplitOIDLast splits an OID into its prefix (everything up to and
// including the final dot) and its last numeric segment. Examples:
//   - "1.3.6.1.4.1.22610.2.4.1.1.1" → ("1.3.6.1.4.1.22610.2.4.1.1.", "1")
//   - "1.3"                          → ("1.",                          "3")
//   - "42"                           → ("",                             "42")
//   - ""                             → ("", "")
//
// Used by the workspace list pane's OID column to render the
// prefix in muted color and the last segment bold/bright per the
// handoff design ("highlighted index indicator").
func SplitOIDLast(oid string) (prefix, last string) {
	if oid == "" {
		return "", ""
	}
	i := strings.LastIndex(oid, ".")
	if i < 0 {
		return "", oid
	}
	return oid[:i+1], oid[i+1:]
}

// pickerModule is the trimmed shape embedded as a JSON script for
// Alpine to consume in the module-picker overlay. It deliberately
// drops fields the picker doesn't render (Description, Imports,
// etc.) so the inline JSON stays small even on a 1k-module bundle.
type pickerModule struct {
	Name string `json:"name"`
	OID  string `json:"oid,omitempty"`
}

// PickerModulesJSON returns a JSON-encoded slice of {name, oid}
// objects for embedding in the module-picker overlay's hidden
// `<script type="application/json">` payload. Errors are rare
// (the slice contains only plain strings) and would surface only
// as an empty list — the picker's empty-state message handles
// that case gracefully.
//
// `json.NewEncoder` + `SetEscapeHTML(true)` is REQUIRED for the
// embed-in-`<script>`-island to be safe. `json.Marshal` does NOT
// HTML-escape by default (despite folklore — only `Encoder.Encode`
// with `SetEscapeHTML(true)` enables it). A module name containing
// `</script>` would otherwise terminate the inline JSON island
// and execute injected markup. Encoder appends a trailing newline
// that we strip before return.
func PickerModulesJSON(mods []model.Module) string {
	out := make([]pickerModule, 0, len(mods))
	for _, m := range mods {
		out = append(out, pickerModule{Name: m.Name, OID: m.OIDRoot})
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(out); err != nil {
		return "[]"
	}
	return strings.TrimRight(buf.String(), "\n")
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

// OIDUnderPrefix reports whether oid is at or under prefix, treated
// as a `.`-delimited OID path. Empty prefix matches everything; an
// exact match counts as "under". Used by handleWorkspace to scope
// the center-pane symbol list to the selected OID.
func OIDUnderPrefix(oid, prefix string) bool {
	if prefix == "" {
		return true
	}
	if oid == prefix {
		return true
	}
	return len(oid) > len(prefix) &&
		oid[:len(prefix)] == prefix &&
		oid[len(prefix)] == '.'
}

// isEmptyCounts reports whether every family count is zero, so the
// status bar can render a single "empty module" pill instead of
// disappearing the count region. Indexes is intentionally counted
// even though Phase 3's CountByFamily classifier doesn't populate
// it (kept here for symmetry once a future commit threads the
// IndexColumns join).
func isEmptyCounts(c *model.FamilyCounts) bool {
	if c == nil {
		return true
	}
	return c.Counters == 0 && c.Gauges == 0 && c.Ints == 0 &&
		c.Texts == 0 && c.Indexes == 0 && c.Times == 0 &&
		c.Addrs == 0 && c.Bools == 0 && c.Notifs == 0 &&
		c.Structs == 0
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

// TypeLetter returns the single-character glyph that appears in the
// type-letter badge in the workspace list pane: C/G/I/S/X/T/A/B/N
// for the nine SMI type families, or `·` (U+00B7) for the structural
// fallback. Mirrors the family taxonomy in `model.TypeFamily` so the
// glyph and the family CSS class are always derived from the same
// source.
func TypeLetter(s *model.Symbol) string {
	switch FamilyClass(s) {
	case "t-counter":
		return "C"
	case "t-gauge":
		return "G"
	case "t-int":
		return "I"
	case "t-text":
		return "S"
	case "t-index":
		return "X"
	case "t-time":
		return "T"
	case "t-addr":
		return "A"
	case "t-bool":
		return "B"
	case "t-notif":
		return "N"
	}
	return "·"
}

// TrapTypeLetter returns the snmptrap CLI type letter for a given
// SMI syntax string. snmptrap takes varbinds as triples
// `<oid> <type-letter> <value>` where the letter encodes the
// runtime type:
//
//	i  INTEGER / Integer32
//	u  Unsigned32 / Gauge32
//	c  Counter32
//	C  Counter64
//	t  TimeTicks
//	s  OCTET STRING (and most string-shaped TCs)
//	o  OBJECT IDENTIFIER
//	a  IpAddress
//	b  BITS
//	x  Hex string (used here for MacAddress / PhysAddress)
//
// Detection is by syntax-string match. The compile layer expands
// some Textual Conventions to inline their underlying type
// (`Counter32` syntax → "Counter32"; `IpAddress` syntax →
// "IpAddress" because the IpAddress TC is base-resolved during
// parse), but a few common TCs survive as their TC name and the
// helper recognises those by name.
//
// Unknown syntaxes default to "s" (string). The user can edit the
// generated command if they need a different letter — better to
// surface a guess than to fail.
func TrapTypeLetter(syntax string) string {
	s := strings.TrimSpace(syntax)
	// Strip any inline `{…}` body — `INTEGER {up(1), down(2)}` is
	// still an INTEGER for snmptrap purposes. Use `i >= 0` so a
	// pathological syntax that starts with `{` (no leading type
	// keyword) still gets the empty-prefix treatment.
	if i := strings.IndexByte(s, '{'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	// Strip subrange / size constraints — `Integer32 (1..2147483647)`
	// or `OCTET STRING (SIZE(0..255))` are the base types for our
	// purposes.
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	switch s {
	case "INTEGER", "Integer32",
		// `Enumeration` is what the compile layer emits as the
		// syntax of an INTEGER-subtype Textual Convention with a
		// named-number list (e.g. `IfAdminStatus`, `IfOperStatus`).
		// Treat it as INTEGER for snmptrap purposes.
		"Enumeration",
		// Common integer-subtype TCs that appear as a varbind
		// syntax verbatim. The list is best-effort; unknown
		// integer TCs fall through to the default `s` and the
		// user can override.
		"InterfaceIndex",
		"InterfaceIndexOrZero",
		"InetPortNumber",
		"InetVersion",
		"IANAifType",
		// TruthValue (INTEGER 1..2) and RowStatus (INTEGER 1..6
		// per RFC 2579) — the spec mandates "underlying base
		// type's letter", which is INTEGER. The modal renders an
		// inline hint near these TCs telling the user to type
		// the integer (e.g. `1` for `up`, `4` for `createAndGo`)
		// rather than the named value, since snmptrap on the
		// wire requires the integer.
		"TruthValue",
		"RowStatus":
		return "i"
	case "Unsigned32", "Gauge32":
		return "u"
	case "Counter32":
		return "c"
	case "Counter64":
		return "C"
	case "TimeTicks", "TimeStamp":
		return "t"
	case "OCTET STRING",
		"DisplayString",
		"SnmpAdminString",
		"DateAndTime":
		return "s"
	case "OBJECT IDENTIFIER":
		return "o"
	case "IpAddress":
		return "a"
	case "BITS":
		return "b"
	case "MacAddress", "PhysAddress":
		return "x"
	}
	return "s"
}

// StatusModifier returns the row-class modifier for an SMI status,
// or "" for normal states (current / mandatory / optional). The
// workspace tree, list, and type-definitions bar attach this
// modifier to their row element so a single CSS rule can dim
// deprecated and obsolete rows uniformly.
//
// The dimming complements `StatusPillLabel`'s text badge —
// together they signal "this symbol exists in the MIB but is
// flagged as legacy by its author" without burying the row.
func StatusModifier(s model.Status) string {
	switch s {
	case model.StatusDeprecated:
		return "status-deprecated"
	case model.StatusObsolete:
		return "status-obsolete"
	}
	return ""
}

// StatusPillLabel returns the text rendered in the small status
// pill that appears alongside a symbol's name on surfaces that
// surface deprecated / obsolete status (list pane, type-defs
// bar). Returns "" for normal states so the templ can omit the
// pill entirely with a `len(...) > 0` gate.
func StatusPillLabel(s model.Status) string {
	switch s {
	case model.StatusDeprecated:
		return "deprecated"
	case model.StatusObsolete:
		return "obsolete"
	}
	return ""
}

// AccessClass returns the abbreviated CSS class used in the
// workspace list-pane access column: "ro" (read-only / cyan), "rw"
// (read-write / read-create / amber), or "na" (not-accessible /
// accessible-for-notify / muted). The empty-string case is treated
// as "na" so SMIv1 imports without a MAX-ACCESS clause still get a
// neutral pill rather than a missing column.
func AccessClass(a model.Access) string {
	switch a {
	case model.AccessReadOnly:
		return "ro"
	case model.AccessReadWrite, model.AccessReadCreate:
		return "rw"
	}
	return "na"
}

// AccessLabel returns the short label rendered next to AccessClass:
// "ro", "rw", "na", "n/a", or empty when no MAX-ACCESS applies.
// Kept separate from AccessClass so future refinements (e.g. an
// "n/a" label for `accessible-for-notify` while keeping the same
// muted color class) don't have to fight a single overloaded
// helper.
func AccessLabel(a model.Access) string {
	switch a {
	case model.AccessReadOnly:
		return "ro"
	case model.AccessReadWrite, model.AccessReadCreate:
		return "rw"
	case model.AccessNotAccessible, model.AccessAccessibleNotify:
		return "na"
	}
	return ""
}

// SplitCamelPrefix returns the (lowercase camelCase prefix, rest)
// pair for an SMI symbol name so the renderer can dim the boring
// shared prefix and emphasize the distinguishing tail. Examples:
//   - "axSysCpu"   → ("ax",  "SysCpu")
//   - "ifInOctets" → ("if",  "InOctets")
//   - "interfaces" → ("",    "interfaces")  (no uppercase → no split)
//   - "TruthValue" → ("",    "TruthValue")  (starts uppercase)
//
// The split is at the first uppercase rune. The empty-prefix cases
// signal "render the whole name as bright tail with no dimmed
// prefix", which the templ uses to skip the wrapping `<span class="pre">`.
func SplitCamelPrefix(name string) (prefix, tail string) {
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i == 0 {
				return "", name
			}
			return name[:i], name[i:]
		}
	}
	return "", name
}

// SplitNameHTML renders a symbol name as `<span class="pre">…</span><span class="tail">…</span>`
// with NO whitespace between the two spans. Built in Go (rather than
// composed in `templ`) because templ preserves source whitespace
// across an `if pre != ""` block, and that inter-element whitespace
// gets rendered as a visible space in inline contexts — so
// `axSysCpu` would read as `ax SysCpu`. Returning a single
// pre-built HTML string sidesteps that.
//
// Returned string is safe to render via templ.Raw.
func SplitNameHTML(name string) string {
	pre, tail := SplitCamelPrefix(name)
	var b strings.Builder
	b.Grow(len(name) + 48)
	if pre != "" {
		b.WriteString(`<span class="pre">`)
		b.WriteString(html.EscapeString(pre))
		b.WriteString(`</span>`)
	}
	b.WriteString(`<span class="tail">`)
	b.WriteString(html.EscapeString(tail))
	b.WriteString(`</span>`)
	return b.String()
}

// KindLabel returns the human-readable label for a SymbolKind, used
// in the workspace detail pane's "● COLUMN" header. Default is the
// kind string itself; KindObjectType-derived values (scalar, table,
// table-entry, column) and special kinds get spelled out for the
// reader instead of the SMI shorthand.
func KindLabel(k model.SymbolKind) string {
	switch k {
	case model.KindScalar:
		return "scalar"
	case model.KindTable:
		return "table"
	case model.KindTableEntry:
		return "table entry"
	case model.KindColumn:
		return "column"
	case model.KindTextualConvention:
		return "textual convention"
	case model.KindObjectIdentity:
		return "object identity"
	case model.KindNotificationType:
		return "notification"
	case model.KindTrapType:
		return "trap"
	case model.KindModuleIdentity:
		return "module"
	case model.KindObjectGroup:
		return "object group"
	case model.KindNotificationGroup:
		return "notification group"
	case model.KindModuleCompliance:
		return "module compliance"
	}
	return string(k)
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

// NotifyVarbind carries the per-varbind metadata the trap simulator
// modal needs at render time, baked into the workspace page as
// data-* attributes. Built by the handler from each
// `RefNotificationObject` reference: the linked symbol's OID,
// syntax, kind (column vs scalar), and any enumerated values from
// its underlying type.
//
// `EnumValuesJSON` is pre-rendered to JSON in the handler so the
// templ doesn't need to encode at render time. Empty string
// indicates a non-enum varbind.
type NotifyVarbind struct {
	Module         string
	Name           string
	OID            string
	Syntax         string
	TrapTypeLetter string
	EnumValuesJSON string
	IsColumn       bool
}

// TrapIndexColumn describes one index column of a notification's
// parent table-entry, in INDEX-clause order. The trap simulator
// modal walks this slice to render one type-specific input per
// column (numeric for INTEGER, dotted-quad for IpAddress, etc.)
// and composes each varbind's column-OID suffix from the per-
// column composers.
type TrapIndexColumn struct {
	Name   string `json:"name"`
	Syntax string `json:"syntax"`
}

// TrapIndexStrategy describes how the trap simulator modal should
// prompt the user for the row identity that's appended to each
// column varbind's OID.
//
// `Mode = "indexed"` means every column varbind shares a parent
// table-entry whose INDEX clause has classifiable column syntaxes
// (e.g. a single INTEGER, a single IpAddress). The modal walks
// `Columns` to render one type-specific input per column and
// composes each suffix from the per-column composers.
//
// `Mode = "scalar-only"` means every varbind is a scalar — `.0` is
// appended silently, no row-identity input is shown.
//
// `Mode = "raw-suffix"` is the v1.0 fallback for everything the
// classifier doesn't yet recognise (multi-column composite
// indexes, OCTET STRING, OID, BITS, multi-parent notifications).
// The modal renders a freeform text input where the user types
// the dotted suffix themselves.
type TrapIndexStrategy struct {
	Mode    string // "indexed" | "scalar-only" | "raw-suffix"
	Columns []TrapIndexColumn
}

// TrapIndexColumnsJSON returns a JSON-encoded slice of {name,
// syntax} objects for embedding in the notify-objects list's
// `data-trap-index-columns` attribute. The trap-simulator modal
// parses this on open() and walks the entries to render per-column
// row-identity inputs.
//
// `json.NewEncoder` + `SetEscapeHTML(true)` is REQUIRED for the
// embed-in-attribute to be safe — `json.Marshal` does not escape
// HTML. A column name containing `</script>` or a quote could
// otherwise terminate the attribute and inject markup. Encoder
// appends a trailing newline that we strip before return.
func TrapIndexColumnsJSON(cols []TrapIndexColumn) string {
	if len(cols) == 0 {
		return "[]"
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(cols); err != nil {
		return "[]"
	}
	return strings.TrimRight(buf.String(), "\n")
}

// parseTCSyntax decomposes a Textual Convention's syntax string
// into a base-type pill label and a constraint phrase for the
// type-definitions bar.
//
// The recognised base types are the SMI base types and a small set
// of common rendering aliases:
//
//	"Integer32"          → "Integer32"
//	"INTEGER"            → "Integer"
//	"Unsigned32"         → "Unsigned32"
//	"Gauge32"            → "Gauge32"
//	"Counter32"          → "Counter32"
//	"Counter64"          → "Counter64"
//	"OCTET STRING"       → "OctetString"
//	"OBJECT IDENTIFIER"  → "OID"
//	"BITS"               → "BITS"
//	"TimeTicks"          → "TimeTicks"
//	"IpAddress"          → "IpAddress"
//
// The recognised constraint shapes:
//
//	"(SIZE(N))"           → "size: N"
//	"(SIZE(min..max))"    → "size: min..max"
//	"(SIZE(...|...))"     → "size: variable"      (multi-segment)
//	"(min..max)"          → "range: min..max"
//	"INTEGER {…}"         → "enum: N values"      (counts named entries)
//	"BITS {…}"            → "N flags"             (counts named entries)
//	(none)                → ""                    (e.g. plain Counter32)
//
// Unrecognised shapes fall back to a verbatim leading-token base
// and the trailing parenthesised substring as the constraint —
// truthful but ugly, so the user can see what shape the parser
// didn't recognise instead of a misleading empty cell.
func parseTCSyntax(syntax string) (base, constraint string) {
	s := strings.TrimSpace(syntax)
	if s == "" {
		return "", ""
	}
	// Enum / BITS shapes carry a `{name(num), …}` body that the
	// SIZE/range-extracting branch below doesn't expect. Detect and
	// classify them first.
	if i := strings.IndexByte(s, '{'); i >= 0 {
		head := strings.TrimSpace(s[:i])
		body := s[i+1:]
		if j := strings.LastIndexByte(body, '}'); j >= 0 {
			body = body[:j]
		}
		count := countNamedEntries(body)
		switch head {
		case "INTEGER", "Enumeration":
			// `Enumeration` is what smidump emits as the basetype
			// for a TEXTUAL-CONVENTION whose underlying type is
			// INTEGER + named numbers. The SMI base type is still
			// INTEGER, so the pill reads `Integer` with the count
			// in the constraint slot — matches the reference UI
			// and tells the user the underlying type at a glance.
			return "Integer", fmt.Sprintf("enum: %d values", count)
		case "BITS", "Bits":
			return "BITS", fmt.Sprintf("%d flags", count)
		}
		// Fall through — unrecognised head with an inline body.
		return head, ""
	}
	// Split into <head> "(" <constraint-body> ")" if a parenthesised
	// constraint is present. The head is the base-type token.
	headEnd := strings.IndexByte(s, '(')
	var (
		head           string
		constraintBody string
	)
	if headEnd < 0 {
		head = s
	} else {
		head = strings.TrimSpace(s[:headEnd])
		// Take everything between the FIRST `(` and the matching final `)`.
		// SMI permits nested parens (`(SIZE(0..255))`), so we want the
		// outermost pair — last `)` in the string is sufficient.
		rest := s[headEnd+1:]
		closeIdx := strings.LastIndexByte(rest, ')')
		if closeIdx >= 0 {
			constraintBody = rest[:closeIdx]
		} else {
			constraintBody = rest
		}
	}
	rendered := renderBaseType(head)
	if rendered == "" {
		// Unrecognised base — verbatim fallback.
		if constraintBody != "" {
			return head, "(" + constraintBody + ")"
		}
		return head, ""
	}
	if constraintBody == "" {
		return rendered, ""
	}
	if c := parseConstraintBody(constraintBody); c != "" {
		return rendered, c
	}
	return rendered, "(" + constraintBody + ")"
}

// renderBaseType normalises an SMI base-type token to the short
// label rendered in the type-defs pill. Returns "" for unrecognised
// tokens — callers fall back to the verbatim head.
//
// The recognised set covers both the SMI-canonical spellings
// (`OCTET STRING`, `OBJECT IDENTIFIER`) and smidump's pascal-case
// XML attribute spellings (`OctetString`, `ObjectIdentifier`,
// `Bits`). Real smidump output for the IF-MIB typedefs uses
// `OctetString` and `Integer32`; using both forms here means the
// parser works whether the syntax string came from a typedef
// rendering, a column rendering, or a synthetic test fixture.
func renderBaseType(head string) string {
	switch head {
	case "Integer32":
		return "Integer32"
	case "INTEGER":
		return "Integer"
	case "Unsigned32":
		return "Unsigned32"
	case "Gauge32":
		return "Gauge32"
	case "Counter32":
		return "Counter32"
	case "Counter64":
		return "Counter64"
	case "OCTET STRING", "OctetString":
		return "OctetString"
	case "OBJECT IDENTIFIER", "ObjectIdentifier":
		return "OID"
	case "BITS", "Bits":
		return "BITS"
	case "TimeTicks":
		return "TimeTicks"
	case "IpAddress":
		return "IpAddress"
	}
	return ""
}

// parseConstraintBody renders the inside of a parenthesised
// constraint group ("SIZE(N)", "SIZE(min..max)", "min..max", etc.)
// as a human-readable phrase. Returns "" when the body shape isn't
// recognised; the caller falls back to verbatim rendering.
func parseConstraintBody(body string) string {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "SIZE(") {
		inner := strings.TrimSuffix(strings.TrimPrefix(body, "SIZE("), ")")
		inner = strings.TrimSpace(inner)
		if inner == "" {
			return ""
		}
		if strings.Contains(inner, "|") {
			return "size: variable"
		}
		return "size: " + inner
	}
	// Bare range — `min..max` (no SIZE prefix).
	if strings.Contains(body, "..") && !strings.Contains(body, "|") {
		return "range: " + body
	}
	return ""
}

// countNamedEntries counts comma-separated `name(num)` entries
// inside an INTEGER {} or BITS {} body. Robust against extra
// whitespace and nested parentheses (no real-world MIB nests
// inside an enum body, but the count tolerates them).
func countNamedEntries(body string) int {
	depth := 0
	count := 0
	hadContent := false
	for _, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 && hadContent {
				count++
				hadContent = false
			}
		default:
			if !unicode.IsSpace(r) {
				hadContent = true
			}
		}
	}
	if hadContent {
		count++
	}
	return count
}

// TypeDef is one row in the workspace's type-definitions bar — a
// projected, render-ready shape derived from a Textual Convention
// symbol. The bar walks `[]TypeDef` to render one row per TC with
// a parsed base-type pill and a constraint phrase ("range:
// 1..2147483647", "size: 0..255", "enum: 3 values"), plus an
// inline expansion of `EnumValues` for enum / BITS TCs.
//
// The shape is a pure projection — no live `*model.Symbol`
// pointer — so the templ doesn't accidentally reach into fields
// that aren't relevant for the bar (Description, OID, etc.).
type TypeDef struct {
	Module     string
	Name       string
	Base       string
	Constraint string
	Status     model.Status
	EnumValues []model.EnumValue
}

// CollectTypeDefs filters a module's symbol slice to its Textual
// Conventions and projects each into a render-ready `TypeDef`.
// Source order is preserved — TCs in real MIBs are often defined
// in dependency order (`InterfaceIndex` before
// `InterfaceIndexOrZero` because the latter cites the former), and
// the bar's reading order should match the source.
//
// Returns nil when the module declares no TCs so the templ can
// suppress the entire bar with a `len(...) > 0` gate.
func CollectTypeDefs(syms []model.Symbol) []TypeDef {
	var out []TypeDef
	for i := range syms {
		s := &syms[i]
		if s.Kind != model.KindTextualConvention {
			continue
		}
		base, constraint := parseTCSyntax(s.Syntax)
		out = append(out, TypeDef{
			Module:     s.ModuleName,
			Name:       s.Name,
			Base:       base,
			Constraint: constraint,
			Status:     s.Status,
			EnumValues: s.EnumValues,
		})
	}
	return out
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

// TreeRow is one node in the workspace's left-rail OID tree.
//
// `HasChildren` drives whether a chevron renders so the user can
// drill in via lazy HTMX-fragment expansion.
//
// `Expanded`, `Selected`, and `PreloadedKids` are populated by the
// workspace handler's auto-expand pass when a selection or scope
// is set. Rows on the path from the module's top-level entry
// down to the selection are marked Expanded=true with their
// children threaded into PreloadedKids — that way the tree
// preserves navigation context across full-page navigations
// without needing client-side state. The row matching the
// current selection picks up Selected=true for the accent
// highlight.
type TreeRow struct {
	Symbol        model.Symbol
	HasChildren   bool
	Expanded      bool
	Selected      bool
	PreloadedKids []TreeRow
}

// TreeRowAlpineState renders the `x-data` initial-state JSON for a
// tree row, baking the server-decided `expanded` / `loaded` values
// into the markup so the row paints in the right state on first
// render. When `expanded` is true, `loaded` is also true so the
// chevron's click handler treats it as already-fetched and
// just toggles visibility — no duplicate fragment fetch.
func TreeRowAlpineState(expanded bool) string {
	return fmt.Sprintf(`{ expanded: %t, loaded: %t, fetching: false }`, expanded, expanded)
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
	// ScopeOID is the URL-driven scope: when non-empty the list
	// pane shows only symbols at or under this OID, and the
	// list-pane chrome renders a "View all in module" link.
	ScopeOID string
	// ModuleDownloadable is true when the module's source file is
	// readable on disk at render time. Drives whether the
	// module-info bar surfaces the `↓ MIB` / `↓ bundle` download
	// affordances — when false, clicking either link would 410 /
	// produce an empty bundle, so we hide them rather than
	// advertise a broken click target.
	ModuleDownloadable bool
	// TypeDefs is the projected list of the module's Textual
	// Conventions for the type-definitions bar. Empty when the
	// module declares no TCs; the templ suppresses the bar
	// entirely in that case.
	TypeDefs []TypeDef
	// BundleFileCount is the number of `.mib` entries the
	// `/m/{name}/download.zip` endpoint will ship — equal to
	// the count of loaded modules in the IMPORTS closure
	// (root + direct + transitive). The module-info bar
	// displays this so users know how many files they'll get
	// before clicking the bundle download. Computed at render
	// time by handleWorkspace; zero when the closure couldn't
	// be resolved (the templ falls back gracefully).
	BundleFileCount int
}

// moduleSummaryPreview returns the first-sentence preview of a
// module's description for the collapsed module-info bar, capped
// at ~120 chars so the summary line stays single-row even on
// narrow displays. Reuses the same first-sentence + word-boundary
// truncation as `SummarizeSymbol`.
func moduleSummaryPreview(desc string) string {
	d := strings.TrimSpace(collapseWhitespace(desc))
	if d == "" {
		return ""
	}
	first := firstSentence(d)
	if utf8Count(first) > 120 {
		first = truncateWord(first, 120) + "…"
	}
	return first
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
