package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/web"
)

// LoadOutcome is a per-file result of a synchronous compile + store
// pass triggered by the upload handler. The handler folds these
// into the JSON response so the operator sees parse status without a
// follow-up request (per design.md D3). cmd/blittermib's loader is
// responsible for populating these from compile.Result; the server
// package never imports compile directly.
type LoadOutcome struct {
	Path        string
	Module      *model.Module // nil when Err != nil
	SymbolCount int
	Diagnostics []model.Diagnostic
	Err         error
}

// LoadFunc compiles + ingests one or more MIB files into the store
// and returns one outcome per input path (in any order). Wired by
// the parent (cmd/blittermib) via EnableUploads so the upload handler
// can compile inline without the internal/server package depending
// on the loader implementation.
type LoadFunc func(ctx context.Context, paths []string) []LoadOutcome

// Server is the blittermib HTTP server.
type Server struct {
	store   *store.Store
	version string
	mibsDir string

	// Upload surface — wired by EnableUploads when
	// BLITTERMIB_UPLOAD_ENABLED is true. Both fields stay nil/false
	// in the default boring configuration.
	uploadsEnabled bool
	uploadDir      string
	loadFiles      LoadFunc

	mux  *http.ServeMux
	http *http.Server
}

// New constructs a Server bound to addr backed by the given store.
// mibsDir is the corpus root — shown to the user on the empty-state
// landing page so they know where to drop MIB files, and used as the
// allowed root for the module-download path-traversal guard. version
// is surfaced at /version and in the /healthz body.
//
// New does NOT wire the upload surface. Call EnableUploads after
// construction (or don't; the default is uploads-off).
func New(st *store.Store, addr, version, mibsDir string) *Server {
	web.Version = version
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
// The load callback is invoked inline by the upload handler to
// compile newly-written files synchronously (per D3). Passing a
// nil load function while the env var is truthy is a configuration
// error; uploads are still disabled in that case so a misconfigured
// deployment fails closed rather than open. The mismatch is logged
// at WARN so an operator who set the env var but wired no callback
// gets a signal in the log instead of silent 404s.
func (s *Server) EnableUploads(loadFiles LoadFunc) {
	envOn := uploadEnvEnabled()
	if !envOn {
		return
	}
	if loadFiles == nil {
		slog.Warn("BLITTERMIB_UPLOAD_ENABLED is true but no load callback was wired; uploads stay disabled")
		return
	}
	s.uploadsEnabled = true
	s.uploadDir = filepath.Join(s.mibsDir, "upload")
	s.loadFiles = loadFiles
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
