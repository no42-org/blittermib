package server

import (
	"archive/zip"
	"bytes"
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

	s := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
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

// downloadTestServer seeds a closure A → B → unloaded C with real
// source files for A and B inside the test temp dir, returning the
// httptest.Server bound to those paths as allowed roots.
func downloadTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	mibsDir := filepath.Join(dir, "mibs")
	stdDir := filepath.Join(dir, "data", "standard-mibs")
	if err := os.MkdirAll(mibsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatal(err)
	}

	aPath := filepath.Join(mibsDir, "A-MIB.txt")
	if err := os.WriteFile(aPath, []byte("A-MIB DEFINITIONS ::= BEGIN\nimports b;\nEND\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bPath := filepath.Join(stdDir, "B-MIB")
	if err := os.WriteFile(bPath, []byte("B-MIB DEFINITIONS ::= BEGIN\nimports c;\nEND\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.ReplaceModule(context.Background(),
		&model.Module{
			Name:        "B-MIB",
			SourcePath:  bPath,
			ParseStatus: model.ParseStatusClean,
			Imports:     []model.Import{{FromModule: "C-MIB", Symbol: "Counter32"}},
		}, nil, nil, nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceModule(context.Background(),
		&model.Module{
			Name:        "A-MIB",
			SourcePath:  aPath,
			ParseStatus: model.ParseStatusClean,
			Imports:     []model.Import{{FromModule: "B-MIB", Symbol: "ifIndex"}},
		}, nil, nil, nil,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(st, "", "test", mibsDir, stdDir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

func TestModuleDownloadServesSource(t *testing.T) {
	ts, _ := downloadTestServer(t)
	resp, err := http.Get(ts.URL + "/m/A-MIB/download")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="A-MIB.mib"`) {
		t.Errorf("content-disposition = %q, want filename A-MIB.mib", cd)
	}
	got := body(t, resp)
	if !strings.Contains(got, "A-MIB DEFINITIONS") {
		t.Errorf("body missing source content: %q", got)
	}
}

func TestModuleDownloadPathTraversalRefused(t *testing.T) {
	dir := t.TempDir()
	mibsDir := filepath.Join(dir, "mibs")
	stdDir := filepath.Join(dir, "data", "standard-mibs")
	if err := os.MkdirAll(mibsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// SourcePath outside both roots — write a real file there so a
	// missing guard would actually leak the bytes.
	outsideDir := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(outsideDir, "EVIL-MIB.txt")
	if err := os.WriteFile(bad, []byte("SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReplaceModule(context.Background(),
		&model.Module{
			Name:        "EVIL-MIB",
			SourcePath:  bad,
			ParseStatus: model.ParseStatusClean,
		}, nil, nil, nil,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(st, "", "test", mibsDir, stdDir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/m/EVIL-MIB/download")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for path-traversal-guarded download", resp.StatusCode)
	}
	if got := body(t, resp); strings.Contains(got, "SECRET") {
		t.Errorf("body leaked the unsafe file: %q", got)
	}
}

func TestModuleDownloadBundleZip(t *testing.T) {
	ts, _ := downloadTestServer(t)
	resp, err := http.Get(ts.URL + "/m/A-MIB/download.zip")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type = %q, want application/zip", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="A-MIB-bundle.zip"`) {
		t.Errorf("content-disposition = %q", cd)
	}

	zipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	gotNames := make(map[string]bool)
	for _, f := range zr.File {
		gotNames[f.Name] = true
	}
	for _, want := range []string{"A-MIB.mib", "B-MIB.mib", "MISSING.txt"} {
		if !gotNames[want] {
			t.Errorf("zip missing entry %q (got %v)", want, gotNames)
		}
	}
}

func TestModuleDownloadBundleMissingImports(t *testing.T) {
	ts, _ := downloadTestServer(t)
	resp, err := http.Get(ts.URL + "/m/A-MIB/download.zip")
	if err != nil {
		t.Fatal(err)
	}
	zipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	var missing string
	for _, f := range zr.File {
		if f.Name != "MISSING.txt" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf := new(strings.Builder)
		if _, err := io.Copy(buf, rc); err != nil {
			t.Fatal(err)
		}
		_ = rc.Close()
		missing = buf.String()
		break
	}
	if missing == "" {
		t.Fatal("MISSING.txt not present in bundle")
	}
	for _, want := range []string{
		"C-MIB",
		"imported by: B-MIB",
		"symbols:     Counter32",
		"reason:      not loaded",
	} {
		if !strings.Contains(missing, want) {
			t.Errorf("MISSING.txt missing %q\n--- contents ---\n%s", want, missing)
		}
	}
}

// TestModuleDownloadBundlePathTraversalRefused covers the spec
// scenario "Path-traversal refused (bundle root)": a bundle request
// for a module whose root SourcePath resolves outside the configured
// roots returns 404 with no ZIP bytes — closure walking is skipped
// entirely. Mirrors TestModuleDownloadPathTraversalRefused for the
// single-MIB endpoint.
func TestModuleDownloadBundlePathTraversalRefused(t *testing.T) {
	dir := t.TempDir()
	mibsDir := filepath.Join(dir, "mibs")
	stdDir := filepath.Join(dir, "data", "standard-mibs")
	if err := os.MkdirAll(mibsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(outsideDir, "EVIL-MIB.txt")
	if err := os.WriteFile(bad, []byte("SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReplaceModule(context.Background(),
		&model.Module{
			Name:        "EVIL-MIB",
			SourcePath:  bad,
			ParseStatus: model.ParseStatusClean,
		}, nil, nil, nil,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(st, "", "test", mibsDir, stdDir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/m/EVIL-MIB/download.zip")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for bundle path-traversal", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct == "application/zip" {
		t.Errorf("response advertised application/zip on a refused bundle: %q", ct)
	}
	if got := body(t, resp); strings.Contains(got, "SECRET") {
		t.Errorf("bundle body leaked the unsafe file: %q", got)
	}
}

// TestModuleDownloadInvalidName ensures attacker-controlled URL
// segments don't reach the store — names not matching
// validModuleName 404 at handler entry.
func TestModuleDownloadInvalidName(t *testing.T) {
	ts, _ := downloadTestServer(t)
	cases := []string{
		"/m/1NUMERIC-START/download",
		"/m/has%20space/download",
		"/m/foo$bar/download",
		"/m/1NUMERIC-START/download.zip",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			resp, err := http.Get(ts.URL + p)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s status = %d, want 404", p, resp.StatusCode)
			}
		})
	}
}

// TestModuleDownloadStaleSourceNoPathLeak verifies the 410 Gone
// response body does NOT echo the recorded server-side filesystem
// path — that would leak install layout / OS / container details.
func TestModuleDownloadStaleSourceNoPathLeak(t *testing.T) {
	dir := t.TempDir()
	mibsDir := filepath.Join(dir, "mibs")
	stdDir := filepath.Join(dir, "data", "standard-mibs")
	if err := os.MkdirAll(mibsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Recorded path inside the allowed mibsDir but the file is
	// missing — the path-traversal guard accepts (path is under
	// roots) and os.Open then fails, exercising the 410 branch.
	gonePath := filepath.Join(mibsDir, "GONE-MIB.txt")

	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReplaceModule(context.Background(),
		&model.Module{
			Name:        "GONE-MIB",
			SourcePath:  gonePath,
			ParseStatus: model.ParseStatusClean,
		}, nil, nil, nil,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(st, "", "test", mibsDir, stdDir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/m/GONE-MIB/download")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410 for stale source", resp.StatusCode)
	}
	got := body(t, resp)
	if strings.Contains(got, gonePath) || strings.Contains(got, mibsDir) {
		t.Errorf("body leaked recorded path: %q", got)
	}
	if !strings.Contains(got, "no longer readable") {
		t.Errorf("body missing explanation, got: %q", got)
	}
}

// TestModuleDownloadBundleAlwaysEmitsMissing verifies MISSING.txt is
// present in the ZIP even when every closure entry is shippable —
// machine consumers can rely on its presence rather than inferring
// from absence.
func TestModuleDownloadBundleAlwaysEmitsMissing(t *testing.T) {
	dir := t.TempDir()
	mibsDir := filepath.Join(dir, "mibs")
	stdDir := filepath.Join(dir, "data", "standard-mibs")
	if err := os.MkdirAll(mibsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	soloPath := filepath.Join(mibsDir, "SOLO-MIB.txt")
	if err := os.WriteFile(soloPath, []byte("SOLO-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ReplaceModule(context.Background(),
		&model.Module{
			Name:        "SOLO-MIB",
			SourcePath:  soloPath,
			ParseStatus: model.ParseStatusClean,
		}, nil, nil, nil,
	); err != nil {
		t.Fatal(err)
	}

	srv := New(st, "", "test", mibsDir, stdDir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/m/SOLO-MIB/download.zip")
	if err != nil {
		t.Fatal(err)
	}
	zipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	var missingBody string
	for _, f := range zr.File {
		if f.Name != "MISSING.txt" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf := new(strings.Builder)
		if _, err := io.Copy(buf, rc); err != nil {
			t.Fatal(err)
		}
		_ = rc.Close()
		missingBody = buf.String()
	}
	if missingBody == "" {
		t.Fatal("MISSING.txt not present in clean-closure bundle")
	}
	if !strings.Contains(missingBody, "no missing imports") {
		t.Errorf("clean-closure MISSING.txt missing the no-missing header: %q", missingBody)
	}
}

func TestModuleDownloadRouteDispatch(t *testing.T) {
	ts, _ := downloadTestServer(t)
	cases := []struct {
		path     string
		wantCode int
	}{
		{"/m/A-MIB/download", 200},
		{"/m/A-MIB/download.zip", 200},
		{"/m/A-MIB/source", 200},
		{"/m/A-MIB/1.2.3", 200},          // workspace with missing-OID
		{"/m/A-MIB/download/extra", 200}, // workspace with oid="download/extra"
		{"/m/UNKNOWN/download", 404},
		{"/m/UNKNOWN/download.zip", 404},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + c.path)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != c.wantCode {
				t.Errorf("%s status = %d, want %d", c.path, resp.StatusCode, c.wantCode)
			}
		})
	}
}

func TestPathUnderAny(t *testing.T) {
	dir := t.TempDir()
	mibsDir := filepath.Join(dir, "mibs")
	stdDir := filepath.Join(dir, "data", "standard-mibs")
	if err := os.MkdirAll(mibsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{stdDir, mibsDir}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"empty path", "", false},
		{"under user mibs dir", filepath.Join(mibsDir, "IF-MIB.txt"), true},
		{"under standard-mibs dir", filepath.Join(stdDir, "SNMPv2-SMI"), true},
		{"exact root match", mibsDir, true},
		{"sibling of root", filepath.Join(dir, "elsewhere", "evil.mib"), false},
		{"escape via ../", filepath.Join(mibsDir, "..", "elsewhere", "evil.mib"), false},
		{"absolute outside roots", "/etc/passwd", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathUnderAny(tt.path, roots); got != tt.want {
				t.Errorf("pathUnderAny(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}

	// Empty roots are skipped — having a single empty root in the
	// list MUST NOT be treated as "everything is under root".
	if pathUnderAny("/etc/passwd", []string{""}) {
		t.Error("empty root accepted /etc/passwd")
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
	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
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
	s := New(st, "", "test", "/srv/mibs", "/srv/data/standard-mibs")
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
	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
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
	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
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

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
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
		"/static/trap-simulator.js",
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

// TestTrapSimulatorAddsNoServerRoutes pins the privacy invariant
// from spec § "Trap simulator privacy invariants": the trap
// simulator is implemented entirely client-side, so no handler
// code in `internal/server/` should mention `simulate` or
// `snmptrap` (test files may, this test scans non-test files
// only). Catches regressions where a future PR adds an
// `/api/simulate` endpoint or similar.
func TestTrapSimulatorAddsNoServerRoutes(t *testing.T) {
	dir := filepath.Join("..", "server")
	matches := scanForForbiddenRefs(t, dir, []string{
		"simulate",
		"snmptrap",
		"SimulateTrap",
		"handleTrap",
	})
	if len(matches) > 0 {
		t.Errorf("found forbidden references in non-test handler code (privacy invariant violated):\n%v",
			strings.Join(matches, "\n"))
	}
}

// scanForForbiddenRefs walks `root` looking for any `*.go` file
// that is NOT a `_test.go` or generated `_templ.go` file and
// contains any of the forbidden substrings on a non-comment line.
// Comment-only lines (`// …`) are skipped because the privacy
// invariant is about executable handler code, not docstrings.
// Returns a list of "<path>:<line>: <substring>" matches.
func scanForForbiddenRefs(t *testing.T, root string, forbidden []string) []string {
	t.Helper()
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasSuffix(path, "_templ.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimLeft(line, " \t")
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, sub := range forbidden {
				if strings.Contains(line, sub) {
					hits = append(hits, path+":"+itoa(i+1)+": "+sub)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return hits
}

// itoa is a tiny strconv.Itoa wrapper kept inline so the test
// snippet stays self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestNotificationPageContainsDataAttributes seeds a `linkDown`-
// shaped NOTIFICATION-TYPE plus its OBJECTS-clause varbinds, then
// renders the workspace page and checks that the notify-objects
// list carries the data-* attributes the trap-simulator modal
// reads. Catches regressions where the templ stops emitting them
// or the handler stops populating NotifyVarbind.
func TestNotificationPageContainsDataAttributes(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.ReplaceModule(context.Background(),
		&model.Module{Name: "IF-MIB", OIDRoot: "1.3.6.1.2.1.31", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "IF-MIB", Name: "ifEntry",
				OID: "1.3.6.1.2.1.2.2.1", ParentOID: "1.3.6.1.2.1.2.2",
				Kind: model.KindTableEntry, Syntax: "IfEntry",
				IndexColumns: []string{"ifIndex"},
			},
			{
				ModuleName: "IF-MIB", Name: "ifIndex",
				OID: "1.3.6.1.2.1.2.2.1.1", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "IF-MIB", Name: "ifAdminStatus",
				OID: "1.3.6.1.2.1.2.2.1.7", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
				EnumValues: []model.EnumValue{
					{Name: "up", Number: 1},
					{Name: "down", Number: 2},
					{Name: "testing", Number: 3},
				},
			},
			{
				ModuleName: "IF-MIB", Name: "ifOperStatus",
				OID: "1.3.6.1.2.1.2.2.1.8", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "IF-MIB", Name: "linkDown",
				OID: "1.3.6.1.6.3.1.1.5.3", ParentOID: "1.3.6.1.6.3.1.1.5",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
				Description: "A linkDown trap signifies that the SNMP entity recognizes a failure.",
			},
		},
		[]model.Reference{
			{
				SourceModule: "IF-MIB", SourceName: "linkDown",
				TargetModule: "IF-MIB", TargetName: "ifIndex",
				Kind: model.RefNotificationObject,
			},
			{
				SourceModule: "IF-MIB", SourceName: "linkDown",
				TargetModule: "IF-MIB", TargetName: "ifAdminStatus",
				Kind: model.RefNotificationObject,
			},
			{
				SourceModule: "IF-MIB", SourceName: "linkDown",
				TargetModule: "IF-MIB", TargetName: "ifOperStatus",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Navigate via `?sel=linkDown` (name selector) so the workspace
	// resolves to the notification-type symbol.
	resp, err := http.Get(ts.URL + "/m/IF-MIB?sel=linkDown")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := bodyString(t, resp)

	// `<ul class="notify-objects" data-…>` carrying notification-level
	// metadata — the modal reads these on open to seed its state.
	wantUL := []string{
		`class="notify-objects"`,
		`data-notification-oid="1.3.6.1.6.3.1.1.5.3"`,
		`data-notification-name="linkDown"`,
		`data-notification-module="IF-MIB"`,
		`data-trap-index-mode="indexed"`,
		// data-trap-index-columns is a JSON array; templ's attribute
		// escaping turns `"` into `&#34;`. The substring assertion
		// pins both the column name and its classified syntax.
		`&#34;name&#34;:&#34;ifIndex&#34;`,
		`&#34;syntax&#34;:&#34;INTEGER&#34;`,
	}
	for _, w := range wantUL {
		if !strings.Contains(body, w) {
			t.Errorf("rendered HTML missing %q", w)
		}
	}

	// Per-varbind <li> data-* — the modal reads these to build
	// the varbind value inputs (and the snmptrap type letters).
	wantVarbind := []string{
		`data-name="ifAdminStatus"`,
		`data-oid="1.3.6.1.2.1.2.2.1.7"`,
		`data-syntax="INTEGER"`,
		`data-trap-type-letter="i"`,
		`data-is-column="true"`,
	}
	for _, w := range wantVarbind {
		if !strings.Contains(body, w) {
			t.Errorf("rendered HTML missing %q", w)
		}
	}

	// EnumValues JSON for ifAdminStatus surfaces as a JSON array
	// in the data-enum-values attribute. templ escapes `"` to
	// `&#34;` for attribute safety; the keys are lowercase per
	// model.EnumValue's json struct tags.
	if !strings.Contains(body, `&#34;name&#34;:&#34;up&#34;`) {
		t.Errorf("rendered HTML missing enum-values JSON for ifAdminStatus; notify-objects excerpt:\n%s",
			snippet(body, "notify-objects", 800))
	}
}

// bodyString reads the response body in full and returns it as a
// string. Used by the data-attribute tests above.
func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()
	return string(b)
}

// TestBuildNotifyVarbindsSingleIntegerIndex covers the
// classifier's single-INTEGER-index path: linkDown's OBJECTS sit
// under ifEntry whose INDEX is `{ifIndex}` (an INTEGER column),
// so `buildNotifyVarbinds` should return Mode "indexed" with a
// single INTEGER column descriptor. The trap-simulator modal
// walks Columns to render the row-identity input(s).
func TestBuildNotifyVarbindsSingleIntegerIndex(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "IF-MIB", OIDRoot: "1.3.6.1.2.1.31", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "IF-MIB", Name: "ifEntry",
				OID: "1.3.6.1.2.1.2.2.1", ParentOID: "1.3.6.1.2.1.2.2",
				Kind: model.KindTableEntry, Syntax: "IfEntry",
				IndexColumns: []string{"ifIndex"},
			},
			{
				ModuleName: "IF-MIB", Name: "ifIndex",
				OID: "1.3.6.1.2.1.2.2.1.1", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "IF-MIB", Name: "ifAdminStatus",
				OID: "1.3.6.1.2.1.2.2.1.7", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "IF-MIB", Name: "linkDown",
				OID: "1.3.6.1.6.3.1.1.5.3", ParentOID: "1.3.6.1.6.3.1.1.5",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "IF-MIB", SourceName: "linkDown",
				TargetModule: "IF-MIB", TargetName: "ifAdminStatus",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "IF-MIB", SourceName: "linkDown",
			TargetModule: "IF-MIB", TargetName: "ifAdminStatus",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1; Columns = %#v", len(idx.Columns), idx.Columns)
	}
	if idx.Columns[0].Name != "ifIndex" {
		t.Errorf("Columns[0].Name = %q, want %q", idx.Columns[0].Name, "ifIndex")
	}
	if idx.Columns[0].Syntax != "INTEGER" {
		t.Errorf("Columns[0].Syntax = %q, want %q", idx.Columns[0].Syntax, "INTEGER")
	}
}

// TestBuildNotifyVarbindsIpAddressIndex covers the Tier 1
// IpAddress path: a notification whose OBJECTS share a parent
// entry indexed by a single `IpAddress` column should classify
// to Mode "indexed" with one IpAddress column descriptor — the
// modal renders a dotted-quad text input and composes the
// `.a.b.c.d` suffix per the spec scenario.
func TestBuildNotifyVarbindsIpAddressIndex(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "IP-MIB", OIDRoot: "1.3.6.1.2.1.4", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "IP-MIB", Name: "ipAddrEntry",
				OID: "1.3.6.1.2.1.4.20.1", ParentOID: "1.3.6.1.2.1.4.20",
				Kind: model.KindTableEntry, Syntax: "IpAddrEntry",
				IndexColumns: []string{"ipAdEntAddr"},
			},
			{
				ModuleName: "IP-MIB", Name: "ipAdEntAddr",
				OID: "1.3.6.1.2.1.4.20.1.1", ParentOID: "1.3.6.1.2.1.4.20.1",
				Kind: model.KindColumn, Syntax: "IpAddress",
			},
			{
				ModuleName: "IP-MIB", Name: "ipAdEntIfIndex",
				OID: "1.3.6.1.2.1.4.20.1.2", ParentOID: "1.3.6.1.2.1.4.20.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "IP-MIB", Name: "ipAddrChange",
				OID: "1.3.6.1.4.1.99999.1", ParentOID: "1.3.6.1.4.1.99999",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "IP-MIB", SourceName: "ipAddrChange",
				TargetModule: "IP-MIB", TargetName: "ipAdEntIfIndex",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "IP-MIB", SourceName: "ipAddrChange",
			TargetModule: "IP-MIB", TargetName: "ipAdEntIfIndex",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1; Columns = %#v", len(idx.Columns), idx.Columns)
	}
	if idx.Columns[0].Name != "ipAdEntAddr" {
		t.Errorf("Columns[0].Name = %q, want %q", idx.Columns[0].Name, "ipAdEntAddr")
	}
	if idx.Columns[0].Syntax != "IpAddress" {
		t.Errorf("Columns[0].Syntax = %q, want %q", idx.Columns[0].Syntax, "IpAddress")
	}
}

// TestBuildNotifyVarbindsFixedOctetStringIndex covers the Tier 2
// fixed-size OCTET STRING path: a notification whose OBJECTS share
// a parent entry indexed by a single `MacAddress` column should
// classify to Mode "indexed" with one OCTET STRING column
// descriptor carrying SizeMin=SizeMax=6 — the modal will render a
// hex-bytes input and compose `.{b0}.{b1}.…` (six segments).
func TestBuildNotifyVarbindsFixedOctetStringIndex(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "BRIDGE-MIB", OIDRoot: "1.3.6.1.2.1.17", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "BRIDGE-MIB", Name: "dot1dTpFdbEntry",
				OID: "1.3.6.1.2.1.17.4.3.1", ParentOID: "1.3.6.1.2.1.17.4.3",
				Kind: model.KindTableEntry, Syntax: "Dot1dTpFdbEntry",
				IndexColumns: []string{"dot1dTpFdbAddress"},
			},
			{
				ModuleName: "BRIDGE-MIB", Name: "dot1dTpFdbAddress",
				OID: "1.3.6.1.2.1.17.4.3.1.1", ParentOID: "1.3.6.1.2.1.17.4.3.1",
				Kind: model.KindColumn, Syntax: "MacAddress",
			},
			{
				ModuleName: "BRIDGE-MIB", Name: "dot1dTpFdbStatus",
				OID: "1.3.6.1.2.1.17.4.3.1.3", ParentOID: "1.3.6.1.2.1.17.4.3.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "BRIDGE-MIB", Name: "newRoot",
				OID: "1.3.6.1.2.1.17.0.1", ParentOID: "1.3.6.1.2.1.17.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "BRIDGE-MIB", SourceName: "newRoot",
				TargetModule: "BRIDGE-MIB", TargetName: "dot1dTpFdbStatus",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "BRIDGE-MIB", SourceName: "newRoot",
			TargetModule: "BRIDGE-MIB", TargetName: "dot1dTpFdbStatus",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1; Columns = %#v", len(idx.Columns), idx.Columns)
	}
	col := idx.Columns[0]
	if col.Name != "dot1dTpFdbAddress" {
		t.Errorf("Columns[0].Name = %q, want %q", col.Name, "dot1dTpFdbAddress")
	}
	if col.Syntax != "OCTET STRING" {
		t.Errorf("Columns[0].Syntax = %q, want %q", col.Syntax, "OCTET STRING")
	}
	if col.SizeMin != 6 || col.SizeMax != 6 {
		t.Errorf("Columns[0] size = (%d, %d), want (6, 6)", col.SizeMin, col.SizeMax)
	}
}

// TestBuildNotifyVarbindsExplicitFixedSizeIndex covers the
// SIZE(N)-on-OCTET-STRING path without going through a TC name —
// e.g. a vendor MIB that writes `OCTET STRING (SIZE(8))` directly
// on the index column. The classifier should still emit an
// OCTET STRING descriptor with SizeMin=SizeMax=8.
func TestBuildNotifyVarbindsExplicitFixedSizeIndex(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorKey"},
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorKey",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "OCTET STRING (SIZE(8))",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1", len(idx.Columns))
	}
	if idx.Columns[0].SizeMin != 8 || idx.Columns[0].SizeMax != 8 {
		t.Errorf("size = (%d, %d), want (8, 8)",
			idx.Columns[0].SizeMin, idx.Columns[0].SizeMax)
	}
}

// TestBuildNotifyVarbindsVariableOctetStringNotImplied covers the
// Tier 2 commit 3 variable OCTET STRING path WITHOUT IMPLIED: a
// single-column OCTET STRING index with a variable SIZE range
// (e.g. SIZE(0..255)) classifies as indexed with IsImplied=false,
// SizeMin/SizeMax carrying the constraint bounds. The JS composer
// length-prefixes the encoding (`.{len}.{b0}.{b1}…`) for
// non-IMPLIED variable OCTET STRING.
func TestBuildNotifyVarbindsVariableOctetStringNotImplied(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorName"},
				IndexImplied: false,
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorName",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "OCTET STRING (SIZE(0..255))",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1; Columns = %#v", len(idx.Columns), idx.Columns)
	}
	col := idx.Columns[0]
	if col.Syntax != "OCTET STRING" {
		t.Errorf("Syntax = %q, want %q", col.Syntax, "OCTET STRING")
	}
	if col.SizeMin != 0 || col.SizeMax != 255 {
		t.Errorf("size = (%d, %d), want (0, 255)", col.SizeMin, col.SizeMax)
	}
	if col.IsImplied {
		t.Errorf("IsImplied = true, want false (entry has IndexImplied=false)")
	}
}

// TestBuildNotifyVarbindsVariableOctetStringImplied is the
// IMPLIED variant: same syntax, but the parent entry's
// `IndexImplied` flag is set, so the column descriptor must
// surface IsImplied=true. The JS composer drops the length
// prefix and emits bare bytes for IMPLIED variable OCTET STRING.
func TestBuildNotifyVarbindsVariableOctetStringImplied(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorName"},
				IndexImplied: true,
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorName",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "OCTET STRING",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1", len(idx.Columns))
	}
	if !idx.Columns[0].IsImplied {
		t.Errorf("IsImplied = false, want true (entry has IndexImplied=true)")
	}
}

// TestBuildNotifyVarbindsOIDIndex covers a single-column
// OBJECT IDENTIFIER index without IMPLIED. The classifier emits
// an indexed descriptor with Syntax="OBJECT IDENTIFIER" and
// IsImplied=false; the JS composer length-prefixes the dotted
// segments.
func TestBuildNotifyVarbindsOIDIndex(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorOID"},
				IndexImplied: false,
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorOID",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "OBJECT IDENTIFIER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1", len(idx.Columns))
	}
	col := idx.Columns[0]
	if col.Syntax != "OBJECT IDENTIFIER" {
		t.Errorf("Syntax = %q, want %q", col.Syntax, "OBJECT IDENTIFIER")
	}
	if col.IsImplied {
		t.Errorf("IsImplied = true, want false")
	}
}

// TestBuildNotifyVarbindsBitsIndex covers a single-column BITS
// index: the classifier derives the column's byte count from
// the named-bits list (`ceil((maxBit+1)/8)`) and emits an
// indexed descriptor with Syntax="BITS", SizeMin=SizeMax set to
// the derived byte count, and IsImplied=false. The JS composer
// reuses the fixed-OCTET-STRING path since BITS encodes
// identically on the wire.
func TestBuildNotifyVarbindsBitsIndex(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorFlags"},
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorFlags",
				OID:       "1.3.6.1.4.1.99999.1.1.1",
				ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind:      model.KindColumn, Syntax: "BITS",
				EnumValues: []model.EnumValue{
					{Name: "red", Number: 0},
					{Name: "green", Number: 1},
					{Name: "blue", Number: 2},
				},
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1", len(idx.Columns))
	}
	col := idx.Columns[0]
	if col.Syntax != "BITS" {
		t.Errorf("Syntax = %q, want %q", col.Syntax, "BITS")
	}
	// Three bits, max bit 2 → ceil(3/8) = 1 byte.
	if col.SizeMin != 1 || col.SizeMax != 1 {
		t.Errorf("size = (%d, %d), want (1, 1) for max-bit=2",
			col.SizeMin, col.SizeMax)
	}
	if col.IsImplied {
		t.Errorf("IsImplied = true, want false (BITS is fixed-size, IMPLIED is inert)")
	}
}

// TestBuildNotifyVarbindsBitsIndexEmptyFallsBack covers the
// degenerate case of an empty BITS definition (no named bits).
// `bitsBytes` returns 0 and the classifier drops to raw-suffix —
// there's no usable size to render, and emitting an indexed
// descriptor with size 0 would break the composer.
func TestBuildNotifyVarbindsBitsIndexEmptyFallsBack(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorFlags"},
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorFlags",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "BITS",
				// No EnumValues — empty BITS definition.
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "raw-suffix" {
		t.Errorf("Mode = %q, want %q (empty BITS has no usable size)",
			idx.Mode, "raw-suffix")
	}
}

// TestBuildNotifyVarbindsOIDIndexImplied is the IMPLIED variant
// of the OID test: parent entry's IndexImplied flag flows through
// to the column descriptor, telling the JS composer to drop the
// length prefix.
func TestBuildNotifyVarbindsOIDIndexImplied(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorOID"},
				IndexImplied: true,
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorOID",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "OBJECT IDENTIFIER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Errorf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1", len(idx.Columns))
	}
	if !idx.Columns[0].IsImplied {
		t.Errorf("IsImplied = false, want true (entry has IndexImplied=true)")
	}
}

// TestBuildNotifyVarbindsCompositeIntegerIpAddress covers the
// canonical two-column INDEX from `ipNetToMediaTable`:
// `INDEX { ifIndex, ipNetToMediaNetAddress }`. The classifier
// must walk both columns and emit a Columns slice in INDEX-clause
// order — INTEGER then IpAddress.
func TestBuildNotifyVarbindsCompositeIntegerIpAddress(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "IP-MIB", OIDRoot: "1.3.6.1.2.1.4", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "IP-MIB", Name: "ipNetToMediaEntry",
				OID: "1.3.6.1.2.1.4.22.1", ParentOID: "1.3.6.1.2.1.4.22",
				Kind: model.KindTableEntry, Syntax: "IpNetToMediaEntry",
				IndexColumns: []string{"ipNetToMediaIfIndex", "ipNetToMediaNetAddress"},
			},
			{
				ModuleName: "IP-MIB", Name: "ipNetToMediaIfIndex",
				OID: "1.3.6.1.2.1.4.22.1.1", ParentOID: "1.3.6.1.2.1.4.22.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "IP-MIB", Name: "ipNetToMediaNetAddress",
				OID: "1.3.6.1.2.1.4.22.1.3", ParentOID: "1.3.6.1.2.1.4.22.1",
				Kind: model.KindColumn, Syntax: "IpAddress",
			},
			{
				ModuleName: "IP-MIB", Name: "ipNetToMediaPhysAddress",
				OID: "1.3.6.1.2.1.4.22.1.2", ParentOID: "1.3.6.1.2.1.4.22.1",
				Kind: model.KindColumn, Syntax: "PhysAddress",
			},
			{
				ModuleName: "IP-MIB", Name: "arpChange",
				OID: "1.3.6.1.4.1.99999.1", ParentOID: "1.3.6.1.4.1.99999",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "IP-MIB", SourceName: "arpChange",
				TargetModule: "IP-MIB", TargetName: "ipNetToMediaPhysAddress",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "IP-MIB", SourceName: "arpChange",
			TargetModule: "IP-MIB", TargetName: "ipNetToMediaPhysAddress",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Fatalf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 2 {
		t.Fatalf("Columns len = %d, want 2; Columns = %#v", len(idx.Columns), idx.Columns)
	}
	if idx.Columns[0].Name != "ipNetToMediaIfIndex" || idx.Columns[0].Syntax != "INTEGER" {
		t.Errorf("Columns[0] = %#v, want {Name:ipNetToMediaIfIndex, Syntax:INTEGER}", idx.Columns[0])
	}
	if idx.Columns[1].Name != "ipNetToMediaNetAddress" || idx.Columns[1].Syntax != "IpAddress" {
		t.Errorf("Columns[1] = %#v, want {Name:ipNetToMediaNetAddress, Syntax:IpAddress}", idx.Columns[1])
	}
}

// TestBuildNotifyVarbindsCompositeImpliedAppliesOnlyToLast pins
// the SMIv2 §7.7 rule: when a multi-column INDEX clause has the
// IMPLIED keyword, only the LAST column may inherit it. Middle
// variable-length columns must always force IsImplied=false so
// they length-prefix on the wire — there's no other way to
// delimit a variable-length component in the middle of the OID.
//
// Setup: INDEX { vendorTag (variable OCTET STRING), vendorPath (OID) }
// with parent entry IndexImplied=true. Expect Columns[0].IsImplied
// = false (mid, must length-prefix); Columns[1].IsImplied = true
// (last, inherits IMPLIED).
func TestBuildNotifyVarbindsCompositeImpliedAppliesOnlyToLast(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorTag", "vendorPath"},
				IndexImplied: true,
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorTag",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "OCTET STRING (SIZE(0..32))",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorPath",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "OBJECT IDENTIFIER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.3", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Fatalf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 2 {
		t.Fatalf("Columns len = %d, want 2", len(idx.Columns))
	}
	if idx.Columns[0].IsImplied {
		t.Errorf("Columns[0] (mid, variable OCTET STRING) IsImplied = true, want false (must length-prefix on the wire)")
	}
	if !idx.Columns[1].IsImplied {
		t.Errorf("Columns[1] (last, OID) IsImplied = false, want true (inherits IndexImplied=true)")
	}
}

// TestBuildNotifyVarbindsInetAddressTypeAlone pins the
// classifier branch ordering: a single-column `InetAddressType`
// INDEX must classify as Syntax="InetAddressType", NOT as plain
// "INTEGER". InetAddressType is an enum-typed integer TC; if a
// future drive-by change adds it to `isIntegerSyntax` (a
// reasonable-looking simplification), the dedicated branch
// becomes unreachable, the modal renders a numeric input
// instead of the typed `<select>`, and no other test catches
// the regression.
func TestBuildNotifyVarbindsInetAddressTypeAlone(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorAddrEntry",
				IndexColumns: []string{"vendorAddrType"},
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrType",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "InetAddressType",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorAddrChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorAddrState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorAddrChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorAddrState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Fatalf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1", len(idx.Columns))
	}
	if idx.Columns[0].Syntax != "InetAddressType" {
		t.Errorf("Columns[0].Syntax = %q, want %q (branch ordering: InetAddressType MUST be caught before isIntegerSyntax)",
			idx.Columns[0].Syntax, "InetAddressType")
	}
}

// TestBuildNotifyVarbindsInetAddressFamilyComposite covers the
// canonical RFC 4001 discriminator pattern: a two-column INDEX
// of `{ InetAddressType, InetAddress }`. The first column should
// classify with Syntax="InetAddressType" (so the modal renders
// a typed `<select>` of the standard enum), the second should
// classify as variable OCTET STRING with IsImplied honoring the
// parent entry's IndexImplied bit.
func TestBuildNotifyVarbindsInetAddressFamilyComposite(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorAddrEntry",
				IndexColumns: []string{"vendorAddrType", "vendorAddrValue"},
				IndexImplied: true,
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrType",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "InetAddressType",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrValue",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "InetAddress",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrState",
				OID: "1.3.6.1.4.1.99999.1.1.3", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorAddrChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorAddrChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorAddrState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorAddrChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorAddrState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Fatalf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 2 {
		t.Fatalf("Columns len = %d, want 2", len(idx.Columns))
	}
	if idx.Columns[0].Syntax != "InetAddressType" {
		t.Errorf("Columns[0].Syntax = %q, want %q", idx.Columns[0].Syntax, "InetAddressType")
	}
	if idx.Columns[0].IsImplied {
		t.Errorf("Columns[0] (InetAddressType, mid) IsImplied = true, want false")
	}
	if idx.Columns[1].Syntax != "OCTET STRING" {
		t.Errorf("Columns[1].Syntax = %q, want %q", idx.Columns[1].Syntax, "OCTET STRING")
	}
	if !idx.Columns[1].IsImplied {
		t.Errorf("Columns[1] (InetAddress, last) IsImplied = false, want true (entry has IndexImplied=true)")
	}
}

// TestBuildNotifyVarbindsInetAddressIPv4UsesIpAddressUI pins
// the UX shortcut: InetAddressIPv4 columns are fixed 4 bytes
// just like IpAddress, so the classifier emits Syntax="IpAddress"
// to give the user a friendly dotted-quad input rather than a
// 4-byte hex input. The dotted-suffix encoding is byte-for-byte
// identical (`.{a}.{b}.{c}.{d}`), so there's no correctness cost.
func TestBuildNotifyVarbindsInetAddressIPv4UsesIpAddressUI(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorIPv4Addr"},
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorIPv4Addr",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "InetAddressIPv4",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "indexed" {
		t.Fatalf("Mode = %q, want %q", idx.Mode, "indexed")
	}
	if len(idx.Columns) != 1 {
		t.Fatalf("Columns len = %d, want 1", len(idx.Columns))
	}
	if idx.Columns[0].Syntax != "IpAddress" {
		t.Errorf("Columns[0].Syntax = %q, want %q (UX shortcut: dotted-quad input)",
			idx.Columns[0].Syntax, "IpAddress")
	}
}

// TestBuildNotifyVarbindsCompositeUnknownColumnFallsBack pins
// the all-or-nothing classifier contract: if any column in a
// multi-column INDEX has a syntax the classifier doesn't
// recognise, the entire row drops to raw-suffix mode. Partial
// classification would compose a malformed dotted suffix —
// raw-suffix preserves user agency at the cost of one freeform
// input.
func TestBuildNotifyVarbindsCompositeUnknownColumnFallsBack(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.ReplaceModule(ctx,
		&model.Module{Name: "VENDOR-MIB", OIDRoot: "1.3.6.1.4.1.99999", ParseStatus: model.ParseStatusClean},
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "vendorEntry",
				OID: "1.3.6.1.4.1.99999.1.1", ParentOID: "1.3.6.1.4.1.99999.1",
				Kind: model.KindTableEntry, Syntax: "VendorEntry",
				IndexColumns: []string{"vendorTag", "vendorWeird"},
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorTag",
				OID: "1.3.6.1.4.1.99999.1.1.1", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorWeird",
				OID: "1.3.6.1.4.1.99999.1.1.2", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "VendorOpaqueTC",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorState",
				OID: "1.3.6.1.4.1.99999.1.1.3", ParentOID: "1.3.6.1.4.1.99999.1.1",
				Kind: model.KindColumn, Syntax: "INTEGER",
			},
			{
				ModuleName: "VENDOR-MIB", Name: "vendorChange",
				OID: "1.3.6.1.4.1.99999.0.1", ParentOID: "1.3.6.1.4.1.99999.0",
				Kind: model.KindNotificationType, Status: model.StatusCurrent,
			},
		},
		[]model.Reference{
			{
				SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
				TargetModule: "VENDOR-MIB", TargetName: "vendorState",
				Kind: model.RefNotificationObject,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	refs := []model.Reference{
		{
			SourceModule: "VENDOR-MIB", SourceName: "vendorChange",
			TargetModule: "VENDOR-MIB", TargetName: "vendorState",
			Kind: model.RefNotificationObject,
		},
	}
	_, idx := srv.buildNotifyVarbinds(ctx, refs)

	if idx.Mode != "raw-suffix" {
		t.Errorf("Mode = %q, want %q (one unknown column → all-or-nothing fallback)",
			idx.Mode, "raw-suffix")
	}
}

// seedModuleWithSymbols is a small helper used by the type-defs
// integration tests to build an in-memory store and an httptest
// server with one module's symbols. Returns the test server URL.
func seedModuleWithSymbols(t *testing.T, modName, oidRoot string, syms []model.Symbol) string {
	t.Helper()
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.ReplaceModule(context.Background(),
		&model.Module{Name: modName, OIDRoot: oidRoot, ParseStatus: model.ParseStatusClean},
		syms,
		nil,
		nil,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := New(st, "", "test", "/var/lib/blittermib/mibs", "/var/lib/blittermib/data/standard-mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

// TestTypeDefsBarHiddenWhenEmpty pins the suppression rule: a
// module that declares no Textual Conventions renders no
// `<details class="type-defs">` element. The empty-bar would be
// noise on RFC1213-style modules (most of them).
func TestTypeDefsBarHiddenWhenEmpty(t *testing.T) {
	url := seedModuleWithSymbols(t, "PLAIN-MIB", "1.3.6.1.4.1.99",
		[]model.Symbol{
			{
				ModuleName: "PLAIN-MIB", Name: "thingScalar",
				OID: "1.3.6.1.4.1.99.1", ParentOID: "1.3.6.1.4.1.99",
				Kind: model.KindScalar, Syntax: "INTEGER",
			},
		},
	)
	resp, err := http.Get(url + "/m/PLAIN-MIB")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := bodyString(t, resp)
	if strings.Contains(body, `class="type-defs"`) {
		t.Errorf("type-defs bar rendered for a module with zero TCs; body excerpt:\n%s",
			snippet(body, "type-defs", 200))
	}
}

// TestTypeDefsBarRendersCount seeds a 3-TC module shaped like
// IF-MIB and asserts the summary count + a representative row
// link.
func TestTypeDefsBarRendersCount(t *testing.T) {
	url := seedModuleWithSymbols(t, "IF-MIB", "1.3.6.1.2.1.31",
		[]model.Symbol{
			{
				ModuleName: "IF-MIB", Name: "InterfaceIndex",
				Kind: model.KindTextualConvention, Syntax: "Integer32 (1..2147483647)",
			},
			{
				ModuleName: "IF-MIB", Name: "InterfaceIndexOrZero",
				Kind: model.KindTextualConvention, Syntax: "Integer32 (0..2147483647)",
			},
			{
				ModuleName: "IF-MIB", Name: "OwnerString",
				Kind: model.KindTextualConvention, Syntax: "OCTET STRING (SIZE(0..255))",
			},
		},
	)
	resp, err := http.Get(url + "/m/IF-MIB")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyString(t, resp)

	wants := []string{
		`class="type-defs"`,
		`Type Definitions`,
		`(3)`,
		`href="/m/IF-MIB?sel=InterfaceIndex"`,
		`<code>InterfaceIndex</code>`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("rendered HTML missing %q; type-defs excerpt:\n%s",
				w, snippet(body, "type-defs", 800))
		}
	}
}

// TestTypeDefsBarRendersParsedShapes pins that the parser's output
// reaches the rendered HTML for each recognised shape — pill text
// + constraint phrase appear as plain HTML strings in the body.
func TestTypeDefsBarRendersParsedShapes(t *testing.T) {
	url := seedModuleWithSymbols(t, "SHAPES-MIB", "1.3.6.1.4.1.99",
		[]model.Symbol{
			{
				ModuleName: "SHAPES-MIB", Name: "RangeTC",
				Kind: model.KindTextualConvention, Syntax: "Integer32 (1..2147483647)",
			},
			{
				ModuleName: "SHAPES-MIB", Name: "FixedSizeTC",
				Kind: model.KindTextualConvention, Syntax: "OCTET STRING (SIZE(6))",
			},
			{
				ModuleName: "SHAPES-MIB", Name: "VarSizeTC",
				Kind: model.KindTextualConvention, Syntax: "OCTET STRING (SIZE(0..255))",
			},
			{
				ModuleName: "SHAPES-MIB", Name: "EnumTC",
				Kind: model.KindTextualConvention, Syntax: "INTEGER { up(1), down(2), testing(3) }",
				EnumValues: []model.EnumValue{
					{Name: "up", Number: 1},
					{Name: "down", Number: 2},
					{Name: "testing", Number: 3},
				},
			},
			{
				ModuleName: "SHAPES-MIB", Name: "BitsTC",
				Kind: model.KindTextualConvention, Syntax: "BITS { read(0), write(1) }",
				EnumValues: []model.EnumValue{
					{Name: "read", Number: 0},
					{Name: "write", Number: 1},
				},
			},
			{
				ModuleName: "SHAPES-MIB", Name: "PlainTC",
				Kind: model.KindTextualConvention, Syntax: "Counter32",
			},
		},
	)
	resp, err := http.Get(url + "/m/SHAPES-MIB")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyString(t, resp)

	wants := []string{
		`>Integer32<`, `range: 1..2147483647`,
		`>OctetString<`, `size: 6`, `size: 0..255`,
		`>Integer<`, `enum: 3 values`,
		`>BITS<`, `2 flags`,
		`>Counter32<`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("rendered HTML missing %q; type-defs excerpt:\n%s",
				w, snippet(body, "type-defs", 1200))
		}
	}
}

// TestTypeDefsBarRawFallback pins the verbatim fallback for
// unrecognised vendor-TC syntaxes — the leading token reaches
// the pill and the parenthesised remainder reaches the
// constraint slot, parens preserved so the user sees the
// shape that wasn't recognised.
func TestTypeDefsBarRawFallback(t *testing.T) {
	url := seedModuleWithSymbols(t, "VENDOR-MIB", "1.3.6.1.4.1.42",
		[]model.Symbol{
			{
				ModuleName: "VENDOR-MIB", Name: "QuirkyTC",
				Kind: model.KindTextualConvention, Syntax: "VendorMagicTC (mystery)",
			},
		},
	)
	resp, err := http.Get(url + "/m/VENDOR-MIB")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyString(t, resp)

	wants := []string{
		`>VendorMagicTC<`,
		`(mystery)`,
		`href="/m/VENDOR-MIB?sel=QuirkyTC"`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("rendered HTML missing %q; type-defs excerpt:\n%s",
				w, snippet(body, "type-defs", 600))
		}
	}
}

// TestDeprecatedSymbolRendersStatusPill seeds a module mixing
// `current` and `deprecated` symbols across kinds (column, TC)
// and asserts the rendered HTML carries the per-row status
// modifier class plus the inline `deprecated` pill on the
// affected rows.
func TestDeprecatedSymbolRendersStatusPill(t *testing.T) {
	url := seedModuleWithSymbols(t, "MIXED-MIB", "1.3.6.1.4.1.42",
		[]model.Symbol{
			{
				ModuleName: "MIXED-MIB", Name: "currentScalar",
				OID: "1.3.6.1.4.1.42.1", ParentOID: "1.3.6.1.4.1.42",
				Kind: model.KindScalar, Syntax: "INTEGER",
				Status: model.StatusCurrent,
			},
			{
				ModuleName: "MIXED-MIB", Name: "deprecatedScalar",
				OID: "1.3.6.1.4.1.42.2", ParentOID: "1.3.6.1.4.1.42",
				Kind: model.KindScalar, Syntax: "Counter32",
				Status: model.StatusDeprecated,
			},
			{
				ModuleName: "MIXED-MIB", Name: "DeprecatedTC",
				Kind: model.KindTextualConvention, Syntax: "OctetString",
				Status: model.StatusDeprecated,
			},
		},
	)
	resp, err := http.Get(url + "/m/MIXED-MIB")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyString(t, resp)

	wants := []string{
		// Per-row modifier on the deprecated list-pane row.
		`status-deprecated`,
		// Inline pill text — appears at least twice
		// (deprecated scalar in the list, deprecated TC in
		// the type-defs bar).
		`>deprecated</span>`,
		// Pill class chains with the status string from
		// model.Status — matches the CSS rules.
		`class="status-pill deprecated"`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("rendered HTML missing %q; body excerpt:\n%s",
				w, snippet(body, "deprecated", 800))
		}
	}

}

// TestTypeDefsBarEnumValuesEmbedded pins that an enum TC's named
// values reach the rendered DOM as `<li>name(number)</li>`
// entries inside the row's enum list. The toggle is client-side
// (Alpine), so the values must be in the markup regardless of
// the open / closed state.
func TestTypeDefsBarEnumValuesEmbedded(t *testing.T) {
	url := seedModuleWithSymbols(t, "STATUS-MIB", "1.3.6.1.4.1.42",
		[]model.Symbol{
			{
				ModuleName: "STATUS-MIB", Name: "IfAdminStatus",
				Kind: model.KindTextualConvention, Syntax: "INTEGER { up(1), down(2), testing(3) }",
				EnumValues: []model.EnumValue{
					{Name: "up", Number: 1},
					{Name: "down", Number: 2},
					{Name: "testing", Number: 3},
				},
			},
		},
	)
	resp, err := http.Get(url + "/m/STATUS-MIB")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyString(t, resp)

	wants := []string{
		`type-defs-enum-toggle`,
		`type-defs-enum-list`,
		`<li>up(1)</li>`,
		`<li>down(2)</li>`,
		`<li>testing(3)</li>`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("rendered HTML missing %q; type-defs excerpt:\n%s",
				w, snippet(body, "type-defs", 1200))
		}
	}
}

// snippet returns a trimmed window of `body` around the first
// occurrence of `marker` for use in test failure messages —
// dumping the full body of a workspace page is unhelpful noise.
func snippet(body, marker string, window int) string {
	i := strings.Index(body, marker)
	if i < 0 {
		return "(marker not found)"
	}
	start := i - window/2
	if start < 0 {
		start = 0
	}
	end := i + window/2
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}
