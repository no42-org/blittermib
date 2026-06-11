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

	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/mibimport"
	"github.com/no42-org/blittermib/internal/server"
	"github.com/no42-org/blittermib/internal/store"
	"github.com/no42-org/blittermib/internal/watch"
	"github.com/no42-org/blittermib/internal/web"
)

// version is set by the linker at release time via -ldflags.
var version = "dev"

// errPrintVersion signals that --version was passed and the program
// should print the version and exit cleanly.
var errPrintVersion = fmt.Errorf("print version")

type config struct {
	mibsDir     string
	mibsSet     bool // -mibs passed explicitly
	standardDir string
	dataDir     string
	listen      string
	verbose     bool
	rebuild     bool
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

	if err := run(context.Background(), cfg); err != nil {
		slog.Error("blittermib failed", "err", err)
		os.Exit(1)
	}
}

func parseFlags(args []string, errOut io.Writer) (config, error) {
	fs := flag.NewFlagSet("blittermib", flag.ContinueOnError)
	fs.SetOutput(errOut)

	var cfg config
	fs.StringVar(&cfg.mibsDir, "mibs", "", "corpus directory (default: <data>/mibs — tree, import/ intake, and cache persist as one unit)")
	fs.StringVar(&cfg.standardDir, "standard-mibs", "/usr/share/blittermib/mibs", "read-only standard corpus to mirror into the corpus root at boot (missing = skip)")
	fs.StringVar(&cfg.dataDir, "data", "./data", "directory for the SQLite database and runtime state")
	fs.StringVar(&cfg.listen, "listen", ":8080", "HTTP listen address (host:port)")
	fs.BoolVar(&cfg.verbose, "v", false, "verbose logging (DEBUG level)")
	fs.BoolVar(&cfg.rebuild, "rebuild", false, "discard the corpus cache fingerprints and recompile every MIB at boot")
	showVersion := fs.Bool("version", false, "print version and exit")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(errOut, "blittermib %s — Pixelperfect MIB browser\n\n", version)
		_, _ = fmt.Fprintf(errOut, "Usage:\n  blittermib [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "mibs" {
			cfg.mibsSet = true
		}
	})
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

// rescanInterval drives the periodic import/ sweep — the universal
// recovery path (design D6): files that arrived while the server was
// down, platform event-queue overflow, and mounts that deliver no
// change events all converge on the same ReadDir.
const rescanInterval = 5 * time.Minute

// bootstrapImportAndStandard prepares the intake skeleton and mirrors
// the image's read-only standard corpus into the corpus root,
// returning whether the import pipeline is usable.
//
// The standard mirror runs INDEPENDENTLY of intake writability. The
// standard set lands in the corpus ROOT (writable by the time we get
// here — run's os.MkdirAll on it succeeded, and the read-only fallback
// already cleared standardDir to "" so the sync is a no-op). A
// non-writable intake only disables imports; it must NOT leave the
// browser serving an empty corpus, so the sync is not gated on it.
func bootstrapImportAndStandard(engine *mibimport.Engine, standardDir string) (importOK bool) {
	importOK = true
	if err := engine.EnsureDirs(); err != nil {
		slog.Error("import pipeline DISABLED — intake directory is not writable; mount a writable volume to import MIBs",
			"dir", engine.Dir(), "err", err)
		importOK = false
	}
	if importOK {
		if n, err := engine.SweepTmp(); err != nil {
			slog.Warn("import tmp sweep failed", "err", err)
		} else if n > 0 {
			slog.Info("cleaned import tmp orphans", "count", n)
		}
	}
	// Mirror BEFORE validation (SyncCorpus): upgrades refresh ietf/ +
	// iana/ (only changed files are written, so only they recompile);
	// operator-owned paths are never touched.
	if _, _, err := engine.SyncStandard(standardDir); err != nil {
		slog.Warn("standard corpus sync encountered errors", "err", err)
	}
	return importOK
}

// testHookBeforeCorpusLoad, when non-nil, runs at the top of the
// background corpus-load goroutine. Tests use it to hold the load open
// and observe the serving-while-loading window (liveness up, readiness
// 503). Nil in production.
var testHookBeforeCorpusLoad func()

// resolveCorpusRoot creates the data directory and settles the corpus
// root in cfg: defaulting it into the data directory, falling back to
// serving the read-only standard set when the root is unwritable, and
// migrating the legacy upload/ drop folder.
func resolveCorpusRoot(cfg *config) error {
	if err := os.MkdirAll(cfg.dataDir, 0o750); err != nil { // #nosec G703 -- path derives from operator CLI flags (-data/-mibs/-standard-mibs), not request input.
		return fmt.Errorf("create data dir: %w", err)
	}
	// Default corpus location: INSIDE the data directory (the
	// standard-mibs-image change) — curated tree, import/ intake,
	// and SQLite cache persist as one unit on one volume, and the
	// import move stays same-filesystem by construction. -mibs
	// remains an override for deployments keeping the old layout.
	if !cfg.mibsSet || cfg.mibsDir == "" {
		cfg.mibsDir = filepath.Join(cfg.dataDir, "mibs")
		warnLegacyCorpus(cfg.mibsDir)
	}
	if err := os.MkdirAll(cfg.mibsDir, 0o750); err != nil { // #nosec G703 -- path derives from operator CLI flags (-data/-mibs/-standard-mibs), not request input.
		// Unwritable corpus root (e.g. docker run --read-only without
		// a data volume): fall back to SERVING the read-only standard
		// set in place. Imports are disabled; the corpus is whatever
		// the image ships — matching what such deployments got from
		// the old baked-corpus layout.
		if _, sErr := os.Stat(cfg.standardDir); sErr == nil { // #nosec G703 -- path derives from operator CLI flags (-data/-mibs/-standard-mibs), not request input.
			slog.Error("corpus root is not writable — serving the read-only standard corpus; imports DISABLED",
				"root", cfg.mibsDir, "standard", cfg.standardDir, "err", err)
			cfg.mibsDir = cfg.standardDir
			cfg.standardDir = "" // nothing to mirror onto itself
		} else {
			return fmt.Errorf("create mibs dir: %w", err)
		}
	}
	migrateLegacyUpload(cfg.mibsDir)
	return nil
}

// run boots the server. parent is the root context (main passes
// Background; tests pass a cancellable context to stop the server) —
// SIGINT/SIGTERM handling is layered on top of it.
func run(parent context.Context, cfg config) error {
	if err := resolveCorpusRoot(&cfg); err != nil {
		return err
	}
	dbPath := filepath.Join(cfg.dataDir, "blittermib.db")

	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	slog.Info("blittermib starting",
		"version", version,
		"mibs", cfg.mibsDir,
		"data", cfg.dataDir,
		"listen", cfg.listen,
	)

	// The import engine is the curated tree's only writer
	// (mib-import-pipeline): every custom MIB enters through
	// mibs/import/, and the persisted store is therefore a
	// trustworthy cache — boot validates fingerprints instead of
	// recompiling the corpus.
	engine := mibimport.New(cfg.mibsDir, st, mibcorpus.GroupMap{})
	engine.Smilint = "smilint"
	// A read-only corpus (e.g. readOnlyRootFilesystem with no volume
	// over the baked tree) can't host an intake directory. Degrade
	// gracefully: keep serving the corpus, disable the pipeline.
	importOK := bootstrapImportAndStandard(engine, cfg.standardDir)

	if cfg.rebuild {
		slog.Info("-rebuild: discarding corpus cache fingerprints")
		if err := st.ResetSourceFiles(ctx); err != nil {
			return fmt.Errorf("rebuild: %w", err)
		}
	}
	// Load the IETF groups map AFTER the standard sync — on a first
	// boot it arrives with the mirrored standard set.
	groups, err := mibcorpus.LoadGroups(filepath.Join(cfg.mibsDir, "_groups.yaml"))
	if err != nil {
		return fmt.Errorf("load groups: %w", err)
	}
	engine.Groups = groups

	// Construct the server and complete ALL route registration
	// (EnableUploads AND EnableWalk both call mux.Handle) plus the
	// write-once render globals BEFORE Start — registering on a live
	// ServeMux is a data race. The listener must bind before the corpus
	// load so liveness probes get a 200 instead of connection-refused
	// during a long first compile (the Kubernetes CrashLoop fix).
	web.SetVersion(version)
	srv := server.New(st, cfg.listen, version, cfg.mibsDir)
	if importOK {
		srv.EnableUploads(engine)
	} else {
		srv.EnableUploads(nil) // env on + nil engine fails closed with a WARN
	}
	srv.EnableWalk()
	web.SetWalkEnabled(srv.WalkEnabled())

	var wg sync.WaitGroup

	// The heavy corpus load runs in the background; the readiness gate
	// (/readyz) opens when the first pass completes. Joining via wg
	// keeps shutdown ordered: the deferred store Close must not race a
	// loader that is still writing.
	wg.Add(1)
	go corpusLoader(ctx, engine, srv, importOK, &wg)

	err = srv.Start(ctx)
	// Release the background goroutines before joining them: when Start
	// returns on a bind/serve ERROR (not a signal), ctx was never
	// cancelled — without this, the loader and watcher/ticker run
	// forever and wg.Wait would swallow the error in a silent hang.
	// stop is the NotifyContext cancel, so this also covers the normal
	// signal path (where it is a no-op second cancel).
	stop()
	wg.Wait()

	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	slog.Info("blittermib stopped")
	return nil
}

// corpusLoader is the background corpus-load goroutine: it validates
// the corpus cache (SyncCorpus), then — for a writable intake — runs
// the boot rescan and starts the intake watcher + periodic rescan,
// and finally opens the readiness gate.
//
// The watcher + periodic rescan start INSIDE this goroutine, after
// the initial sync: engine.Import serializes on the engine mutex but
// SyncCorpus does not, so launching the watcher earlier would let a
// file dropped into import/ during the load window run Import
// concurrently with SyncCorpus — an overlap that cannot happen today
// and that any reorder must not introduce.
//
// The caller holds a WaitGroup count for this goroutine (wg.Add(1)
// before `go corpusLoader(...)`); the watcher and ticker goroutines
// are added here while that count is still held, so the counter
// cannot reach zero mid-spawn.
func corpusLoader(ctx context.Context, engine *mibimport.Engine, srv *server.Server, importOK bool, wg *sync.WaitGroup) {
	defer wg.Done()
	// A panicking loader must not take the process down — the pod
	// stays alive (liveness 200) and visibly not-ready (readiness
	// 503), with the panic in the logs (design Risk 2).
	defer func() {
		if r := recover(); r != nil {
			slog.Error("corpus load panicked — readiness gate stays closed", "panic", r)
		}
	}()

	// rescan processes anything already sitting in import/.
	rescan := func(ctx context.Context) {
		pending, err := engine.Pending()
		if err != nil {
			slog.Warn("import rescan failed", "err", err)
			return
		}
		if len(pending) > 0 {
			engine.Import(ctx, pending)
		}
	}

	if hook := testHookBeforeCorpusLoad; hook != nil {
		hook()
	}
	if _, _, err := engine.SyncCorpus(ctx); err != nil {
		slog.Warn("corpus cache validation encountered errors", "err", err)
	}
	// Bail BEFORE the boot rescan when shutdown was requested
	// mid-load: engine.Import with an already-cancelled context
	// quarantines every pending file into import/failed/ with a
	// "context canceled" sidecar (the engine treats cancellation
	// like any compile failure), permanently eating intake that a
	// clean next boot would have processed.
	if ctx.Err() != nil {
		return
	}
	if importOK {
		rescan(ctx)

		watcher := watch.NewSingle(engine.Dir(), 250*time.Millisecond, func(ctx context.Context, files []string) {
			engine.Import(ctx, files)
		})

		// Safe concurrent Add: this goroutine still holds a
		// WaitGroup count, so the counter cannot reach zero while
		// these are added.
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := watcher.Run(ctx); err != nil {
				slog.Warn("watcher exited with error", "err", err)
			}
		}()
		go func() {
			defer wg.Done()
			tick := time.NewTicker(rescanInterval)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					rescan(ctx)
				}
			}
		}()
	}
	if ctx.Err() != nil {
		// Best-effort: skip reporting ready when shutdown is
		// already requested. (A cancel can still land between this
		// check and SetReady — benign: the listener is draining and
		// the process is exiting either way.)
		return
	}
	srv.SetReady()
	slog.Info("corpus loaded — readiness gate open")
}

// warnLegacyCorpus flags a populated pre-relocation corpus at the
// old container path when the relocated default is in effect — an
// upgraded deployment whose volume still mounts there would
// otherwise be silently ignored (drops never imported, vendor MIBs
// invisible). Heuristic on the well-known container path only.
func warnLegacyCorpus(newRoot string) {
	const legacyRoot = "/var/lib/blittermib/mibs"
	if legacyRoot == newRoot {
		return
	}
	entries, err := os.ReadDir(legacyRoot)
	if err != nil || len(entries) == 0 {
		return
	}
	slog.Warn("legacy corpus detected at the pre-v0.10 path — it is NOT being served; pass -mibs to keep that layout, or copy its vendors/ into the new corpus root (see README: Upgrading)",
		"legacy", legacyRoot, "active", newRoot)
}

// migrateLegacyUpload renames the pre-import-pipeline drop folder:
// deployments upgrading in place keep working, their pending files
// flow through the new pipeline on this same boot.
func migrateLegacyUpload(mibsDir string) {
	legacy := filepath.Join(mibsDir, "upload")
	current := filepath.Join(mibsDir, "import")
	if _, err := os.Stat(legacy); err != nil { // #nosec G703 -- path derives from operator CLI flags (-data/-mibs/-standard-mibs), not request input.
		return
	}
	if _, err := os.Stat(current); err == nil { // #nosec G703 -- path derives from operator CLI flags (-data/-mibs/-standard-mibs), not request input.
		slog.Warn("legacy mibs/upload/ present alongside mibs/import/ — not migrating; move its files into import/ manually",
			"legacy", legacy)
		return
	}
	if err := os.Rename(legacy, current); err != nil { // #nosec G703 -- path derives from operator CLI flags (-data/-mibs/-standard-mibs), not request input.
		slog.Warn("legacy upload/ migration failed", "err", err)
		return
	}
	slog.Info("migrated legacy mibs/upload/ to mibs/import/")
}
