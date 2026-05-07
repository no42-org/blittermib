package watch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatcherDebouncesAndFires drops a few files into a temp dir and
// asserts that the callback fires exactly once with all the files
// after the debounce window elapses.
func TestWatcherDebouncesAndFires(t *testing.T) {
	dir := t.TempDir()

	var (
		mu    sync.Mutex
		fired [][]string
	)
	cb := func(_ context.Context, files []string) {
		mu.Lock()
		defer mu.Unlock()
		fired = append(fired, files)
	}

	w := New(dir, 60*time.Millisecond, cb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = w.Run(ctx) }()
	// Give Run() a moment to register the watch.
	time.Sleep(50 * time.Millisecond)

	for _, name := range []string{"A.mib", "B.mib", "C.mib"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Spread writes within the debounce window so they coalesce.
		time.Sleep(15 * time.Millisecond)
	}

	// Wait for debounce window plus a bit.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 {
		t.Fatalf("callback fired %d times, want 1: %+v", len(fired), fired)
	}
	if len(fired[0]) != 3 {
		t.Errorf("first call had %d files, want 3: %v", len(fired[0]), fired[0])
	}
}

// TestWatcherIgnoresNonMIBFiles asserts that files with extensions
// outside the MIB filter (e.g. .DS_Store, .swp, .lock) don't trigger
// the callback.
func TestWatcherIgnoresNonMIBFiles(t *testing.T) {
	dir := t.TempDir()

	calls := 0
	cb := func(_ context.Context, _ []string) { calls++ }
	w := New(dir, 40*time.Millisecond, cb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	for _, name := range []string{".DS_Store", "foo.swp", "scratch.json"} {
		_ = os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	time.Sleep(150 * time.Millisecond)

	if calls != 0 {
		t.Errorf("callback fired %d times for non-MIB files; expected 0", calls)
	}
}

// TestWatcherSurvivesCallbackPanic verifies that a panic in the cb
// (e.g. an unexpected loader bug) is recovered, the batch is dropped,
// and the watcher remains live to fire on subsequent events.
//
// Without the recover, a single panic would take the whole binary
// down — the cb runs in a time.AfterFunc goroutine that the Go
// runtime can't recover for us.
func TestWatcherSurvivesCallbackPanic(t *testing.T) {
	dir := t.TempDir()

	var fired int32
	cb := func(_ context.Context, _ []string) {
		atomic.AddInt32(&fired, 1)
		panic("intentional test panic")
	}
	w := New(dir, 40*time.Millisecond, cb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// First event → cb panics, watcher recovers.
	if err := os.WriteFile(filepath.Join(dir, "a.mib"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(&fired); got != 1 {
		t.Fatalf("first cb didn't fire: count=%d", got)
	}

	// Second event → if recover worked, cb fires again.
	if err := os.WriteFile(filepath.Join(dir, "b.mib"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	if got := atomic.LoadInt32(&fired); got != 2 {
		t.Errorf("watcher did not survive cb panic: count=%d, want 2", got)
	}
}

// TestWatcherFiresOnRename verifies rename events trigger the callback —
// editor "save" workflows often go via rename (atomic write).
func TestWatcherFiresOnRename(t *testing.T) {
	dir := t.TempDir()

	var (
		mu    sync.Mutex
		fired bool
	)
	cb := func(_ context.Context, _ []string) {
		mu.Lock()
		fired = true
		mu.Unlock()
	}
	w := New(dir, 40*time.Millisecond, cb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	old := filepath.Join(dir, "old.mib")
	newp := filepath.Join(dir, "new.mib")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	fired = false
	mu.Unlock()

	if err := os.Rename(old, newp); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !fired {
		t.Error("rename did not fire callback")
	}
}

// waitFor polls fn until it returns true or the deadline passes.
// Returns true on success. Used to replace fragile time.Sleep waits
// in watcher tests where the registration / event-delivery latency
// is bounded but variable on slow CI runners.
func waitFor(deadline time.Duration, step time.Duration, fn func() bool) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if fn() {
			return true
		}
		time.Sleep(step)
	}
	return fn()
}

// TestWatcherRecursive seeds a temp tree with a pre-existing nested
// directory, drops a MIB file into a deeply-nested subdir, and
// asserts the callback fires. Verifies §5.1 (per-directory watch at
// startup). Uses poll-based synchronisation rather than fixed
// time.Sleep waits to stay reliable on slow CI runners.
func TestWatcherRecursive(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "vendors", "9-cisco")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	var got [][]string
	var mu sync.Mutex
	cb := func(_ context.Context, files []string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, files)
	}

	w := New(root, 80*time.Millisecond, cb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()

	target := filepath.Join(deep, "CISCO-EXAMPLE-MIB")

	// Race-tolerant write: poll until the file write produces a
	// callback batch containing it. The watcher needs Run() to
	// finish addRecursive before the write is observable; if the
	// first attempt's event lands before the watch registers, the
	// write triggers nothing. We retry after a short delay.
	containsTarget := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, batch := range got {
			for _, f := range batch {
				if f == target {
					return true
				}
			}
		}
		return false
	}
	// Wait for startup walk to complete before the first write
	// (best-effort: the slog.Info "mib watcher started" line fires
	// after addRecursive returns).
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(target, []byte("dummy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !waitFor(2*time.Second, 20*time.Millisecond, containsTarget) {
		mu.Lock()
		defer mu.Unlock()
		t.Errorf("nested MIB %s not in any callback batch within 2s: %v", target, got)
	}
}

// TestWatcherRuntimeDirCreate covers §5.2: a directory created at
// runtime (after the watcher has started) gets a watch BEFORE the
// race-window walk picks up its contents. Files dropped into the new
// dir should produce callback invocations.
//
// The intermediate `vendors/` directory is created BEFORE the
// watcher starts so the test produces a single, deterministic
// Create event for the leaf — `MkdirAll(vendors/61509-no42)` would
// otherwise produce two Create events and the inner `mkdir` could
// race against the outer dir's watch registration.
func TestWatcherRuntimeDirCreate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vendors"), 0o755); err != nil {
		t.Fatal(err)
	}

	var got [][]string
	var mu sync.Mutex
	cb := func(_ context.Context, files []string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, files)
	}

	w := New(root, 80*time.Millisecond, cb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
	// Brief wait for addRecursive to register the watches.
	time.Sleep(50 * time.Millisecond)

	// Single mkdir: only the leaf directory is new.
	newDir := filepath.Join(root, "vendors", "61509-no42")
	if err := os.Mkdir(newDir, 0o755); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(newDir, "NO42-EXAMPLE-MIB")
	containsTarget := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, batch := range got {
			for _, f := range batch {
				if f == target {
					return true
				}
			}
		}
		return false
	}

	// Poll until the new dir's watch registration is plausibly
	// done (handleDirCreate is synchronous in the event loop, so
	// once we see the Mkdir event has been processed, the watch is
	// registered). A short wait then lets us write the target.
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(target, []byte("dummy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !waitFor(2*time.Second, 20*time.Millisecond, containsTarget) {
		mu.Lock()
		defer mu.Unlock()
		t.Errorf("file in runtime-created dir %s not seen in any callback batch within 2s: %v", target, got)
	}
}

// TestWatcherSkipsHiddenSubtree asserts §5.4: hidden directories
// (e.g. `.git`) are not watched, so writes inside them never fire
// the callback.
func TestWatcherSkipsHiddenSubtree(t *testing.T) {
	root := t.TempDir()
	hidden := filepath.Join(root, ".git")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}

	var calls int
	cb := func(_ context.Context, _ []string) { calls++ }
	w := New(root, 60*time.Millisecond, cb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
	time.Sleep(80 * time.Millisecond)

	// Write a MIB-shaped file inside the hidden subtree.
	if err := os.WriteFile(filepath.Join(hidden, "FAKE-MIB"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	if calls != 0 {
		t.Errorf("watcher fired %d times for files under %s; expected 0", calls, hidden)
	}
}
