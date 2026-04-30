// blittermib is a self-hostable, browser-based MIB reference tool.
// It compiles a directory of SNMP MIB files via libsmi, indexes them
// in SQLite + FTS5, and serves a typographically-disciplined web UI.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/mibsbundle"
	"github.com/no42-org/blittermib/internal/server"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/watch"
)

// version is set by the linker at release time via -ldflags.
var version = "dev"

// errPrintVersion signals that --version was passed and the program
// should print the version and exit cleanly.
var errPrintVersion = fmt.Errorf("print version")

type config struct {
	mibsDir string
	dataDir string
	listen  string
	verbose bool
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	switch {
	case err == errPrintVersion:
		fmt.Fprintln(os.Stderr, version)
		return
	case err != nil:
		os.Exit(2)
	}

	slog.SetDefault(newLogger(cfg.verbose))

	if err := run(cfg); err != nil {
		slog.Error("blittermib failed", "err", err)
		os.Exit(1)
	}
}

func parseFlags(args []string, errOut io.Writer) (config, error) {
	fs := flag.NewFlagSet("blittermib", flag.ContinueOnError)
	fs.SetOutput(errOut)

	var cfg config
	fs.StringVar(&cfg.mibsDir, "mibs", "./mibs", "directory containing user MIB files")
	fs.StringVar(&cfg.dataDir, "data", "./data", "directory for the SQLite database and runtime state")
	fs.StringVar(&cfg.listen, "listen", ":8080", "HTTP listen address (host:port)")
	fs.BoolVar(&cfg.verbose, "v", false, "verbose logging (DEBUG level)")
	showVersion := fs.Bool("version", false, "print version and exit")

	fs.Usage = func() {
		fmt.Fprintf(errOut, "blittermib %s — Pixelperfect MIB browser\n\n", version)
		fmt.Fprintf(errOut, "Usage:\n  blittermib [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if *showVersion {
		return cfg, errPrintVersion
	}
	return cfg, nil
}

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func run(cfg config) error {
	if err := os.MkdirAll(cfg.dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(cfg.mibsDir, 0o755); err != nil {
		return fmt.Errorf("create mibs dir: %w", err)
	}
	dbPath := filepath.Join(cfg.dataDir, "blittermib.db")
	standardDir := filepath.Join(cfg.dataDir, "standard-mibs")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	slog.Info("blittermib starting",
		"version", version,
		"mibs", cfg.mibsDir,
		"data", cfg.dataDir,
		"listen", cfg.listen,
	)

	// Stage the embedded standard MIB bundle so libsmi can resolve
	// IMPORTS for SNMPv2-SMI, SNMPv2-TC, etc. without requiring the
	// user to ship their own copy. User-supplied MIBs in mibsDir
	// override on filename collision (loaded after, ReplaceModule
	// is per-module so the second wins).
	staged, err := mibsbundle.Stage(standardDir)
	if err != nil {
		slog.Warn("staging standard MIBs failed", "err", err)
	} else if len(staged) > 0 {
		slog.Info("staged standard MIBs", "count", len(staged), "dir", standardDir)
	}

	smidumpPaths := []string{standardDir, cfg.mibsDir}
	loader := &loader{
		compiler: &compile.Compiler{
			Smidump: &compile.Smidump{Path: "smidump", Paths: smidumpPaths},
			Smilint: &compile.Smilint{Path: "smilint", Paths: smidumpPaths},
		},
		store: st,
	}

	// Initial scan: parse and ingest everything currently in the
	// standard bundle and the user's MIB directory. Failures are
	// logged per-module; a busted MIB doesn't abort startup.
	if err := loader.loadAll(ctx, standardDir, cfg.mibsDir); err != nil {
		slog.Warn("initial mib load encountered errors", "err", err)
	}

	// Watcher: hot-reload on any change in the MIB directory.
	watcher := watch.New(cfg.mibsDir, 250*time.Millisecond, func(ctx context.Context, files []string) {
		if err := loader.loadFiles(ctx, files); err != nil {
			slog.Warn("hot-reload failed", "err", err)
		}
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := watcher.Run(ctx); err != nil {
			slog.Warn("watcher exited with error", "err", err)
		}
	}()

	srv := server.New(st, cfg.listen, version, cfg.mibsDir)
	err = srv.Start(ctx)
	wg.Wait()

	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	slog.Info("blittermib stopped")
	return nil
}
