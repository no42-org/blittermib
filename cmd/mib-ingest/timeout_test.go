/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/no42-org/blittermib/internal/mibcorpus"
)

func TestScaledCompileTimeout(t *testing.T) {
	cases := []struct {
		files int
		want  time.Duration
	}{
		{0, 5 * time.Minute},
		{1, 5 * time.Minute},
		{299, 5 * time.Minute},   // 299 s < 5 m floor
		{301, 301 * time.Second}, // past the floor, scale wins
		{29000, 29000 * time.Second},
	}
	for _, c := range cases {
		if got := scaledCompileTimeout(c.files); got != c.want {
			t.Errorf("scaledCompileTimeout(%d) = %v, want %v", c.files, got, c.want)
		}
	}
}

// signalKilledErr produces a REAL *exec.ExitError for a
// signal-terminated child — the exact shape CommandContext's kill
// leaves behind (Wait prefers it over the context error, so the
// chain carries no DeadlineExceeded; verified empirically).
func signalKilledErr(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "kill -KILL $$").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != -1 {
		t.Fatalf("fixture: want signal-terminated ExitError, got %v", err)
	}
	return err
}

func TestBudgetExhausted(t *testing.T) {
	killed := signalKilledErr(t)
	expired := context.DeadlineExceeded

	cases := []struct {
		name   string
		ctxErr error
		err    error
		want   bool
	}{
		{"nil err", expired, nil, false},
		{"plain parse failure, bound live", nil, errors.New("smidump exit 1"), false},
		{"plain parse failure, bound fired", expired, errors.New("smidump exit 1"), false},
		{"bare deadline", nil, context.DeadlineExceeded, true},
		// The real pre-start chain: DumpModule wraps exec's ctx error.
		{"wrapped deadline", nil, fmt.Errorf("smidump FOO-MIB: %w: ", context.DeadlineExceeded), true},
		{"doubly wrapped deadline", expired, fmt.Errorf("outer: %w", fmt.Errorf("smidump: %w", context.DeadlineExceeded)), true},
		// The real in-flight chain: signal-killed child, ctx expired.
		{"in-flight kill, bound fired", expired, fmt.Errorf("smidump FOO-MIB: %w: ", killed), true},
		// A signal-terminated child with a LIVE bound is a real crash.
		{"signal crash, bound live", nil, fmt.Errorf("smidump FOO-MIB: %w: ", killed), false},
		{"canceled ctx is not the bound", context.Canceled, fmt.Errorf("smidump: %w", killed), false},
	}
	for _, c := range cases {
		if got := budgetExhausted(c.ctxErr, c.err); got != c.want {
			t.Errorf("%s: budgetExhausted(%v, %v) = %v, want %v", c.name, c.ctxErr, c.err, got, c.want)
		}
	}
}

func TestCompileTimeoutFlagNegative(t *testing.T) {
	dir := t.TempDir()
	err := ingestCmd([]string{"--compile-timeout", "-5s", "--src", dir, "--root", dir, "--no-index"})
	if err == nil || !strings.Contains(err.Error(), "--compile-timeout must be >= 0") {
		t.Fatalf("negative --compile-timeout must be rejected, got %v", err)
	}
}

// TestClassifyFilesBudgetExhaustion drives a real classifyFiles run
// with an already-expired bound: every MIB-shaped file must come back
// outcomeBudgetExhausted (incomplete work), never outcomeParseError.
func TestClassifyFilesBudgetExhaustion(t *testing.T) {
	if _, err := exec.LookPath("smidump"); err != nil {
		t.Skipf("smidump not on PATH: %v", err)
	}
	dir := t.TempDir()
	mib := filepath.Join(dir, "FOO-MIB.mib")
	if err := os.WriteFile(mib, []byte("FOO-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	parsed, parseErrors := classifyFiles("smidump", "", dir, dir,
		[]string{mib}, mibcorpus.GroupMap{}, time.Nanosecond, true)
	if len(parsed) != 0 {
		t.Fatalf("nothing should parse under an expired bound, got %d", len(parsed))
	}
	if n := countBudgetExhausted(parseErrors); n != 1 {
		t.Fatalf("want 1 budget-exhausted result, got %d (results: %+v)", n, parseErrors)
	}
	for _, r := range parseErrors {
		if r.outcome == outcomeParseError {
			t.Errorf("budget exhaustion misclassified as parse error: %+v", r)
		}
	}
}

// TestClassifyFilesUnboundedZero: an explicit `0` disables the bound
// entirely — the same tiny batch compiles normally.
func TestClassifyFilesUnboundedZero(t *testing.T) {
	if _, err := exec.LookPath("smidump"); err != nil {
		t.Skipf("smidump not on PATH: %v", err)
	}
	dir := t.TempDir()
	mib := filepath.Join(dir, "FOO-MIB.mib")
	if err := os.WriteFile(mib, []byte("FOO-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	parsed, parseErrors := classifyFiles("smidump", "", dir, dir,
		[]string{mib}, mibcorpus.GroupMap{}, 0, true)
	if n := countBudgetExhausted(parseErrors); n != 0 {
		t.Fatalf("explicit 0 must disable the bound, got %d budget-exhausted", n)
	}
	// FOO-MIB is degenerate; it may parse or fail — either way it
	// must not be attributed to the budget.
	_ = parsed
}

// TestClassifyFilesInFlightKill drives the kill path: a fake smidump
// that outlives the bound gets SIGKILLed mid-run, producing the
// "signal: killed" ExitError shape (no DeadlineExceeded in the
// chain) — and must still classify as budget exhaustion.
func TestClassifyFilesInFlightKill(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "slow-smidump")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	mib := filepath.Join(dir, "FOO-MIB.mib")
	if err := os.WriteFile(mib, []byte("FOO-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	parsed, parseErrors := classifyFiles(slow, "", dir, dir,
		[]string{mib}, mibcorpus.GroupMap{}, 200*time.Millisecond, true)
	if len(parsed) != 0 {
		t.Fatalf("nothing should parse, got %d", len(parsed))
	}
	if n := countBudgetExhausted(parseErrors); n != 1 {
		t.Fatalf("in-flight kill must classify as budget exhaustion, got %d (results: %+v)", n, parseErrors)
	}
}

// TestClassifyFilesPositiveOverride: an explicit positive
// --compile-timeout is honored as-is (the scaled default is
// bypassed) and an ample bound classifies nothing as exhausted.
func TestClassifyFilesPositiveOverride(t *testing.T) {
	if _, err := exec.LookPath("smidump"); err != nil {
		t.Skipf("smidump not on PATH: %v", err)
	}
	dir := t.TempDir()
	mib := filepath.Join(dir, "FOO-MIB.mib")
	if err := os.WriteFile(mib, []byte("FOO-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, parseErrors := classifyFiles("smidump", "", dir, dir,
		[]string{mib}, mibcorpus.GroupMap{}, time.Hour, true)
	if n := countBudgetExhausted(parseErrors); n != 0 {
		t.Fatalf("ample explicit bound must not exhaust, got %d", n)
	}
}

func TestCompileTimeoutFlagTooLarge(t *testing.T) {
	dir := t.TempDir()
	err := ingestCmd([]string{"--compile-timeout", "100000h", "--src", dir, "--root", dir, "--no-index"})
	if err == nil || !strings.Contains(err.Error(), "pass 0 to disable") {
		t.Fatalf("overlarge --compile-timeout must be rejected, got %v", err)
	}
}

// TestFindBrokenAndNonMIBExcludesBudget: budget-exhausted entries
// must never surface as `broken` findings in report mode — a
// truncated report claiming N broken files would lie.
func TestFindBrokenAndNonMIBExcludesBudget(t *testing.T) {
	findings := findBrokenAndNonMIB([]result{
		{src: "a", outcome: outcomeBudgetExhausted, reason: "compile budget exhausted"},
		{src: "b", outcome: outcomeParseError, reason: "smidump rejected"},
	})
	if len(findings) != 1 {
		t.Fatalf("want 1 finding (the real parse error), got %d: %+v", len(findings), findings)
	}
	if findings[0].Category != CategoryBroken || findings[0].Sources[0] != "b" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
}

// TestReportModeBudgetExhaustion: report mode must exit via the
// actionable sentinel when the bound truncated the analysis, even
// when the report itself carries no actionable findings.
func TestReportModeBudgetExhaustion(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "slow-smidump")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "upload")
	if err := os.Mkdir(src, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "FOO-MIB.mib"), []byte("FOO-MIB DEFINITIONS ::= BEGIN\nEND\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ingestCmd([]string{"--report", "--report-format", "json",
		"--src", src, "--root", dir, "--smidump", slow,
		"--compile-timeout", "200ms"})
	if !errors.Is(err, errReportActionable) {
		t.Fatalf("truncated report must exit actionable, got %v", err)
	}
}
