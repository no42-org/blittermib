/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRenderReportJSON(t *testing.T) {
	t.Run("empty findings emit []", func(t *testing.T) {
		var buf bytes.Buffer
		if err := renderReportJSON(&buf, nil); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(buf.String()); got != "[]" {
			t.Errorf("empty findings = %q, want '[]'", got)
		}
	})

	t.Run("flat array of objects", func(t *testing.T) {
		findings := []Finding{
			{
				Category:   CategoryByteIdentical,
				Severity:   SeverityInfo,
				ModuleName: "",
				Sources:    []string{"a", "b"},
				Detail:     map[string]any{"hash": "sha-1"},
			},
		}
		var buf bytes.Buffer
		if err := renderReportJSON(&buf, findings); err != nil {
			t.Fatal(err)
		}
		var decoded []map[string]any
		if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
			t.Fatalf("output is not a JSON array of objects: %v\n%s", err, buf.String())
		}
		if len(decoded) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(decoded))
		}
		if decoded[0]["category"] != CategoryByteIdentical {
			t.Errorf("category = %v", decoded[0]["category"])
		}
	})

	t.Run("indented two spaces", func(t *testing.T) {
		var buf bytes.Buffer
		_ = renderReportJSON(&buf, []Finding{{
			Category: CategoryNonMIB,
			Severity: SeverityInfo,
			Sources:  []string{"x"},
			Detail:   map[string]any{},
		}})
		// MarshalIndent("", "  ") produces lines indented with
		// multiples of two spaces; verify presence of "    " (four
		// spaces) inside the array element (one element × two-space
		// indent × two levels).
		if !strings.Contains(buf.String(), "    \"category\":") {
			t.Errorf("expected two-space indent for nested fields; got:\n%s", buf.String())
		}
	})
}

func TestRenderReportText(t *testing.T) {
	t.Run("empty findings print 'no findings'", func(t *testing.T) {
		var buf bytes.Buffer
		if err := renderReportText(&buf, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "no findings") {
			t.Errorf("expected 'no findings' line; got %q", buf.String())
		}
	})

	t.Run("section per category with count header", func(t *testing.T) {
		findings := []Finding{
			{
				Category: CategoryByteIdentical,
				Severity: SeverityInfo,
				Sources:  []string{"a", "b"},
				Detail:   map[string]any{"hash": "sha-1", "size": int64(1024), "cross_directory": true},
			},
			{
				Category:   CategoryModuleNameCollision,
				Severity:   SeverityWarn,
				ModuleName: "CISCO-FOO-MIB",
				Sources:    []string{"a", "b"},
				Detail: map[string]any{
					"candidates": []map[string]any{
						{"source": "a", "last_updated_normalised": "201803191200Z", "sha": "sha-1"},
						{"source": "b", "last_updated_normalised": "202205101200Z", "sha": "sha-2"},
					},
				},
			},
		}
		var buf bytes.Buffer
		if err := renderReportText(&buf, findings); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "# byte-identical (1)") {
			t.Errorf("missing byte-identical section header; got:\n%s", out)
		}
		if !strings.Contains(out, "# module-name-collision (1)") {
			t.Errorf("missing module-name-collision section header; got:\n%s", out)
		}
		if !strings.Contains(out, "CISCO-FOO-MIB") {
			t.Errorf("module name not rendered; got:\n%s", out)
		}
	})

	t.Run("truncation at cap with spec'd trailer", func(t *testing.T) {
		// Build 73 byte-identical findings — spec scenario exactly.
		findings := make([]Finding, 73)
		for i := range findings {
			findings[i] = Finding{
				Category: CategoryByteIdentical,
				Severity: SeverityInfo,
				Sources:  []string{fmt.Sprintf("path-%03d-a", i), fmt.Sprintf("path-%03d-b", i)},
				Detail: map[string]any{
					"hash":            fmt.Sprintf("sha-%03d", i),
					"size":            int64(1024),
					"cross_directory": false,
				},
			}
		}
		var buf bytes.Buffer
		if err := renderReportText(&buf, findings); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		// Header counts ALL findings, not just emitted.
		if !strings.Contains(out, "# byte-identical (73)") {
			t.Errorf("header count should reflect total (73); got:\n%s", out)
		}
		// Trailer with exact spec wording.
		wantTrailer := "... and 23 more (use --report-format=json for the full list)"
		if !strings.Contains(out, wantTrailer) {
			t.Errorf("expected trailer %q; got:\n%s", wantTrailer, out)
		}
		// First 50 sha values present; sha-050 onwards absent.
		if !strings.Contains(out, "sha-049") {
			t.Errorf("expected sha-049 in truncated set; got:\n%s", out)
		}
		if strings.Contains(out, "sha-050") {
			t.Errorf("sha-050 should be truncated; got:\n%s", out)
		}
	})

	t.Run("truncation boundary at cap+1 → 'and 1 more'", func(t *testing.T) {
		// Off-by-one regression guard: with exactly 51 findings,
		// 50 should emit + the trailer must say "and 1 more".
		findings := make([]Finding, 51)
		for i := range findings {
			findings[i] = Finding{
				Category: CategoryByteIdentical,
				Severity: SeverityInfo,
				Sources:  []string{fmt.Sprintf("a-%03d", i), fmt.Sprintf("b-%03d", i)},
				Detail: map[string]any{
					"hash": fmt.Sprintf("sha-%03d", i), "size": int64(1), "cross_directory": false,
				},
			}
		}
		var buf bytes.Buffer
		if err := renderReportText(&buf, findings); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		if !strings.Contains(out, "# byte-identical (51)") {
			t.Errorf("header count should reflect total (51); got:\n%s", out)
		}
		wantTrailer := "... and 1 more (use --report-format=json for the full list)"
		if !strings.Contains(out, wantTrailer) {
			t.Errorf("expected boundary trailer %q; got:\n%s", wantTrailer, out)
		}
		if !strings.Contains(out, "sha-049") {
			t.Errorf("expected last-in-cap (sha-049) present; got:\n%s", out)
		}
		if strings.Contains(out, "sha-050") {
			t.Errorf("first-over-cap (sha-050) should be truncated; got:\n%s", out)
		}
	})

	t.Run("exactly cap (50) emits no trailer", func(t *testing.T) {
		// Boundary on the other side: 50 findings exactly → all
		// emitted, no trailer line.
		findings := make([]Finding, 50)
		for i := range findings {
			findings[i] = Finding{
				Category: CategoryByteIdentical,
				Severity: SeverityInfo,
				Sources:  []string{fmt.Sprintf("a-%03d", i)},
				Detail: map[string]any{
					"hash": fmt.Sprintf("sha-%03d", i), "size": int64(1), "cross_directory": false,
				},
			}
		}
		var buf bytes.Buffer
		_ = renderReportText(&buf, findings)
		if strings.Contains(buf.String(), "... and") {
			t.Errorf("at-cap should NOT emit trailer; got:\n%s", buf.String())
		}
	})

	t.Run("under-cap categories untouched", func(t *testing.T) {
		// 30 byte-identical + 2 module-name-collision — neither
		// section should be truncated.
		findings := make([]Finding, 0, 32)
		for i := 0; i < 30; i++ {
			findings = append(findings, Finding{
				Category: CategoryByteIdentical,
				Severity: SeverityInfo,
				Sources:  []string{fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)},
				Detail:   map[string]any{"hash": fmt.Sprintf("sha-%d", i), "size": int64(1), "cross_directory": false},
			})
		}
		for i := 0; i < 2; i++ {
			findings = append(findings, Finding{
				Category:   CategoryModuleNameCollision,
				Severity:   SeverityWarn,
				ModuleName: fmt.Sprintf("MOD-%d", i),
				Sources:    []string{"x", "y"},
				Detail:     map[string]any{"candidates": []map[string]any{}},
			})
		}
		var buf bytes.Buffer
		_ = renderReportText(&buf, findings)
		if strings.Contains(buf.String(), "... and") {
			t.Errorf("no section should be truncated; got:\n%s", buf.String())
		}
	})
}

func TestRenderReportExitCodePolicy(t *testing.T) {
	// renderReport returns errReportActionable when any finding has
	// severity warn or error; otherwise nil. The sentinel must
	// surface from BOTH formats — a regression that returns it from
	// only one path would let `--report-format=text` exit 0 on
	// warnings.
	cases := []struct {
		name      string
		findings  []Finding
		wantError bool
	}{
		{"empty findings → clean exit", nil, false},
		{"info-only → clean exit", []Finding{
			{Category: CategoryByteIdentical, Severity: SeverityInfo, Sources: []string{"a"}, Detail: map[string]any{}},
			{Category: CategoryNonMIB, Severity: SeverityInfo, Sources: []string{"b"}, Detail: map[string]any{"reason": "x"}},
		}, false},
		{"any warn → actionable", []Finding{
			{Category: CategoryByteIdentical, Severity: SeverityInfo, Sources: []string{"a"}, Detail: map[string]any{}},
			{Category: CategoryModuleNameCollision, Severity: SeverityWarn, Sources: []string{"b"}, Detail: map[string]any{}},
		}, true},
		{"any error → actionable", []Finding{
			{Category: CategoryDivergentIdentity, Severity: SeverityError, Sources: []string{"a"}, Detail: map[string]any{}},
		}, true},
	}
	for _, format := range []string{"text", "json"} {
		for _, tc := range cases {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				var buf bytes.Buffer
				err := renderReport(&buf, format, tc.findings)
				gotErr := errors.Is(err, errReportActionable)
				if gotErr != tc.wantError {
					t.Errorf("renderReport(format=%s) actionable=%v, want %v (err=%v)",
						format, gotErr, tc.wantError, err)
				}
			})
		}
	}
}

func TestRenderReportUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	err := renderReport(&buf, "yaml", nil)
	if err == nil {
		t.Errorf("expected error for unknown format, got nil")
	}
	if errors.Is(err, errReportActionable) {
		t.Errorf("unknown-format error must not match errReportActionable sentinel")
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("error should mention the bad format; got %v", err)
	}
}

func TestHasActionableFinding(t *testing.T) {
	if hasActionableFinding(nil) {
		t.Errorf("nil findings should not be actionable")
	}
	if hasActionableFinding([]Finding{{Severity: SeverityInfo}}) {
		t.Errorf("only-info findings should not be actionable")
	}
	if !hasActionableFinding([]Finding{{Severity: SeverityWarn}}) {
		t.Errorf("warn findings should be actionable")
	}
	if !hasActionableFinding([]Finding{{Severity: SeverityError}}) {
		t.Errorf("error findings should be actionable")
	}
	if !hasActionableFinding([]Finding{
		{Severity: SeverityInfo},
		{Severity: SeverityError},
	}) {
		t.Errorf("any actionable finding in slice should win")
	}
}
