package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/mibimport"
)

// TestServeBeforeCorpusLoad pins the boot-order invariant: the
// listener accepts connections (and /healthz answers 200) while the
// corpus load is still in flight, /readyz reports 503 "loading" during
// that window, and the gate opens once the load completes. The
// testHookBeforeCorpusLoad seam holds the loader open deterministically
// — the in-flight window is real, not a timing guess.
func TestServeBeforeCorpusLoad(t *testing.T) {
	// Pick a free port (run() does not expose the bound address).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	gate := make(chan struct{})
	var release sync.Once
	releaseGate := func() { release.Do(func() { close(gate) }) }
	t.Cleanup(releaseGate) // never leave run() blocked on the hook
	testHookBeforeCorpusLoad = func() { <-gate }
	t.Cleanup(func() { testHookBeforeCorpusLoad = nil })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := config{
		dataDir:     t.TempDir(),
		mibsDir:     t.TempDir(),
		mibsSet:     true,
		standardDir: filepath.Join(t.TempDir(), "missing"), // no mirror
		listen:      addr,
	}
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg) }()

	base := "http://" + addr
	get := func(path string) (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		return http.DefaultClient.Do(req)
	}
	waitFor := func(path string, want int, what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := get(path)
			if err == nil {
				code := resp.StatusCode
				_ = resp.Body.Close()
				if code == want {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s: got %d, want %d before deadline", what, code, want)
				}
			} else if time.Now().After(deadline) {
				t.Fatalf("%s: %v", what, err)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	// Liveness answers while the load is held open by the hook.
	waitFor("/healthz", http.StatusOK, "liveness during corpus load")

	// Readiness is closed for the whole in-flight window.
	resp, err := get("/readyz")
	if err != nil {
		t.Fatalf("/readyz during load: %v", err)
	}
	bodyBytes := make([]byte, 256)
	n, _ := resp.Body.Read(bodyBytes)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz during load = %d, want 503", resp.StatusCode)
	}
	if !strings.Contains(string(bodyBytes[:n]), "loading") {
		t.Errorf("/readyz body = %q, want status loading", string(bodyBytes[:n]))
	}

	// Release the loader; the gate must open.
	releaseGate()
	waitFor("/readyz", http.StatusOK, "readiness after corpus load")

	// Graceful stop via the parent context.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(35 * time.Second): // shutdown drain is 30s
		t.Fatal("run did not return after context cancel")
	}
}

func TestParseFlags_Defaults(t *testing.T) {
	cfg, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// -mibs defaults to EMPTY at parse time (resolved to <data>/mibs
	// in run() — the standard-mibs-image relocation); mibsSet tracks
	// explicit use for the override path.
	if cfg.mibsDir != "" || cfg.mibsSet || cfg.dataDir != "./data" || cfg.listen != ":8080" {
		t.Errorf("defaults wrong: %+v", cfg)
	}
	if cfg.standardDir != "/usr/share/blittermib/mibs" {
		t.Errorf("standardDir default wrong: %q", cfg.standardDir)
	}
	if cfg.verbose {
		t.Error("verbose should default to false")
	}
}

func TestParseFlags_Overrides(t *testing.T) {
	cfg, err := parseFlags(
		[]string{"-mibs", "/etc/mibs", "-listen", ":9000", "-v"},
		&bytes.Buffer{},
	)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.mibsDir != "/etc/mibs" {
		t.Errorf("mibs = %q", cfg.mibsDir)
	}
	if cfg.listen != ":9000" {
		t.Errorf("listen = %q", cfg.listen)
	}
	if !cfg.verbose {
		t.Error("verbose should be true")
	}
}

func TestParseFlags_VersionSentinel(t *testing.T) {
	_, err := parseFlags([]string{"-version"}, &bytes.Buffer{})
	if !errors.Is(err, errPrintVersion) {
		t.Errorf("err = %v, want errPrintVersion", err)
	}
}

// A non-writable intake dir disables imports but MUST NOT suppress the
// standard-corpus mirror — otherwise a deployment with an unwritable
// import/ mount would serve an empty browser. Regression guard for the
// SyncStandard/importOK decoupling.
func TestBootstrapStandardSyncsWhenIntakeUnwritable(t *testing.T) {
	root := t.TempDir()
	std := t.TempDir()

	// Minimal read-only standard set to mirror.
	mibPath := filepath.Join(std, "ietf", "core", "SNMPv2-SMI")
	if err := os.MkdirAll(filepath.Dir(mibPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mibPath, []byte("-- standard mib --"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Block intake: plant a regular file where EnsureDirs needs import/
	// to be a directory, so MkdirAll fails while the corpus root stays
	// writable.
	if err := os.WriteFile(filepath.Join(root, "import"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := mibimport.New(root, nil, mibcorpus.GroupMap{})
	if importOK := bootstrapImportAndStandard(engine, std); importOK {
		t.Fatal("importOK = true; want false when the intake dir is blocked")
	}

	if _, err := os.Stat(filepath.Join(root, "ietf", "core", "SNMPv2-SMI")); err != nil {
		t.Fatalf("standard corpus not mirrored despite unwritable intake: %v", err)
	}
}

func TestParseFlags_BadFlagReturnsError(t *testing.T) {
	var out bytes.Buffer
	_, err := parseFlags([]string{"-not-a-flag"}, &out)
	if err == nil {
		t.Error("expected error for unknown flag")
	}
	if !strings.Contains(out.String(), "not-a-flag") {
		t.Errorf("usage not written to errOut: %q", out.String())
	}
}
