/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// fetchWith performs a GET with the given headers and returns status,
// response headers, and body.
func fetchWith(t *testing.T, url string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

// TestWorkspacePartialCaseA: an htmx request with an unchanged scope
// (selection-only change) gets the detail section only — no list, no
// tree, no document shell.
func TestWorkspacePartialCaseA(t *testing.T) {
	ts := newTestServer(t)
	code, _, body := fetchWith(t,
		ts.URL+"/m/IF-MIB/1.3.6.1.2.1.2.2?sel=1.3.6.1.2.1.2.2.1",
		map[string]string{
			"HX-Request":     "true",
			"HX-Current-URL": ts.URL + "/m/IF-MIB/1.3.6.1.2.1.2.2",
		})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, `id="workspace-detail"`) {
		t.Errorf("case A missing detail section:\n%s", body)
	}
	if strings.Contains(body, `id="workspace-list"`) {
		t.Errorf("case A must not include the list section")
	}
	if strings.Contains(body, `id="workspace-tree"`) || strings.Contains(body, "<html") {
		t.Errorf("case A must not include tree or document shell")
	}
}

// TestWorkspacePartialCaseB: an htmx request that changes scope gets
// the detail section plus the list section as an out-of-band swap.
func TestWorkspacePartialCaseB(t *testing.T) {
	ts := newTestServer(t)
	code, _, body := fetchWith(t,
		ts.URL+"/m/IF-MIB/1.3.6.1.2.1.2.2.1",
		map[string]string{
			"HX-Request":     "true",
			"HX-Current-URL": ts.URL + "/m/IF-MIB/1.3.6.1.2.1.2.2",
		})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, `id="workspace-detail"`) {
		t.Errorf("case B missing detail section")
	}
	if !strings.Contains(body, `id="workspace-list"`) ||
		!strings.Contains(body, `hx-swap-oob="outerHTML"`) {
		t.Errorf("case B must include the OOB list section:\n%.400s", body)
	}
	if strings.Contains(body, `id="workspace-tree"`) || strings.Contains(body, "<html") {
		t.Errorf("case B must not include tree or document shell")
	}
}

// TestWorkspacePartialFallbacks: non-htmx requests, history restores,
// and unparseable HX-Current-URL values keep safe behavior.
func TestWorkspacePartialFallbacks(t *testing.T) {
	ts := newTestServer(t)

	// Plain request → full document.
	code, _, body := fetchWith(t, ts.URL+"/m/IF-MIB/1.3.6.1.2.1.2.2", nil)
	if code != http.StatusOK || !strings.Contains(body, "<html") ||
		!strings.Contains(body, `id="workspace-tree"`) {
		t.Errorf("plain request must render the full page")
	}

	// History restore → full document (htmx cache misses need one).
	code, _, body = fetchWith(t, ts.URL+"/m/IF-MIB/1.3.6.1.2.1.2.2",
		map[string]string{
			"HX-Request":                 "true",
			"HX-History-Restore-Request": "true",
			"HX-Current-URL":             ts.URL + "/m/IF-MIB",
		})
	if code != http.StatusOK || !strings.Contains(body, "<html") {
		t.Errorf("history restore must render the full page")
	}

	// Missing HX-Current-URL → case B (the safe superset), not a full page.
	code, _, body = fetchWith(t, ts.URL+"/m/IF-MIB/1.3.6.1.2.1.2.2",
		map[string]string{"HX-Request": "true"})
	if code != http.StatusOK || strings.Contains(body, "<html") ||
		!strings.Contains(body, `id="workspace-list"`) {
		t.Errorf("missing HX-Current-URL must yield the partial superset")
	}
}

// TestWorkspacePartialModuleMismatch: a cross-module htmx request is
// refused with HX-Refresh so the client performs a full reload.
func TestWorkspacePartialModuleMismatch(t *testing.T) {
	ts := newTestServer(t)
	code, hdr, _ := fetchWith(t, ts.URL+"/m/IF-MIB/1.3.6.1.2.1.2.2",
		map[string]string{
			"HX-Request":     "true",
			"HX-Current-URL": ts.URL + "/m/OTHER-MIB/1.3.6.1.4.1",
		})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if hdr.Get("HX-Refresh") != "true" {
		t.Errorf("module mismatch must set HX-Refresh: true")
	}
}
