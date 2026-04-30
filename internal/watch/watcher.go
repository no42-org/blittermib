// Package watch observes the user's MIB directory via fsnotify and
// debounces change events before triggering a recompile of affected
// modules. Module replacement in the store is transactional so the
// served view never reflects a partial state.
package watch

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
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

// Run watches dir until ctx is done. Errors from fsnotify (including
// the initial Add) are returned; transient event-channel errors are
// logged and recovery continues.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer fsw.Close()

	if err := fsw.Add(w.dir); err != nil {
		return fmt.Errorf("fsnotify add %s: %w", w.dir, err)
	}
	slog.Info("mib watcher started", "dir", w.dir, "debounce", w.debounce)

	for {
		select {
		case <-ctx.Done():
			w.flush(context.Background())
			return nil

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
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
		w.cb(ctx, files)
	}
}
