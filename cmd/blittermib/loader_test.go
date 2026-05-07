package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/server"
	"github.com/no42-org/blittermib/internal/store"
)

func TestRejectReason(t *testing.T) {
	cases := []struct {
		name       string
		result     compile.Result
		wantOK     bool
		wantReason string
	}{
		{
			name:   "nil module",
			result: compile.Result{Module: nil},
			wantOK: false,
		},
		{
			name: "empty module name",
			result: compile.Result{
				Module: &model.Module{Name: ""},
			},
			wantOK: false,
		},
		{
			name: "phantom: zero symbols, zero imports",
			result: compile.Result{
				Module:  &model.Module{Name: "Hello"},
				Symbols: nil,
			},
			wantOK: false,
		},
		{
			name: "macro module: zero symbols, has imports",
			result: compile.Result{
				Module: &model.Module{
					Name: "SNMPv2-CONF",
					Imports: []model.Import{
						{FromModule: "SNMPv2-SMI", Symbol: "MODULE-IDENTITY"},
					},
				},
				Symbols: nil,
			},
			wantOK: true,
		},
		{
			name: "normal module: has symbols and imports",
			result: compile.Result{
				Module: &model.Module{
					Name: "IF-MIB",
					Imports: []model.Import{
						{FromModule: "SNMPv2-SMI", Symbol: "Counter32"},
					},
				},
				Symbols: []model.Symbol{
					{Name: "ifInOctets", Kind: model.KindScalar},
				},
			},
			wantOK: true,
		},
		{
			name: "symbol-only module: kept (defensive — legitimate parsers may omit imports)",
			result: compile.Result{
				Module: &model.Module{Name: "MINIMAL-MIB"},
				Symbols: []model.Symbol{
					{Name: "foo", Kind: model.KindScalar},
				},
			},
			wantOK: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, ok := rejectReason(c.result)
			if ok != c.wantOK {
				t.Errorf("ok = %v (reason %q), want %v", ok, reason, c.wantOK)
			}
			if !ok && reason == "" {
				t.Error("rejected but no reason given")
			}
		})
	}
}

// minimalMIB is a hand-crafted SMIv2 module that smidump 0.5.0
// accepts cleanly. Just enough machinery (MODULE-IDENTITY pointing
// at a private OID, one OBJECT-IDENTITY child) to land a row in
// `module` plus a row in `symbol`.
const minimalMIB = `BLITTERMIB-E2E-MIB DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, OBJECT-IDENTITY, enterprises
        FROM SNMPv2-SMI;

testRoot MODULE-IDENTITY
    LAST-UPDATED "202605030000Z"
    ORGANIZATION "blittermib e2e"
    CONTACT-INFO "test@example.invalid"
    DESCRIPTION  "Probe MIB for the compile->store->download e2e test."
    ::= { enterprises 99999 }

testProbe OBJECT-IDENTITY
    STATUS  current
    DESCRIPTION "Probe object."
    ::= { testRoot 1 }

END
`

// TestE2E_CompileStoreDownload exercises the full production path:
// real smidump compiles a real MIB file, the result lands in the
// store, and an HTTP request to /m/{name}/download serves the
// recorded source bytes back. Catches the SourcePath plumbing
// regression where smidump 0.5.0's XML omits `path=`, ToModel
// produces empty SourcePath, and every download endpoint 404s.
//
// Skips when smidump isn't on PATH. The unit-level coverage in
// internal/compile/compiler_test.go (TestCompiler_BackfillsSource…)
// guards the back-fill logic without that dependency.
func TestE2E_CompileStoreDownload(t *testing.T) {
	if _, err := exec.LookPath("smidump"); err != nil {
		t.Skip("smidump not on PATH — skipping e2e download probe")
	}

	dir := t.TempDir()
	mibsDir := filepath.Join(dir, "mibs")
	stdDir := filepath.Join(dir, "data", "standard-mibs")
	if err := os.MkdirAll(mibsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mibPath := filepath.Join(mibsDir, "BLITTERMIB-E2E-MIB.mib")
	if err := os.WriteFile(mibPath, []byte(minimalMIB), 0o644); err != nil {
		t.Fatalf("write MIB: %v", err)
	}

	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	importPaths := []string{mibsDir, stdDir}
	ldr := &loader{
		compiler: &compile.Compiler{
			Smidump: &compile.Smidump{Path: "smidump", Paths: importPaths},
			Smilint: &compile.Smilint{Path: "smilint", Paths: importPaths},
		},
		store: st,
	}
	if err := ldr.loadFiles(context.Background(), []string{mibPath}); err != nil {
		t.Fatalf("loadFiles: %v", err)
	}

	mod, err := st.GetModule(context.Background(), "BLITTERMIB-E2E-MIB")
	if err != nil {
		t.Fatalf("GetModule: %v", err)
	}
	if mod.SourcePath == "" {
		t.Fatal("SourcePath empty after compile + store — back-fill regressed")
	}
	if !filepath.IsAbs(mod.SourcePath) {
		t.Errorf("SourcePath = %q, want absolute path", mod.SourcePath)
	}

	srv := server.New(st, "", "test", mibsDir, stdDir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/m/BLITTERMIB-E2E-MIB/download")
	if err != nil {
		t.Fatalf("GET /download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="BLITTERMIB-E2E-MIB.mib"`) {
		t.Errorf("Content-Disposition = %q, want filename BLITTERMIB-E2E-MIB.mib", cd)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "BLITTERMIB-E2E-MIB DEFINITIONS") {
		t.Errorf("body missing source content (first 200 bytes): %q", string(body[:min(200, len(body))]))
	}
}

// TestLoaderRecursiveWalk seeds a temp tree mirroring the post-mib-corpus
// corpus layout and asserts walkMIBFiles returns only the real MIB files —
// hidden directories, symlinks, and non-MIB files (LICENSE, README) are
// filtered out by the recursive walk's skip rules + lexical-marker check.
func TestLoaderRecursiveWalk(t *testing.T) {
	root := t.TempDir()

	// Layout:
	//   root/ietf/core/SNMPv2-SMI                ← MIB (kept)
	//   root/vendors/9-cisco/CISCO-EXAMPLE-MIB   ← MIB (kept)
	//   root/.git/HEAD                           ← hidden dir, skipped
	//   root/vendors/9-cisco/LICENSE             ← extensionless, no marker
	//   root/vendors/9-cisco/README.md           ← wrong extension
	//   root/vendors/9-cisco/SHIM.mib            ← MIB extension but no marker
	//   root/.hidden-mib                         ← hidden file at root
	//   root/symlink-mib                         ← symlink to outside file
	mib := func(name string) string {
		return name + " DEFINITIONS ::= BEGIN\nEND\n"
	}
	cases := []struct {
		path, body string
	}{
		{filepath.Join(root, "ietf/core/SNMPv2-SMI"), mib("SNMPv2-SMI")},
		{filepath.Join(root, "vendors/9-cisco/CISCO-EXAMPLE-MIB"), mib("CISCO-EXAMPLE-MIB")},
		{filepath.Join(root, ".git/HEAD"), "ref: refs/heads/main\n"},
		{filepath.Join(root, "vendors/9-cisco/LICENSE"), "Copyright (c) 2024 ...\n"},
		{filepath.Join(root, "vendors/9-cisco/README.md"), "# Cisco MIBs\n"},
		{filepath.Join(root, "vendors/9-cisco/SHIM.mib"), "not a mib\n"},
		{filepath.Join(root, ".hidden-mib"), mib("HIDDEN")},
	}
	for _, c := range cases {
		if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Symlink to an outside file. Skip on platforms where the
	// symlink call would fail (Windows without privilege).
	outside := filepath.Join(t.TempDir(), "OUTSIDE-MIB")
	if err := os.WriteFile(outside, []byte(mib("OUTSIDE-MIB")), 0o644); err == nil {
		_ = os.Symlink(outside, filepath.Join(root, "symlink-mib"))
	}

	files, err := walkMIBFiles(root)
	if err != nil {
		t.Fatalf("walkMIBFiles: %v", err)
	}

	wantSuffixes := []string{
		"ietf/core/SNMPv2-SMI",
		"vendors/9-cisco/CISCO-EXAMPLE-MIB",
	}
	if len(files) != len(wantSuffixes) {
		t.Errorf("walkMIBFiles returned %d files, want %d: %v", len(files), len(wantSuffixes), files)
	}
	for _, want := range wantSuffixes {
		found := false
		for _, f := range files {
			if strings.HasSuffix(filepath.ToSlash(f), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("walkMIBFiles did not return %s; got %v", want, files)
		}
	}
	// Negative assertions.
	for _, bad := range []string{".git/HEAD", "LICENSE", "README.md", "SHIM.mib", ".hidden-mib"} {
		for _, f := range files {
			if strings.HasSuffix(filepath.ToSlash(f), bad) {
				t.Errorf("walkMIBFiles unexpectedly returned %s; got %v", bad, files)
			}
		}
	}
}

// TestLoaderHasMIBOpener spot-checks the lexical gate that filters out
// LICENSE / README / partial-write garbage from the recursive walk.
func TestLoaderHasMIBOpener(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, body string
		want       bool
	}{
		{"with-marker", "FOO-MIB DEFINITIONS ::= BEGIN\nEND\n", true},
		{"with-leading-comments", "-- header\n-- more header\nFOO DEFINITIONS ::= BEGIN\n", true},
		{"no-marker", "Copyright (c) 2024 ...\nNot a MIB at all.\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name)
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := hasMIBOpener(path)
			if err != nil {
				t.Fatalf("hasMIBOpener: %v", err)
			}
			if got != c.want {
				t.Errorf("hasMIBOpener(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}
