package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/mibimport"
)

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
