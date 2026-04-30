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
				Kind: model.KindObjectType, Syntax: "SEQUENCE OF IfEntry",
				Access: model.AccessNotAccessible, Status: model.StatusCurrent,
				IsTable: true, Description: "A list of interface entries.",
			},
			{
				ModuleName: "IF-MIB", Name: "ifEntry",
				OID: "1.3.6.1.2.1.2.2.1", ParentOID: "1.3.6.1.2.1.2.2",
				Kind: model.KindObjectType, Syntax: "IfEntry",
				Access: model.AccessNotAccessible, Status: model.StatusCurrent,
				IsTableEntry: true, IndexColumns: []string{"ifIndex"},
			},
			{
				ModuleName: "IF-MIB", Name: "ifIndex",
				OID: "1.3.6.1.2.1.2.2.1.1", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindObjectType, Syntax: "InterfaceIndex",
				Access: model.AccessReadOnly, Status: model.StatusCurrent,
			},
			{
				ModuleName: "IF-MIB", Name: "ifInOctets",
				OID: "1.3.6.1.2.1.2.2.1.10", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindObjectType, Syntax: "Counter32",
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
	for _, want := range []string{"IF-MIB", "ifInOctets", `class="oid"`, `<span class="dot">.</span>`} {
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
	if loc := resp.Header.Get("Location"); loc != "/s/IF-MIB::ifInOctets" {
		t.Errorf("location = %q", loc)
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
			for _, want := range []string{`/static/htmx.min.js`, `hx-boost="true"`} {
				if !strings.Contains(html, want) {
					t.Errorf("page missing %q", want)
				}
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
	for _, asset := range []string{"/static/palette.js", "/static/glossary.js"} {
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
			{ModuleName: "MM", Name: "noDesc", OID: "1.2", Kind: model.KindObjectType,
				Status: model.StatusCurrent},
		}, nil, nil); err != nil {
		t.Fatal(err)
	}
	srv := New(st, "", "test", "/var/lib/blittermib/mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, _ := http.Get(ts.URL + "/s/MM::noDesc")
	html := body(t, resp)
	if !strings.Contains(html, "object-type in MM") {
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
	if loc := resp.Header.Get("Location"); loc != "/s/IF-MIB::ifInOctets" {
		t.Errorf("location = %q", loc)
	}
}

func TestSymbolDisambiguationChooser(t *testing.T) {
	// Seed two modules that both export "common" — multiple matches.
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, m := range []string{"A-MIB", "B-MIB"} {
		if err := st.ReplaceModule(context.Background(),
			&model.Module{Name: m, ParseStatus: model.ParseStatusClean},
			[]model.Symbol{{ModuleName: m, Name: "common", OID: "1." + m,
				Kind: model.KindObjectType, Status: model.StatusCurrent}},
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
	for _, want := range []string{"Multiple modules", "A-MIB::common", "B-MIB::common"} {
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
	for _, name := range []string{"Inter-400.woff2", "JetBrainsMono-400.woff2"} {
		resp, err := http.Get(ts.URL + "/static/fonts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d (fonts not vendored? run `make fetch-fonts`)", name, resp.StatusCode)
		}
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
