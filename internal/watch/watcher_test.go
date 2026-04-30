package watch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
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
