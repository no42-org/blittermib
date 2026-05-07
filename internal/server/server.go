package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/no42-org/blittermib/internal/store"
)

// Server is the blittermib HTTP server.
type Server struct {
	store   *store.Store
	version string
	mibsDir string
	mux     *http.ServeMux
	http    *http.Server
}

// New constructs a Server bound to addr backed by the given store.
// mibsDir is the corpus root — shown to the user on the empty-state
// landing page so they know where to drop MIB files, and used as the
// allowed root for the module-download path-traversal guard. version
// is surfaced at /version and in the /healthz body.
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
