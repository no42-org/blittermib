package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// TestGoldenHTML is the visual regression net for the server's
// rendered pages. For each route in the table below, the test
// fetches the response and compares it byte-for-byte against the
// committed file at internal/server/testdata/golden/{name}.html.
//
// On intentional template / styling changes:
//
//	GOLDEN_UPDATE=1 go test ./internal/server -run TestGoldenHTML
//
// rewrites the golden files. Inspect the diff before committing.
//
// Mismatches write the actual output to testdata/golden/{name}.got.html
// alongside the golden so a developer can diff manually.
//
// The fixture is the same as newGoldenServer below — a deliberately
// trimmed IF-MIB shape that exercises every page rhythm element
// (table, table-entry, column, descriptor with content, used-by ref,
// diagnostic, source path).
func TestGoldenHTML(t *testing.T) {
	ts := newGoldenServer(t)

	cases := []struct {
		name string
		path string
	}{
		{"landing", "/"},
		{"module-index", "/m"},
		{"module-detail", "/m/IF-MIB"},
		{"symbol-column", "/s/IF-MIB::ifInOctets"},
		{"symbol-table", "/s/IF-MIB::ifTable"},
		{"symbol-not-found", "/s/IF-MIB::doesNotExist"},
		{"tree-root", "/tree"},
		{"tree-focused", "/tree/1.3.6.1.2.1"},
		{"search-with-results", "/search?q=octets"},
		{"search-empty-query", "/search"},
		{"search-no-results-with-suggestions", "/search?q=ifInOctts"},
		{"search-no-results-no-suggestions", "/search?q=zzzqqq9999"},
		{"diagnostics", "/diagnostics"},
		{"not-found", "/totally-unknown-path"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + c.path)
			if err != nil {
				t.Fatal(err)
			}
			got := body(t, resp)
			assertGolden(t, c.name, got)
		})
	}
}

// TestGoldenEmptyState covers the LandingEmpty branch separately
// because it requires a store with zero modules.
func TestGoldenEmptyState(t *testing.T) {
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := New(st, "", "test", "/var/lib/blittermib/mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "landing-empty", body(t, resp))
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	dir := filepath.Join("testdata", "golden")
	path := filepath.Join(dir, name+".html")

	if os.Getenv("GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated golden %s (%d bytes)", path, len(got))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden %s missing — run with GOLDEN_UPDATE=1 to create", path)
		}
		t.Fatal(err)
	}

	if string(want) != got {
		// Drop the actual output next to the golden so a developer
		// can diff manually. The .got.html files are gitignored.
		gotPath := filepath.Join(dir, name+".got.html")
		_ = os.WriteFile(gotPath, []byte(got), 0o644)
		t.Errorf("golden mismatch: %s\n  expected %d bytes, got %d bytes\n  actual written to %s\n  if intentional, re-run with GOLDEN_UPDATE=1",
			path, len(want), len(got), gotPath)

		// Surface the first divergence to make the diff easier to
		// triage from the test log alone.
		w, g := string(want), got
		max := len(w)
		if len(g) < max {
			max = len(g)
		}
		diverged := false
		for i := 0; i < max; i++ {
			if w[i] != g[i] {
				start := i - 80
				if start < 0 {
					start = 0
				}
				endW := i + 80
				if endW > len(w) {
					endW = len(w)
				}
				endG := i + 80
				if endG > len(g) {
					endG = len(g)
				}
				t.Logf("first divergence at byte %d", i)
				t.Logf("expected: …%s…", strings.ReplaceAll(w[start:endW], "\n", "⏎"))
				t.Logf("actual:   …%s…", strings.ReplaceAll(g[start:endG], "\n", "⏎"))
				diverged = true
				break
			}
		}
		// One string is a strict prefix of the other — the loop
		// found no in-range divergence but lengths differ. Surface
		// the truncation point so the test log isn't silent on what
		// actually went wrong.
		if !diverged && len(w) != len(g) {
			t.Logf("matched as prefix through byte %d; lengths differ — expected %d, got %d", max, len(w), len(g))
		}
	}
}

// newGoldenServer is a self-contained fixture deliberately separated
// from newTestServer so the two are decoupled — a future change to
// the regular server tests' fixture won't silently invalidate every
// committed golden.
func newGoldenServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.ReplaceModule(context.Background(),
		&model.Module{
			Name:         "IF-MIB",
			OIDRoot:      "1.3.6.1.2.1.31",
			Organization: "IETF Interfaces MIB Working Group",
			ContactInfo:  "WG email: ietfmibs@ops.ietf.org",
			Description:  "The MIB module to describe generic objects for network interface sub-layers.",
			LastUpdated:  "2007-09-29 00:00",
			// `warnings` so the diagnostics page actually renders
			// the seeded warning row. Otherwise handleDiagnostics
			// skips this module as clean and the golden would be
			// the empty "all parsed cleanly" branch.
			ParseStatus: model.ParseStatusWarnings,
		},
		[]model.Symbol{
			{
				ModuleName: "IF-MIB", Name: "ifTable",
				OID: "1.3.6.1.2.1.2.2", ParentOID: "1.3.6.1.2.1.2",
				Kind: model.KindObjectType, Syntax: "SEQUENCE OF IfEntry",
				Access: model.AccessNotAccessible, Status: model.StatusCurrent,
				IsTable:     true,
				Description: "A list of interface entries. The number of entries is given by the value of ifNumber.",
			},
			{
				ModuleName: "IF-MIB", Name: "ifEntry",
				OID: "1.3.6.1.2.1.2.2.1", ParentOID: "1.3.6.1.2.1.2.2",
				Kind: model.KindObjectType, Syntax: "IfEntry",
				Access: model.AccessNotAccessible, Status: model.StatusCurrent,
				IsTableEntry: true, IndexColumns: []string{"ifIndex"},
				Description: "An entry containing management information applicable to a particular interface.",
			},
			{
				ModuleName: "IF-MIB", Name: "ifIndex",
				OID: "1.3.6.1.2.1.2.2.1.1", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindObjectType, Syntax: "InterfaceIndex",
				Access: model.AccessReadOnly, Status: model.StatusCurrent,
				Description: "A unique value, greater than zero, for each interface.",
			},
			{
				ModuleName: "IF-MIB", Name: "ifInOctets",
				OID: "1.3.6.1.2.1.2.2.1.10", ParentOID: "1.3.6.1.2.1.2.2.1",
				Kind: model.KindObjectType, Syntax: "Counter32",
				Access: model.AccessReadOnly, Status: model.StatusCurrent,
				Units:       "octets",
				Description: "The total number of octets received on the interface, including framing characters.",
			},
		},
		[]model.Reference{
			{
				SourceModule: "IF-MIB", SourceName: "ifPacketGroup",
				TargetModule: "IF-MIB", TargetName: "ifInOctets",
				Kind: model.RefGroupMember,
			},
		},
		[]model.Diagnostic{
			{File: "IF-MIB.txt", Line: 142, Severity: model.SeverityWarning,
				Code: "compliance-non-current", Message: "stub diagnostic for golden snapshot"},
		},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := New(st, "", "test", "/var/lib/blittermib/mibs")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}
