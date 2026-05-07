// Package watch observes the user's MIB directory via fsnotify and
// debounces change events before triggering a recompile of affected
// modules. Module replacement in the store is transactional so the
// served view never reflects a partial state.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Callback receives the set of MIB files that changed inside the
// debounce window. The watcher passes absolute paths.
type Callback func(ctx context.Context, changedFiles []string)

// Watcher batches fsnotify events from a single directory and fires
// a callback once the directory has been quiet for the debounce
// window. Use New to construct, then Run to block until ctx is done.
type Watcher struct {
	dir      string
	debounce time.Duration
	cb       Callback

	// mibExt allows tests to widen the file filter; in practice MIB
	// filenames are conventionally `.mib`, `.txt`, or no extension.
	mibExt []string

	mu      sync.Mutex
	pending map[string]struct{}
	timer   *time.Timer
}

// New constructs a Watcher for dir. debounce <= 0 falls back to 250 ms.
func New(dir string, debounce time.Duration, cb Callback) *Watcher {
	if debounce <= 0 {
		debounce = 250 * time.Millisecond
	}
	return &Watcher{
		dir:      dir,
		debounce: debounce,
		cb:       cb,
		mibExt:   []string{".mib", ".txt", ".my", ""},
		pending:  make(map[string]struct{}),
	}
}

// Run watches dir and every subdirectory beneath it until ctx is
// done. Recursion is implemented by walking the tree at startup and
// adding a per-directory watch, plus an event-loop handler that
// registers a watch on any newly-created subdirectory BEFORE walking
// its contents — closing the race window where a file could land
// inside the new directory between mkdir and watch-add.
//
// Skip rules mirror the loader: directories whose basename starts
// with `.` (hidden / `.git` / `.github`) and symlinks are ignored.
//
// Errors from fsnotify (including the initial Add on the root) are
// returned; transient event-channel errors are logged and recovery
// continues.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer fsw.Close()

	added, failed, err := w.addRecursive(fsw, w.dir)
	if err != nil {
		return fmt.Errorf("fsnotify add %s: %w", w.dir, err)
	}
	slog.Info("mib watcher started",
		"dir", w.dir,
		"debounce", w.debounce,
		"subdirs", added-1,
		"failed", failed,
	)

	for {
		select {
		case <-ctx.Done():
			w.flush(context.Background())
			return nil

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			// Newly-created directory: add the watch FIRST so any
			// files that land inside between mkdir and watch-add
			// still produce events; THEN walk the directory to pick
			// up anything that beat us to it. The walk emits
			// synthetic enqueue calls for files already present.
			if ev.Op&fsnotify.Create != 0 {
				if w.handleDirCreate(ctx, fsw, ev.Name) {
					continue
				}
			}
			if !w.relevant(ev) {
				continue
			}
			w.enqueue(ctx, ev.Name)

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			slog.Warn("fsnotify error", "err", err)
		}
	}
}

// addRecursive walks root and registers a per-directory watch on
// every non-hidden directory it finds. Returns (added, failed,
// error) so the caller can surface a counter when partial failures
// occur (e.g. inotify watch limit).
//
// Note on symlink handling: filepath.WalkDir uses Lstat, so a
// symlinked-to-directory surfaces with d.IsDir()=false and is
// silently skipped by the IsDir guard below. The walker never
// descends into symlinked subtrees, which is what we want
// (security — avoids registering watches on filesystems outside
// the corpus root via a malicious link).
func (w *Watcher) addRecursive(fsw *fsnotify.Watcher, root string) (added, failed int, err error) {
	var firstFailErr error
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Warn("watcher walk error; skipping", "path", path, "err", walkErr)
			failed++
			if firstFailErr == nil {
				firstFailErr = walkErr
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if err := fsw.Add(path); err != nil {
			// Per-dir errors (e.g. inotify watch limit) are warnings,
			// not fatal — the rest of the tree may still work.
			slog.Warn("fsnotify add failed", "path", path, "err", err)
			failed++
			if firstFailErr == nil {
				firstFailErr = err
			}
			return nil
		}
		added++
		return nil
	})
	if walkErr != nil {
		return added, failed, walkErr
	}
	if added == 0 {
		// The root itself failed to register — surface it so the
		// caller knows the watcher is non-functional. Wrap the
		// underlying cause when we have one.
		if firstFailErr != nil {
			return 0, failed, fmt.Errorf("no watches registered for %s: %w", root, firstFailErr)
		}
		return 0, failed, fmt.Errorf("no watches registered for %s", root)
	}
	return added, failed, nil
}

// handleDirCreate registers a watch on a newly-created directory and
// re-walks it to enqueue any files that landed before the watch was
// added. Returns true when the path was a directory (so the caller
// can skip the regular file-event handling).
func (w *Watcher) handleDirCreate(ctx context.Context, fsw *fsnotify.Watcher, path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		// ENOENT = the file was already removed before we got to
		// it (common during rapid create-delete bursts) — that's
		// fine, just fall through to the regular handler. Other
		// errors (EACCES, EIO) are noteworthy and should surface in
		// the log.
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("watcher Lstat failed", "path", path, "err", err)
		}
		return false
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return false
	}
	if !info.IsDir() {
		return false
	}
	if strings.HasPrefix(filepath.Base(path), ".") {
		return true
	}
	// Add the watch BEFORE walking — closes the race window where
	// a file could land in the new directory between mkdir and our
	// watch registration.
	if err := fsw.Add(path); err != nil {
		slog.Warn("fsnotify add (runtime) failed", "path", path, "err", err)
		return true
	}
	// Walk the new subtree and enqueue any pre-existing files +
	// any nested directories we now need to watch. Surface walk
	// errors (symlink loops, permission denials) via the logger
	// rather than swallowing them.
	if err := filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			return walkErr
		}
		if d.IsDir() {
			if p != path && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if p != path {
				if err := fsw.Add(p); err != nil {
					slog.Warn("fsnotify add (runtime nested) failed", "path", p, "err", err)
				}
			}
			return nil
		}
		// File entry: synthesise an enqueue if it looks like a MIB.
		// Symlinked files surface here with d.IsDir()=false; the
		// extension filter already keeps them sane.
		if w.relevantPath(p) {
			w.enqueue(ctx, p)
		}
		return nil
	}); err != nil {
		slog.Warn("watcher runtime walk error", "path", path, "err", err)
	}
	return true
}

// relevantPath is the path-only flavour of relevant() — used for the
// synthetic enqueue path where we don't have a real fsnotify.Event.
func (w *Watcher) relevantPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range w.mibExt {
		if ext == e {
			return true
		}
	}
	return false
}

// relevant returns true for events that warrant a recompile: writes,
// renames, removes, and creates of files that look like MIBs.
func (w *Watcher) relevant(ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	ext := strings.ToLower(filepath.Ext(ev.Name))
	for _, e := range w.mibExt {
		if ext == e {
			return true
		}
	}
	return false
}

func (w *Watcher) enqueue(ctx context.Context, path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[path] = struct{}{}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, func() { w.flush(ctx) })
}

func (w *Watcher) flush(ctx context.Context) {
	w.mu.Lock()
	if len(w.pending) == 0 {
		w.mu.Unlock()
		return
	}
	files := make([]string, 0, len(w.pending))
	for p := range w.pending {
		files = append(files, p)
	}
	w.pending = make(map[string]struct{})
	w.timer = nil
	w.mu.Unlock()

	slog.Info("mib watcher firing", "files", len(files))
	if w.cb != nil {
		w.invokeCallback(ctx, files)
	}
}

// invokeCallback runs the user callback with a recover, so a panic
// inside the loader (or anything it calls) doesn't take the binary
// down — the watcher catches it, logs the stack, drops the batch,
// and remains ready for the next event.
func (w *Watcher) invokeCallback(ctx context.Context, files []string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("mib watcher callback panicked",
				"err", r,
				"files", len(files),
				"stack", string(debug.Stack()),
			)
		}
	}()
	w.cb(ctx, files)
}
