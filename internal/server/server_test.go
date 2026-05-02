package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.ReplaceModule(context.Background(),
		&model.Module{Name: "IF-MIB", OIDRoot: "1.3.6.1.2.1.31", ParseStatus: model.ParseStatusClean,
			Description: "Interfaces MIB."},
		[]model.Symbol{
			{
				ModuleName: "IF-MIB", Name: "ifTable",
				OID: "1.3.6.1.2.1.2.2", ParentOID: "1.3.6.1.2.1.2",
				Kind: model.KindTable, Syntax: "SEQUENCE OF IfEntry",
				Access: model.AccessNotAccessible, Status: model.StatusCurrent,
				Description: "A list of interface entries.",
			},
			{
				ModuleName: "IF-MIB", Name: "ifEntry",
				OID: "1.3.6.1.2.1.2.2.1", ParentOID: "1.3.6.1.2.1.2.2",
				Kind: model.KindTableEntry, Syntax: "IfEntry",
				Access: model.AccessNotAccessible, Status: model.StatusCurrent,
				IndexColumns: []string{"ifIndex"},
			},
			{
				ModuleName: "IF-MIB", Name: "ifIndex",
				OID: "1.3.6.1.2.1.2.2.1.1", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindColumn, Syntax: "InterfaceIndex",
				Access: model.AccessReadOnly, Status: model.StatusCurrent,
			},
			{
				ModuleName: "IF-MIB", Name: "ifInOctets",
				OID: "1.3.6.1.2.1.2.2.1.10", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindColumn, Syntax: "Counter32",
				Access: model.AccessReadOnly, Status: model.StatusCurrent,
				Units: "octets", Description: "The total number of octets received on the interface.",
			},
		},
		[]model.Reference{
			{
				SourceModule: "IF-MIB", SourceName: "ifPacketGroup",
				TargetModule: "IF-MIB", TargetName: "ifInOctets",
				Kind: model.RefGroupMember,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	s := New(st, "", "test", "/var/lib/blittermib/mibs")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ok" {
		t.Errorf("status field = %v", got["status"])
	}
}

func TestVersion(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/version")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(body(t, resp)); got != "test" {
		t.Errorf("version = %q", got)
	}
}

func TestIndex(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	for _, want := range []string{"blittermib", "<strong>1</strong> modules", "<strong>4</strong> symbols"} {
		if !strings.Contains(html, want) {
			t.Errorf("landing missing %q", want)
		}
	}
}

func TestModuleDetail(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/m/IF-MIB")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	html := body(t, resp)
	// The OID 1.3.6.1.2.1.31 no longer appears as a literal substring
	// because FormatOIDHTML wraps each `.` in a span. Assert on the
	// dot-styled fragment instead.
	for _, want := range []string{"IF-MIB", "ifInOctets", `class="oid `, `<span class="dot">.</span>`} {
		if !strings.Contains(html, want) {
			t.Errorf("module page missing %q", want)
		}
	}
}

func TestSymbolDetail(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/s/IF-MIB::ifInOctets")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	html := body(t, resp)
	for _, want := range []string{"ifInOctets", "Counter32", "octets", "ifPacketGroup"} {
		if !strings.Contains(html, want) {
			t.Errorf("symbol page missing %q", want)
		}
	}
}

func TestOIDRedirect(t *testing.T) {
	ts := newTestServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(ts.URL + "/o/1.3.6.1.2.1.2.2.1.10")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/m/IF-MIB/1.3.6.1.2.1.2.2.1.10" {
		t.Errorf("location = %q (Phase 3: /o/{oid} redirects to the workspace, not /s/...)", loc)
	}
}

func TestWorkspaceRoute(t *testing.T) {
	ts := newTestServer(t)
	cases := []struct {
		name       string
		path       string
		wantCode   int
		wantInBody []string
	}{
		{"empty selection", "/m/IF-MIB", 200, []string{
			"IF-MIB",
			`class="status-bar-module"`,
			`class="workspace-grid"`,
			// The unscoped landing now renders a module overview
			// (description + imports) in the right pane instead
			// of the legacy "Pick a symbol" empty state. Asserting
			// on the detail-name code wrapper confirms the
			// overview body painted.
			`<h2 class="detail-name">`,
		}},
		{"with selection", "/m/IF-MIB/1.3.6.1.2.1.2.2.1.10", 200, []string{
			"ifInOctets",
			"Counter32",
			"octets",
			// Right pane no longer renders an "OID decode"
			// section — the scope breadcrumb above the list pane
			// already shows the path. Asserting on the kvbox
			// (Properties grid) confirms the compact detail
			// body still painted.
			`class="kvbox"`,
		}},
		{"missing OID", "/m/IF-MIB/9.9.9", 200, []string{
			`No symbol at`,
			`9.9.9`,
		}},
		{"unknown module", "/m/NO-SUCH-MIB", 404, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + c.path)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != c.wantCode {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.wantCode)
			}
			if c.wantCode == 200 {
				html := body(t, resp)
				for _, want := range c.wantInBody {
					if !strings.Contains(html, want) {
						t.Errorf("workspace %s missing %q", c.name, want)
					}
				}
			}
		})
	}
}

func TestAPITreeFragment(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/tree/fragment?parent=1.3.6.1.2.1.2.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	html := body(t, resp)
	// Phase 4: the fragment endpoint emits `<li>` rows directly
	// (no surrounding `<ul>`); the chevron's HTMX swap appends
	// them into the pre-rendered .tree-children-container in the
	// parent row.
	//
	// Phase 5: workspace tree rows split the camelCase name into
	// `<span class="pre">` + `<span class="tail">` for the dim/
	// bright treatment, so `>ifIndex<` no longer appears as a
	// literal substring. We assert on `data-name="…"` instead —
	// it carries the unsplit name and is the API a future test
	// (or scraper) would actually want to key off.
	for _, want := range []string{
		`class="tree-row `,
		`data-name="ifIndex"`,
		`data-name="ifInOctets"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("tree fragment missing %q", want)
		}
	}
}

func TestAPITreeFragmentMissingParent(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/tree/fragment")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no parent param)", resp.StatusCode)
	}
}

func TestSourceURLGuard(t *testing.T) {
	// Phase 4: the source endpoint is exactly `/m/{name}/source`.
	// An embedded OID before `/source` (e.g. /m/IF-MIB/1.2.3/source)
	// must NOT mis-route through handleModuleSource — that path is
	// just a workspace selection at the literal OID `1.2.3/source`,
	// which doesn't match a symbol, so the workspace renders the
	// missing-OID hint.
	ts := newTestServer(t)

	// /m/IF-MIB/source dispatches to handleModuleSource. The test
	// fixture has no SourcePath set, so the handler returns 404.
	// (Verified the dispatch happened by the 404 status — the
	// workspace handler would have returned 200.)
	resp, err := http.Get(ts.URL + "/m/IF-MIB/source")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/m/IF-MIB/source: status = %d, want 404 (no source path; dispatched correctly)", resp.StatusCode)
	}

	// /m/IF-MIB/1.2.3/source dispatches to handleWorkspace with
	// oid="1.2.3/source". That OID doesn't match any symbol, so the
	// workspace renders with the missing-OID hint — proves the
	// path was NOT caught by the suffix-first source-endpoint
	// check that the Phase-4 guard removed.
	resp, err = http.Get(ts.URL + "/m/IF-MIB/1.2.3/source")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/m/IF-MIB/1.2.3/source: status = %d, want 200 (workspace path)", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	html := body(t, resp)
	if !strings.Contains(html, "No symbol at") {
		t.Errorf("workspace missing-OID hint missing — embedded /source mis-routed to source endpoint?")
	}
}

func TestWorkspaceEmptyModulePill(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReplaceModule(context.Background(),
		&model.Module{Name: "EMPTY-MIB", ParseStatus: model.ParseStatusClean},
		nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	srv := New(st, "", "test", "/var/lib/blittermib/mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/m/EMPTY-MIB")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	if !strings.Contains(html, `class="status-count empty"`) {
		t.Errorf("status bar missing the empty-module pill")
	}
	if !strings.Contains(html, "empty module") {
		t.Errorf("status bar missing the 'empty module' text")
	}
}

func TestWorkspaceScopeFilter(t *testing.T) {
	ts := newTestServer(t)

	// /m/IF-MIB lists every symbol in the module.
	resp, err := http.Get(ts.URL + "/m/IF-MIB")
	if err != nil {
		t.Fatal(err)
	}
	all := body(t, resp)
	// Phase 5: list+tree rows split camelCase names into pre/tail
	// spans, so we key off `data-name="…"` (carried on every row)
	// rather than the bare visible text.
	for _, want := range []string{
		`data-name="ifTable"`,
		`data-name="ifEntry"`,
		`data-name="ifIndex"`,
		`data-name="ifInOctets"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("unscoped /m/IF-MIB missing %q", want)
		}
	}

	// /m/IF-MIB/1.3.6.1.2.1.2.2.1 (ifEntry) narrows to the entry +
	// its columns; ifTable (the parent) is excluded.
	resp, err = http.Get(ts.URL + "/m/IF-MIB/1.3.6.1.2.1.2.2.1")
	if err != nil {
		t.Fatal(err)
	}
	scoped := body(t, resp)
	for _, want := range []string{
		`data-name="ifEntry"`,
		`data-name="ifIndex"`,
		`data-name="ifInOctets"`,
	} {
		if !strings.Contains(scoped, want) {
			t.Errorf("scoped to ifEntry missing %q", want)
		}
	}
	// The list pane scope-link is rendered when scope is active.
	if !strings.Contains(scoped, "View all in module") {
		t.Errorf("scoped list missing the View-all-in-module link")
	}
	// Server-narrowing means ifTable should NOT appear as a list-row
	// (it can still appear as the scope-link href, etc., so check the
	// list-cell context).
	if strings.Contains(scoped, `class="list-row t-struct" data-name="ifTable"`) {
		t.Errorf("scoped list still includes the ifTable row above the scope")
	}
}

func TestWorkspaceKindChips(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/m/IF-MIB")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	// Phase 5: chip labels are lowercase (matching the handoff
	// terminal aesthetic) and carry a colored leading dot before
	// the text — but the dot is suppressed on the "all" chip so
	// `>all</button>` still matches the lowercase plain form.
	for _, want := range []string{
		`class="kind-chips"`,
		`>all</button>`,
		`>scalars`,
		`>tables`,
		`>notifs`,
		`data-kind="table"`,
		`data-kind="column"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("workspace missing %q", want)
		}
	}
}

func TestSearchResultsLinkToWorkspace(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/search?q=octets")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	// Hit rows must point at the workspace selection, not the
	// canonical detail page (Phase 3 retarget).
	want := `href="/m/IF-MIB/1.3.6.1.2.1.2.2.1.10"`
	if !strings.Contains(html, want) {
		t.Errorf("search hit link missing %q (search results should target workspace, not /s/...)", want)
	}
	notWant := `href="/s/IF-MIB::ifInOctets"`
	if strings.Contains(html, notWant) {
		t.Errorf("search results still link to /s/...; expected workspace link instead")
	}
}

func TestSymbolNotFound(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/s/IF-MIB::doesNotExist")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestAPISearch(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/search?q=octets")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got struct {
		Hits []struct {
			Name string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range got.Hits {
		if h.Name == "ifInOctets" {
			found = true
		}
	}
	if !found {
		t.Errorf("ifInOctets not in API search hits: %+v", got.Hits)
	}
}

func TestAPISymbol(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/symbol/IF-MIB/ifInOctets")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got model.Symbol
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "ifInOctets" {
		t.Errorf("name = %q", got.Name)
	}
}

func TestStaticAsset(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("content-type = %q", ct)
	}
}

func TestHTMXLoadedOnEveryPage(t *testing.T) {
	ts := newTestServer(t)
	for _, path := range []string{"/", "/m/IF-MIB", "/s/IF-MIB::ifInOctets", "/diagnostics"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			html := body(t, resp)
			// htmx is loaded for explicit `htmx.ajax(...)` calls in the
			// workspace tree's chevron-expand handler (and any future
			// island that wants partial swaps). Phase 5 dropped global
			// `hx-boost` from the body — full-body swaps caused
			// visible "in-flight" flicker on every navigation; native
			// browser navigation is smoother on the workspace surface
			// where the entire body would have to be replaced anyway.
			if !strings.Contains(html, `/static/htmx.min.js`) {
				t.Errorf("page missing %q", `/static/htmx.min.js`)
			}
		})
	}
}

func TestIslandsLoadedOnEveryPage(t *testing.T) {
	ts := newTestServer(t)
	for _, path := range []string{"/", "/m/IF-MIB", "/s/IF-MIB::ifInOctets", "/diagnostics"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			html := body(t, resp)
			for _, want := range []string{
				`/static/palette.js`,
				`/static/glossary.js`,
			} {
				if !strings.Contains(html, want) {
					t.Errorf("page %q missing %q", path, want)
				}
			}
		})
	}
}

func TestPaletteAssetServed(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/static/palette.js")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	js := body(t, resp)
	for _, marker := range []string{"palette-overlay", "/api/v1/search"} {
		if !strings.Contains(js, marker) {
			t.Errorf("palette.js missing %q — wrong file served?", marker)
		}
	}
}

func TestGlossaryAssetServed(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/static/glossary.js")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	js := body(t, resp)
	for _, marker := range []string{"glossary-popover", "OBJECT-TYPE", "Counter32"} {
		if !strings.Contains(js, marker) {
			t.Errorf("glossary.js missing %q", marker)
		}
	}
}

func TestPaletteCSSLoaded(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/static/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	css := body(t, resp)
	for _, sel := range []string{".palette-overlay", ".palette-input", ".palette-results", ".glossary-popover"} {
		if !strings.Contains(css, sel) {
			t.Errorf("styles.css missing palette selector %q — did prepare-assets run?", sel)
		}
	}
	// Regression: design.md mandates "no layered shadows" — the
	// glossary popover must not use box-shadow. Match only actual
	// declarations (`box-shadow:`), not the substring inside
	// explanatory comments.
	if strings.Contains(css, "box-shadow:") {
		t.Error("styles.css contains a box-shadow declaration — design.md says 'no shadows'")
	}
	// Regression: glossary-seen rule must exist (dropped inline style
	// in glossary.js relies on this CSS owning the styling).
	if !strings.Contains(css, ".glossary-seen") {
		t.Error("styles.css missing .glossary-seen rule")
	}
}

// TestIslandsRebindOnHTMXSwap is a smoke test that the JS islands
// register an htmx:afterSwap handler. Without it, the palette
// overlay (appended to <body>) is destroyed on the first hx-boost
// navigation and the palette silently breaks. We can't drive the
// browser from Go tests, so we verify the source contains the
// re-binding code path.
func TestIslandsRebindOnHTMXSwap(t *testing.T) {
	ts := newTestServer(t)
	for _, asset := range []string{
		"/static/palette.js",
		"/static/glossary.js",
		"/static/workspace.js",
		"/static/picker.js",
	} {
		t.Run(asset, func(t *testing.T) {
			resp, err := http.Get(ts.URL + asset)
			if err != nil {
				t.Fatal(err)
			}
			js := body(t, resp)
			if !strings.Contains(js, "htmx:afterSwap") {
				t.Errorf("%s missing htmx:afterSwap handler — palette/glossary will break after first nav", asset)
			}
		})
	}
}

func TestHTMXAssetServed(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/static/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/javascript") && !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("content-type = %q", ct)
	}
}

func TestLandingEmptyState(t *testing.T) {
	// Construct a server with an empty store — no modules at all.
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, "", "test", "/srv/mibs")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	for _, want := range []string{`Drop your MIB files here`, `/srv/mibs`, `class="empty"`} {
		if !strings.Contains(html, want) {
			t.Errorf("empty landing missing %q", want)
		}
	}
	for _, badButLooksLikeHero := range []string{"Browse SNMP MIBs, beautifully.</p><a class=\"search-large\""} {
		if strings.Contains(html, badButLooksLikeHero) {
			t.Errorf("empty landing should not show the search hero")
		}
	}
}

func TestSymbolColumnInContext(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/s/IF-MIB::ifInOctets")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	for _, want := range []string{
		`In context`,
		`>10 of `,
		`/s/IF-MIB::ifTable`,
		`Indexed by`,
		`>ifIndex<`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("in-context missing %q", want)
		}
	}
}

func TestSymbolTableColumnsRendered(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/s/IF-MIB::ifTable")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	for _, want := range []string{
		`>Columns<`,
		`class="toc-table"`,
		`>ifIndex<`,
		`>ifInOctets<`,
		`>1<`,                // ifIndex column position
		`>10<`,               // ifInOctets column position
		`class="key">index<`, // ifIndex marked as INDEX column
	} {
		if !strings.Contains(html, want) {
			t.Errorf("table page missing %q", want)
		}
	}
}

func TestSearchExactMatchPrepended(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/search?q=IF-MIB::ifInOctets")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Hits []struct {
			Name string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if got.Hits[0].Name != "ifInOctets" {
		t.Errorf("first hit = %q, want exact match ifInOctets", got.Hits[0].Name)
	}
}

func TestPrivacyNoticeInTopbar(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	if !strings.Contains(html, `class="privacy-notice"`) {
		t.Error("privacy notice missing from topbar")
	}
	if !strings.Contains(html, `Self-hosted`) {
		t.Error("privacy notice text missing")
	}
}

func TestTreePage(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/tree")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	html := body(t, resp)
	if !strings.Contains(html, `data-tree`) {
		t.Error("/tree page missing data-tree attachment point")
	}
}

func TestTreePageFocused(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/tree/1.3.6.1.2.1")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	if !strings.Contains(html, `data-tree-focus="1.3.6.1.2.1"`) {
		t.Error("focused tree page missing data-tree-focus")
	}
}

func TestAPITree(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/tree?parent=1.3.6.1.2.1.2.2")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got struct {
		Parent   string
		Children []struct {
			OID         string
			Name        string
			Module      string
			Kind        string
			HasChildren bool
			Position    string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Parent != "1.3.6.1.2.1.2.2" {
		t.Errorf("parent = %q", got.Parent)
	}
	// ifEntry is at 1.3.6.1.2.1.2.2.1 in our fixture and has children.
	var entry *struct {
		OID         string
		Name        string
		Module      string
		Kind        string
		HasChildren bool
		Position    string
	}
	for i := range got.Children {
		if got.Children[i].Name == "ifEntry" {
			entry = &got.Children[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("ifEntry not in children")
	}
	if !entry.HasChildren {
		t.Error("ifEntry should report HasChildren=true")
	}
	if entry.Position != "1" {
		t.Errorf("ifEntry position = %q, want 1", entry.Position)
	}
}

func TestTreeIslandLoaded(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	if !strings.Contains(html, `/static/tree.js`) {
		t.Error("base layout missing tree.js")
	}
}

func TestTreeAssetServed(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/static/tree.js")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	js := body(t, resp)
	for _, marker := range []string{"data-tree", "/api/v1/tree"} {
		if !strings.Contains(js, marker) {
			t.Errorf("tree.js missing marker %q", marker)
		}
	}
}

func TestSymbolPlainLanguageSummary(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/s/IF-MIB::ifInOctets")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	// summary is rendered between name and OID
	if !strings.Contains(html, `class="summary"`) {
		t.Error("symbol page missing class=summary")
	}
	// fixture description starts with "The total number of octets..."
	// expect that as the first sentence (or part of it)
	if !strings.Contains(html, "The total number of octets") {
		t.Error("summary missing description content")
	}
}

func TestSymbolPlainLanguageSummaryFallback(t *testing.T) {
	// Symbol with no description → "{Kind} in {Module}" fallback.
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReplaceModule(context.Background(),
		&model.Module{Name: "MM", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{ModuleName: "MM", Name: "noDesc", OID: "1.2", Kind: model.KindScalar,
				Status: model.StatusCurrent},
		}, nil, nil); err != nil {
		t.Fatal(err)
	}
	srv := New(st, "", "test", "/var/lib/blittermib/mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, _ := http.Get(ts.URL + "/s/MM::noDesc")
	html := body(t, resp)
	if !strings.Contains(html, "scalar in MM") {
		t.Errorf("fallback summary missing; got: %s", html[:min(2000, len(html))])
	}
}

func TestSymbolDisambiguationRedirectsSingleMatch(t *testing.T) {
	ts := newTestServer(t)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(ts.URL + "/s/ifInOctets")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	// Phase 3: single-match disambiguation now redirects into the
	// workspace selection so `/s/{name}` is consistent with /o/{oid}
	// and search-hit retargets. Symbols without an OID still fall
	// back to /s/... via web.WorkspaceSymbolURL.
	if loc := resp.Header.Get("Location"); loc != "/m/IF-MIB/1.3.6.1.2.1.2.2.1.10" {
		t.Errorf("location = %q", loc)
	}
}

func TestSymbolDisambiguationChooser(t *testing.T) {
	// Seed four modules that each export "common" — multiple matches,
	// one per narrower OBJECT-TYPE kind. Catches accidental kind-
	// specific code paths in disambiguation rendering.
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seeds := []struct {
		mod  string
		kind model.SymbolKind
	}{
		{"A-MIB", model.KindScalar},
		{"B-MIB", model.KindTable},
		{"C-MIB", model.KindTableEntry},
		{"D-MIB", model.KindColumn},
	}
	for _, s := range seeds {
		if err := st.ReplaceModule(context.Background(),
			&model.Module{Name: s.mod, ParseStatus: model.ParseStatusClean},
			[]model.Symbol{{ModuleName: s.mod, Name: "common", OID: "1." + s.mod,
				Kind: s.kind, Status: model.StatusCurrent}},
			nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(st, "", "test", "/var/lib/blittermib/mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/s/common")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	html := body(t, resp)
	for _, want := range []string{
		"Multiple modules",
		"A-MIB::common", "B-MIB::common", "C-MIB::common", "D-MIB::common",
		"scalar", "table", "table-entry", "column",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("disambiguation page missing %q", want)
		}
	}
}

func TestSymbolNoQualifierEmpty404(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/s/doesNotExistAnywhere")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestModuleSourceRoute(t *testing.T) {
	// Seed a module with a real source file on disk so the route
	// can stream it.
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "TEST-MIB.txt")
	srcContent := "TEST-MIB DEFINITIONS ::= BEGIN\nimports SNMPv2-SMI;\nEND\n"
	if err := os.WriteFile(srcPath, []byte(srcContent), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReplaceModule(context.Background(),
		&model.Module{Name: "TEST-MIB", SourcePath: srcPath, ParseStatus: model.ParseStatusClean},
		nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/m/TEST-MIB/source")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q", ct)
	}
	if got := body(t, resp); got != srcContent {
		t.Errorf("body = %q", got)
	}
}

func TestModuleSourceRouteNoSourcePath(t *testing.T) {
	// Module exists but has no source path recorded → 404.
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/m/IF-MIB/source")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSearchDidYouMean(t *testing.T) {
	ts := newTestServer(t)
	// "ifInOctts" — typo of "ifInOctets" (distance 1)
	resp, err := http.Get(ts.URL + "/search?q=ifInOctts")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	html := body(t, resp)
	for _, want := range []string{
		`No matches for`,
		`Did you mean`,
		`ifInOctets`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("no-results page missing %q", want)
		}
	}
}

func TestSearchNoResultsZeroSuggestions(t *testing.T) {
	// Query with no plausible suggestion — page should still render
	// the "Other places to look" panel.
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/search?q=zzzqqqxxx9999")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	if !strings.Contains(html, "Other places to look") {
		t.Error("no-results page missing fallback nav block")
	}
}

func TestSearchSnippetRendered(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/search?q=octets")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	// FTS5 snippet wraps the matched terms in <mark> markers; our
	// SanitizeSnippet preserves them while escaping everything else.
	if !strings.Contains(html, `<mark>`) {
		t.Error("search results missing <mark> highlights — snippet not rendered as raw HTML?")
	}
	if !strings.Contains(html, `class="search-snippet"`) {
		t.Error("search results missing snippet row")
	}
}

func TestAPIErrorSanitization(t *testing.T) {
	// Bad symbol path → public message only, no internal error leak.
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/symbol/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	// Public message should be the canned form, not contain the URL
	// path or a raw Go error string.
	if got["error"] != "expected /api/v1/symbol/{module}/{name}" {
		t.Errorf("error = %q", got["error"])
	}
}

func TestPaletteFocusTrap(t *testing.T) {
	// Source-level check that the palette JS contains a focus-trap
	// implementation. We can't drive a browser from Go tests, so we
	// assert the load-bearing strings instead — same pattern as
	// TestIslandsRebindOnHTMXSwap from Phase 5 review.
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/static/palette.js")
	if err != nil {
		t.Fatal(err)
	}
	js := body(t, resp)
	for _, marker := range []string{"returnFocusTo", "Focus trap"} {
		if !strings.Contains(js, marker) {
			t.Errorf("palette.js missing focus-trap marker %q", marker)
		}
	}
}

func TestThemeToggleAndBrandMark(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)

	for _, want := range []string{
		`data-theme-toggle`,
		`class="brand-mark"`,
		`bar bar-1`,
		`bar bar-2`,
		`bar bar-3`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("layout missing %q", want)
		}
	}
}

func TestThemeAssetServed(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/static/theme.js")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	js := body(t, resp)
	for _, marker := range []string{"blittermib-theme", "data-theme-toggle"} {
		if !strings.Contains(js, marker) {
			t.Errorf("theme.js missing marker %q", marker)
		}
	}
}

func TestNoGoogleFontsCDN(t *testing.T) {
	// Phase 2.3: fonts must be self-hosted, no third-party CDN refs.
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	for _, banned := range []string{"fonts.googleapis.com", "fonts.gstatic.com"} {
		if strings.Contains(html, banned) {
			t.Errorf("layout still references %q — Phase 2.3 says fonts must be self-hosted", banned)
		}
	}
}

func TestFontAssetServed(t *testing.T) {
	ts := newTestServer(t)
	// Probe every weight `make fetch-fonts` is supposed to fetch — a
	// partial fetch (e.g. only -400 succeeds, -600 404s) would
	// otherwise ship a binary with the bold weight missing and the
	// stack would silently fall through to the system fallback.
	for _, name := range []string{
		"Inter-400.woff2",
		"Inter-500.woff2",
		"Inter-600.woff2",
		"JetBrainsMono-400.woff2",
		"JetBrainsMono-500.woff2",
	} {
		resp, err := http.Get(ts.URL + "/static/fonts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d (fonts not vendored? run `make fetch-fonts`)", name, resp.StatusCode)
		}
	}
}

func TestWorkspaceAssetsServed(t *testing.T) {
	// Phase 3 vendored Alpine.js + shipped two new island scripts.
	// A missing or empty file (forgotten `make fetch-alpine`,
	// stripped embed) would silently degrade the workspace; this
	// test catches that at build time.
	ts := newTestServer(t)
	for _, asset := range []string{
		"/static/alpine.min.js",
		"/static/workspace.js",
		"/static/picker.js",
	} {
		t.Run(asset, func(t *testing.T) {
			resp, err := http.Get(ts.URL + asset)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: status = %d", asset, resp.StatusCode)
			}
			js := body(t, resp)
			if len(js) == 0 {
				t.Errorf("%s: empty body — asset not vendored?", asset)
			}
		})
	}
}

func TestSplitQualified(t *testing.T) {
	cases := []struct {
		in   string
		mod  string
		name string
		ok   bool
	}{
		{"IF-MIB::ifInOctets", "IF-MIB", "ifInOctets", true},
		{"ifInOctets", "", "ifInOctets", false},
		{"A::B::C", "A", "B::C", true},
	}
	for _, c := range cases {
		mod, name, ok := splitQualified(c.in)
		if mod != c.mod || name != c.name || ok != c.ok {
			t.Errorf("splitQualified(%q) = (%q,%q,%v); want (%q,%q,%v)",
				c.in, mod, name, ok, c.mod, c.name, c.ok)
		}
	}
}
