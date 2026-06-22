/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/no42-org/blittermib/internal/store"
)

// newMCPServer builds a server backed by an empty in-memory store. token is
// passed to EnableMCP; ready controls whether the readiness gate is open.
// The caller is responsible for setting BLITTERMIB_MCP_ENABLED via t.Setenv
// before calling, so the env-gated fail-closed paths can be exercised.
func newMCPServer(t *testing.T, token string, ready bool) *Server {
	t.Helper()
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, ":0", "v-test", t.TempDir())
	s.EnableMCP(token)
	if ready {
		s.SetReady()
	}
	return s
}

// captureLogs redirects the default slog logger to a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func mcpReq(t *testing.T, s *Server, method, auth string, body io.Reader) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, "/mcp", body)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func TestMCPDisabledByDefault(t *testing.T) {
	// No BLITTERMIB_MCP_ENABLED set at all.
	st, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, ":0", "v-test", t.TempDir())
	s.EnableMCP("secret") // env off → no-op
	if s.MCPEnabled() {
		t.Fatal("MCP must stay disabled when BLITTERMIB_MCP_ENABLED is unset")
	}
	if got := mcpReq(t, s, http.MethodPost, "", nil).StatusCode; got != http.StatusNotFound {
		t.Errorf("/mcp status = %d, want 404 when disabled", got)
	}
}

func TestMCPFailClosed(t *testing.T) {
	t.Run("enabled without token", func(t *testing.T) {
		t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
		logs := captureLogs(t)
		s := newMCPServer(t, "", true)
		if s.MCPEnabled() {
			t.Error("MCP must fail closed with an empty token")
		}
		if got := mcpReq(t, s, http.MethodPost, "", nil).StatusCode; got != http.StatusNotFound {
			t.Errorf("/mcp status = %d, want 404", got)
		}
		if !strings.Contains(logs.String(), "stays disabled") {
			t.Error("expected a WARN that the transport stays disabled")
		}
	})

	t.Run("enabled with whitespace-only token", func(t *testing.T) {
		t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
		s := newMCPServer(t, "   \t ", true)
		if s.MCPEnabled() {
			t.Error("a whitespace-only token must count as empty → fail closed")
		}
		if got := mcpReq(t, s, http.MethodPost, "", nil).StatusCode; got != http.StatusNotFound {
			t.Errorf("/mcp status = %d, want 404", got)
		}
	})

	t.Run("token set but transport not enabled", func(t *testing.T) {
		// No BLITTERMIB_MCP_ENABLED → token present is irrelevant.
		s := newMCPServer(t, "secret", true)
		if s.MCPEnabled() {
			t.Error("MCP must stay disabled when the env flag is not truthy")
		}
		if got := mcpReq(t, s, http.MethodPost, "", nil).StatusCode; got != http.StatusNotFound {
			t.Errorf("/mcp status = %d, want 404", got)
		}
	})
}

func TestMCPAuth(t *testing.T) {
	t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
	s := newMCPServer(t, "s3cret", true)
	if !s.MCPEnabled() {
		t.Fatal("MCP should be enabled for this test")
	}

	rejected := []struct {
		name string
		auth string
	}{
		{"no header", ""},
		{"wrong token", "Bearer nope"},
		{"missing scheme", "s3cret"},
		{"empty after bearer", "Bearer "},
		{"wrong scheme", "Basic s3cret"},
	}
	for _, tc := range rejected {
		t.Run("401/"+tc.name, func(t *testing.T) {
			if got := mcpReq(t, s, http.MethodPost, tc.auth, strings.NewReader("{}")).StatusCode; got != http.StatusUnauthorized {
				t.Errorf("auth %q → status %d, want 401", tc.auth, got)
			}
		})
	}

	// A valid token (with a case-variant scheme and surrounding whitespace)
	// passes auth — downstream may 4xx on the bogus body, but never 401.
	for _, auth := range []string{"Bearer s3cret", "bearer s3cret", "BEARER   s3cret  "} {
		t.Run("admitted/"+auth, func(t *testing.T) {
			if got := mcpReq(t, s, http.MethodPost, auth, strings.NewReader("{}")).StatusCode; got == http.StatusUnauthorized {
				t.Errorf("valid token %q was rejected with 401", auth)
			}
		})
	}
}

func TestMCPReadinessPrecedence(t *testing.T) {
	t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
	s := newMCPServer(t, "s3cret", false) // NOT ready

	// Authenticated but pre-ready → 503.
	if got := mcpReq(t, s, http.MethodPost, "Bearer s3cret", strings.NewReader("{}")).StatusCode; got != http.StatusServiceUnavailable {
		t.Errorf("authenticated pre-ready status = %d, want 503", got)
	}
	// Unauthenticated pre-ready → 401, NOT 503 (auth precedes readiness; no
	// load-state leak to an unauthenticated caller).
	if got := mcpReq(t, s, http.MethodPost, "", strings.NewReader("{}")).StatusCode; got != http.StatusUnauthorized {
		t.Errorf("unauthenticated pre-ready status = %d, want 401", got)
	}
}

func TestMCPMethodGuard(t *testing.T) {
	t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
	s := newMCPServer(t, "s3cret", true)
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		if got := mcpReq(t, s, m, "Bearer s3cret", nil).StatusCode; got != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp status = %d, want 405", m, got)
		}
	}
}

// TestMCPBodyLimitCaps exercises the body guard directly: a read past the
// ceiling must error, regardless of the MCP SDK's own content-type/parse
// checks. Testing the middleware in isolation is what actually proves the
// guard fires — at the /mcp level an oversized body and a parse failure both
// surface as 400, so a status-only assertion can't distinguish them.
func TestMCPBodyLimitCaps(t *testing.T) {
	var readErr error
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("a", mcpMaxBodyBytes+1)))
	mcpBodyLimit(inner).ServeHTTP(httptest.NewRecorder(), req)
	if readErr == nil {
		t.Error("reading a body past mcpMaxBodyBytes must error (MaxBytesReader), got nil")
	}
}

// TestMCPOversizedBodyRejected is the /mcp-level smoke: a well-formed
// (correct content-type) but over-ceiling POST is refused with a 4xx before
// any tool runs. The body guard's correctness is proven by TestMCPBodyLimitCaps;
// here we just confirm the route wires it in and does not 200.
func TestMCPOversizedBodyRejected(t *testing.T) {
	t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
	s := newMCPServer(t, "s3cret", true)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("a", mcpMaxBodyBytes+1)))
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if got := rec.Result().StatusCode; got != http.StatusBadRequest {
		t.Errorf("over-ceiling body status = %d, want 400 (rejected before dispatch)", got)
	}
}

func TestMCPWebRoutesUnaffected(t *testing.T) {
	t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
	s := newMCPServer(t, "s3cret", true)
	// The bearer requirement applies to /mcp only — other routes ignore it.
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if got := rec.Result().StatusCode; got != http.StatusOK {
		t.Errorf("/readyz without a token status = %d, want 200 (unaffected by MCP auth)", got)
	}
}

func TestMCPTokenNeverLogged(t *testing.T) {
	t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
	logs := captureLogs(t)
	s := newMCPServer(t, "topsecret", true)
	// Exercise both an admitted and a rejected request so logging runs.
	_ = mcpReq(t, s, http.MethodPost, "Bearer topsecret", strings.NewReader("{}"))
	_ = mcpReq(t, s, http.MethodPost, "Bearer wrong", strings.NewReader("{}"))
	if strings.Contains(logs.String(), "topsecret") {
		t.Error("the bearer token must never appear in logs")
	}
	if strings.Contains(logs.String(), "Authorization") {
		t.Error("the Authorization header must never be logged")
	}
}

// TestMCPHandshakeOverHTTP is the end-to-end check: a real MCP client speaks
// Streamable HTTP to the /mcp route through an httptest server, authenticating
// with the bearer token, and sees exactly the five read-only tools.
func TestMCPHandshakeOverHTTP(t *testing.T) {
	t.Setenv("BLITTERMIB_MCP_ENABLED", "true")
	s := newMCPServer(t, "s3cret", true)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	transport := &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerRT{token: "s3cret", base: http.DefaultTransport}},
		DisableStandaloneSSE: true, // stateless JSON server: no GET SSE stream
	}

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	want := []string{"search_mibs", "lookup_oid", "lookup_symbol", "decode_walk", "classify_notification"}
	if len(res.Tools) != len(want) {
		t.Errorf("advertised %d tools, want %d: %v", len(res.Tools), len(want), got)
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing advertised tool %q", name)
		}
	}
}

// bearerRT adds the Authorization header to every request, standing in for a
// client configured with the network MCP token.
type bearerRT struct {
	token string
	base  http.RoundTripper
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}
