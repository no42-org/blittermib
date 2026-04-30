package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

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
	for _, want := range []string{"IF-MIB", "ifInOctets", "1.3.6.1.2.1.31"} {
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
