/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/no42-org/blittermib/internal/mcptools"
)

const (
	// mcpMaxBodyBytes is a coarse pre-parse guard on a single /mcp request
	// body: it stops a multi-hundred-MB POST before the SDK buffers and
	// unmarshals it. It is sized to clear a realistic decode_walk envelope
	// (a ~10 MB ASCII walk plus modest JSON-string overhead). It is NOT the
	// authoritative per-tool limit — decode_walk enforces its own 10 MB cap
	// on the decoded text after parse, which is what bounds legitimate input.
	mcpMaxBodyBytes = 16 << 20 // 16 MB

	// mcpRequestTimeout bounds a single /mcp tool invocation. The store
	// pins MaxOpenConns(1), and database/sql queues waiters on that one
	// connection with no built-in ceiling; this deadline converts an
	// unbounded pile-up into a fast-failing timeout under burst load. It
	// sits below the http.Server WriteTimeout (30 s) so the application
	// deadline wins and emits a clean timeout rather than the server write
	// deadline severing the connection mid-response.
	mcpRequestTimeout = 20 * time.Second
)

// EnableMCP wires the network MCP transport at /mcp when
// BLITTERMIB_MCP_ENABLED parses as truthy AND a usable token is provided.
// It mirrors EnableUploads/EnableWalk: off by default, and fails closed —
// if the env var is truthy but the token is empty (or whitespace-only) the
// route stays unregistered and a WARN is logged, so a misconfigured
// deployment never exposes an unauthenticated or trivially-guessable
// endpoint. token is passed in by main (read from BLITTERMIB_MCP_TOKEN).
func (s *Server) EnableMCP(token string) {
	if !mcpEnvEnabled() {
		return
	}
	token = strings.TrimSpace(token)
	if token == "" {
		slog.Warn("BLITTERMIB_MCP_ENABLED is true but BLITTERMIB_MCP_TOKEN is empty; network MCP transport stays disabled")
		return
	}
	s.mcpEnabled = true
	s.mcpToken = token
	s.routesMCP()
}

// MCPEnabled reports whether the network MCP transport is live.
func (s *Server) MCPEnabled() bool { return s.mcpEnabled }

// mcpEnvEnabled parses BLITTERMIB_MCP_ENABLED with the same permissive
// semantics as uploadEnvEnabled/walkEnvEnabled. Empty or unparseable
// leaves the transport disabled.
func mcpEnvEnabled() bool {
	v := os.Getenv("BLITTERMIB_MCP_ENABLED")
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

// routesMCP registers the /mcp route. Called only from EnableMCP when the
// transport is enabled; left unregistered otherwise so /mcp 404s via the
// catch-all — symmetric with routesUpload/routesWalk.
//
// The handler is built in stateless mode with JSON responses (not SSE
// streaming, which would collide with the http.Server WriteTimeout and
// open unbounded GET streams). The middleware chain runs, outermost first:
//
//	withLogging → withRecover → auth → method-guard → readiness → body-limit → deadline → MCP
//
// Auth precedes readiness so an unauthenticated caller is rejected with 401
// before any 503 load-state is observable. The method guard precedes
// readiness so GET/DELETE always 405, regardless of load state.
func (s *Server) routesMCP() {
	// Build the MCP server once and share it across requests. In stateless
	// mode the handler creates a fresh per-request transport/session bound to
	// this server, so there is no per-request server state to isolate — and
	// reusing one instance avoids re-reflecting the five tool schemas on every
	// call. The underlying store is read-only and shared safely.
	mcpSrv := mcptools.NewServer(s.store, s.version)
	streamHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpSrv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)

	var h http.Handler = streamHandler
	h = mcpDeadline(h)
	h = mcpBodyLimit(h)
	h = s.mcpReadiness(h)
	h = mcpMethodGuard(h)
	h = s.mcpAuth(h)

	s.mux.Handle("/mcp", chain(h, withLogging, withRecover))
}

// mcpAuth checks the bearer token before anything downstream. The Bearer
// scheme is matched case-insensitively (RFC 6750), surrounding whitespace
// on the credential is trimmed, and only the first Authorization header is
// honored (http.Header.Get). A missing, malformed, or empty credential —
// and any mismatch (compared constant-time) — yields 401. The token is
// never written to the response or logs.
func (s *Server) mcpAuth(next http.Handler) http.Handler {
	want := []byte(s.mcpToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, cred, found := strings.Cut(r.Header.Get("Authorization"), " ")
		if !found || !strings.EqualFold(scheme, "bearer") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		got := strings.TrimSpace(cred)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mcpMethodGuard rejects the streaming-only verbs. In stateless
// JSON-response mode there is no standalone SSE stream (GET) or session to
// terminate (DELETE), so both 405.
func mcpMethodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mcpReadiness returns 503 until the initial corpus load has completed,
// matching the web UI's readiness behavior. It runs after auth so the load
// state is never disclosed to an unauthenticated caller.
func (s *Server) mcpReadiness(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":  "loading",
				"version": s.version,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mcpBodyLimit caps the request body so an oversized POST is refused before
// the SDK reads and unmarshals it.
func mcpBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, mcpMaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// mcpDeadline runs each request under a bounded context so a burst of
// concurrent clients fails fast on the single-connection store rather than
// stalling indefinitely on the pool wait.
func mcpDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), mcpRequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
