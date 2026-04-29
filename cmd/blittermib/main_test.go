package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseFlags_Defaults(t *testing.T) {
	cfg, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.mibsDir != "./mibs" || cfg.dataDir != "./data" || cfg.listen != ":8080" {
		t.Errorf("defaults wrong: %+v", cfg)
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
