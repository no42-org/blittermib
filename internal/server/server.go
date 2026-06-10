package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/no42-org/blittermib/internal/mibimport"
	"github.com/no42-org/blittermib/internal/store"
)

// Server is the blittermib HTTP server.
type Server struct {
	store   *store.Store
	version string
	mibsDir string

	// Upload surface — wired by EnableUploads when
	// BLITTERMIB_UPLOAD_ENABLED is true. Both fields stay nil/false
	// in the default boring configuration. The importer is the
	// single intake pipeline (mib-import-pipeline): uploads land in
	// import/ and traverse the same engine as filesystem drops.
	uploadsEnabled bool
	importer       *mibimport.Engine

	// Walk-decoder surface — wired by EnableWalk when
	// BLITTERMIB_WALK_DECODER_ENABLED is true. Off by default. When
	// false the /walk routes stay unregistered and the walk-overlay
	// client asset is omitted from rendered pages. The decoder holds no
	// engine or disk state (walks are parsed in memory), so there is
	// nothing to fail closed on.
	walkEnabled bool

	// ready is the readiness gate: false until the initial corpus load
	// (SyncCorpus + boot rescan) completes. Binary and one-way — opened
	// once by SetReady, never re-closed. A transient store error after
	// the gate opens surfaces as a 503 from /readyz's per-request store
	// check instead of re-latching the gate.
	ready atomic.Bool

	mux  *http.ServeMux
	http *http.Server
}

// SetReady opens the readiness gate. Called once from the boot path
// when the initial corpus load finishes; /readyz reports 503 "loading"
// until then.
func (s *Server) SetReady() { s.ready.Store(true) }

// Ready reports whether the initial corpus load has completed.
func (s *Server) Ready() bool { return s.ready.Load() }

// New constructs a Server bound to addr backed by the given store.
// mibsDir is the corpus root — shown to the user on the empty-state
// landing page so they know where to drop MIB files, and used as the
// allowed root for the module-download path-traversal guard. version
// is surfaced at /version and in the /healthz body.
//
// New does NOT wire the upload surface. Call EnableUploads after
// construction (or don't; the default is uploads-off).
func New(st *store.Store, addr, version, mibsDir string) *Server {
	mux := http.NewServeMux()
	s := &Server{
		store:   st,
		version: version,
		mibsDir: mibsDir,
		mux:     mux,
		http: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadTimeout:       15 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
	}
	s.routes()
	return s
}

// EnableUploads wires the upload + delete + management routes when
// BLITTERMIB_UPLOAD_ENABLED parses as truthy via strconv.ParseBool.
// Otherwise this is a no-op — the upload routes never get registered
// so they 404 via the catch-all, and the conditional UI fragments
// keyed off s.UploadsEnabled() stay absent from rendered HTML.
//
// The import engine is invoked inline by the upload handler so the
// response carries per-file imported/failed/duplicate outcomes.
// Passing a nil engine while the env var is truthy is a configuration
// error; uploads are still disabled in that case so a misconfigured
// deployment fails closed rather than open. The mismatch is logged
// at WARN so an operator who set the env var but wired no callback
// gets a signal in the log instead of silent 404s.
func (s *Server) EnableUploads(importer *mibimport.Engine) {
	envOn := uploadEnvEnabled()
	if !envOn {
		return
	}
	if importer == nil {
		slog.Warn("BLITTERMIB_UPLOAD_ENABLED is true but no import engine was wired; uploads stay disabled")
		return
	}
	s.uploadsEnabled = true
	s.importer = importer
	s.routesUpload()
}

// UploadsEnabled reports whether the upload surface is live, for
// templates that conditionally render drop-zone / inline-delete
// affordances.
func (s *Server) UploadsEnabled() bool { return s.uploadsEnabled }

// uploadEnvEnabled parses BLITTERMIB_UPLOAD_ENABLED. Permissive —
// strconv.ParseBool accepts 1, t, T, TRUE, true, True (and the
// matching falsy spellings). Any unparseable value, or an empty
// env var, leaves uploads disabled.
func uploadEnvEnabled() bool {
	v := os.Getenv("BLITTERMIB_UPLOAD_ENABLED")
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

// EnableWalk wires the SNMP walk-decoder routes (/walk, /walk/decode,
// /walk/bundle) when BLITTERMIB_WALK_DECODER_ENABLED parses as truthy
// via strconv.ParseBool. Otherwise this is a no-op — the routes stay
// unregistered so they 404 via the catch-all, and the walk-overlay
// client asset (gated off WalkEnabled in the base layout) stays out
// of rendered HTML.
//
// Unlike EnableUploads there is no engine to wire: walks are parsed in
// memory and never touch disk, so the env var is the only gate.
func (s *Server) EnableWalk() {
	if !walkEnvEnabled() {
		return
	}
	s.walkEnabled = true
	s.routesWalk()
}

// WalkEnabled reports whether the walk-decoder surface is live, for
// main to mirror into the web layer so the base layout includes or
// omits walk-overlay.js.
func (s *Server) WalkEnabled() bool { return s.walkEnabled }

// walkEnvEnabled parses BLITTERMIB_WALK_DECODER_ENABLED with the same
// permissive semantics as uploadEnvEnabled. Empty or unparseable
// leaves the decoder disabled.
func walkEnvEnabled() bool {
	v := os.Getenv("BLITTERMIB_WALK_DECODER_ENABLED")
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

// Start runs the HTTP server until ctx is canceled, then performs a
// graceful shutdown bounded by a 30-second drain window.
//
// Returns nil on a clean shutdown, or any non-ErrServerClosed listen
// error from the underlying http.Server.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", s.http.Addr)
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-errCh:
		return err
	}
}

func (s *Server) shutdown() error {
	slog.Info("http server shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

// Handler exposes the underlying multiplexer (for httptest).
func (s *Server) Handler() http.Handler { return s.mux }
