/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

// Package mibimport is the single intake pipeline for custom MIBs
// (the mib-import-pipeline change): files arrive in the corpus
// root's import/ directory (by filesystem drop or web upload), get
// settled, sniffed, deduplicated, compiled, classified, and moved
// into the curated tree. Files that can't be imported quarantine in
// import/failed/, already-known content in import/duplicate/ — each
// with a JSON reason sidecar. The engine is the curated tree's ONLY
// writer, which is what makes the persisted store trustworthy: boot
// validates fingerprints instead of recompiling the corpus.
package mibimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// Status classifies one import outcome.
type Status string

const (
	StatusImported  Status = "imported"
	StatusFailed    Status = "failed"
	StatusDuplicate Status = "duplicate"
)

// Outcome is the per-file result of an import pass.
type Outcome struct {
	Path        string // original path in import/
	Name        string // base filename
	Status      Status
	Module      *model.Module // populated on success
	SymbolCount int
	Diagnostics []model.Diagnostic
	Reason      string // failed / duplicate explanation
	Dest        string // curated path relative to the corpus root (imported)
	Existing    string // existing corpus path (duplicate)
}

// Sidecar is the JSON written beside quarantined files — the
// filesystem is the source of truth for quarantine state; the
// store's outcome rows are display state.
type Sidecar struct {
	Name       string `json:"name"`
	Status     Status `json:"status"`
	Reason     string `json:"reason"`
	Existing   string `json:"existing,omitempty"`
	OccurredAt string `json:"occurred_at"`
}

// Engine routes import/ intake into the curated tree. All entry
// points serialize on one mutex (single-flight: compiles stay
// batched, moves stay race-free).
type Engine struct {
	Root    string // corpus root (the directory holding ietf/, vendors/, import/, …)
	Store   *store.Store
	Groups  mibcorpus.GroupMap
	Smidump string // binary path; "smidump" by default
	Smilint string // binary path; "" disables lint diagnostics

	mu sync.Mutex
	// poisoned remembers intake paths whose quarantine move failed
	// (e.g. a read-only intake mount): without it, the periodic
	// rescan would recompile the same un-movable file every cycle
	// forever. Cleared by restart.
	poisoned map[string]struct{}
}

// New constructs an engine with default tool paths.
func New(root string, st *store.Store, groups mibcorpus.GroupMap) *Engine {
	return &Engine{Root: root, Store: st, Groups: groups, Smidump: "smidump",
		poisoned: make(map[string]struct{})}
}

// Dir is the intake directory.
func (e *Engine) Dir() string { return filepath.Join(e.Root, "import") }

// FailedDir holds files that could not be imported.
func (e *Engine) FailedDir() string { return filepath.Join(e.Dir(), "failed") }

// DuplicateDir holds files skipped as duplicates.
func (e *Engine) DuplicateDir() string { return filepath.Join(e.Dir(), "duplicate") }

// TmpDir stages atomic writes (web uploads).
func (e *Engine) TmpDir() string { return filepath.Join(e.Dir(), ".tmp") }

// EnsureDirs creates the intake skeleton.
func (e *Engine) EnsureDirs() error {
	for _, d := range []string{e.Dir(), e.FailedDir(), e.DuplicateDir(), e.TmpDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// SweepTmp removes orphaned staging files left in import/.tmp/ by
// interrupted uploads.
func (e *Engine) SweepTmp() (int, error) {
	entries, err := os.ReadDir(e.TmpDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(e.TmpDir(), ent.Name())); err == nil {
			n++
		}
	}
	return n, nil
}

// Pending lists unprocessed files sitting flat in import/ —
// subdirectories (including the outcome dirs) are ignored by design.
func (e *Engine) Pending() ([]string, error) {
	entries, err := os.ReadDir(e.Dir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || strings.HasPrefix(name, ".") || name == ".gitkeep" ||
			strings.HasSuffix(name, sidecarSuffix) {
			continue
		}
		if !ent.Type().IsRegular() {
			continue
		}
		out = append(out, filepath.Join(e.Dir(), name))
	}
	return out, nil
}

// Import runs the pipeline over the given files (which must sit flat
// in import/). Files still being written (size/mtime unstable across
// the settle window) are skipped silently — the periodic rescan
// retries them. Returns one outcome per processed file.
func (e *Engine) Import(ctx context.Context, paths []string) []Outcome {
	return e.run(ctx, paths, false)
}

// ImportReplace runs the pipeline with module replacement allowed —
// the web upload's explicit ?replace=true path (design D11): an
// existing module's curated file is superseded instead of the new
// file quarantining as a duplicate.
func (e *Engine) ImportReplace(ctx context.Context, paths []string) []Outcome {
	return e.run(ctx, paths, true)
}

// ImportReplacing re-runs a single quarantined (or pending) file with
// replacement allowed — the sanctioned overwrite path for updating an
// existing module (design D11). The file is moved back into import/
// first when it sits in a quarantine dir.
func (e *Engine) ImportReplacing(ctx context.Context, path string) Outcome {
	base := filepath.Base(path)
	intake := uniquePath(filepath.Join(e.Dir(), base))
	if filepath.Dir(path) != e.Dir() {
		if err := moveFile(path, intake); err != nil {
			return Outcome{Path: path, Name: base, Status: StatusFailed,
				Reason: fmt.Sprintf("stage for replacement: %v", err)}
		}
		_ = os.Remove(sidecarPath(path))
	}
	res := e.run(ctx, []string{intake}, true)
	if len(res) == 1 {
		return res[0]
	}
	return Outcome{Path: intake, Name: base, Status: StatusFailed,
		Reason: "file disappeared during replacement"}
}

func (e *Engine) run(ctx context.Context, paths []string, replace bool) []Outcome {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.EnsureDirs(); err != nil {
		slog.Warn("import: ensure dirs failed", "err", err)
		return nil
	}

	type cand struct {
		path string
		base string
		sha  string
		size int64
	}
	var (
		outcomes []Outcome
		toCheck  []cand
	)

	// Flat-intake filter, then a SINGLE batch settle: stat every
	// candidate, sleep one 200 ms window, re-stat — files whose
	// size/mtime moved are still being written and defer to the
	// rescan. One sleep per batch, not per file (a 1k-file bulk drop
	// would otherwise sleep 200 s serially under the engine mutex).
	type settling struct {
		path string
		base string
		st   os.FileInfo
	}
	var candidates []settling
	for _, p := range paths {
		base := filepath.Base(p)
		if filepath.Dir(p) != e.Dir() || strings.HasPrefix(base, ".") ||
			strings.HasSuffix(base, sidecarSuffix) {
			// Sidecar-suffixed names are reserved (they'd collide
			// with quarantine sidecars); they sit inert in import/.
			continue
		}
		if _, bad := e.poisoned[p]; bad {
			continue // un-movable on a previous pass; skip until restart
		}
		st1, err := os.Lstat(p)
		if err != nil || !st1.Mode().IsRegular() {
			continue // vanished or non-regular (symlink, pipe)
		}
		candidates = append(candidates, settling{path: p, base: base, st: st1})
	}
	if len(candidates) > 0 {
		time.Sleep(settleWindow)
	}
	for _, c := range candidates {
		st2, err := os.Stat(c.path)
		if err != nil || st2.Size() != c.st.Size() || !st2.ModTime().Equal(c.st.ModTime()) {
			slog.Debug("import: file still settling; deferring", "path", c.path)
			continue
		}
		sha, size, err := hashFile(c.path)
		if err != nil {
			outcomes = append(outcomes, e.quarantine(ctx, c.path, StatusFailed,
				fmt.Sprintf("unreadable: %v", err), ""))
			continue
		}
		toCheck = append(toCheck, cand{path: c.path, base: c.base, sha: sha, size: size})
	}

	// Sniff + byte-identical dedupe (cheap, pre-compile).
	var toCompile []cand
	for _, c := range toCheck {
		ok, err := mibcorpus.HasMIBOpener(c.path)
		if err != nil || !ok {
			outcomes = append(outcomes, e.quarantine(ctx, c.path, StatusFailed,
				"not a MIB (DEFINITIONS ::= BEGIN absent in first 32 KB)", ""))
			continue
		}
		if !replace {
			if existing, err := e.Store.FindSourceFileBySHA(ctx, c.sha); err == nil && existing != nil {
				outcomes = append(outcomes, e.quarantine(ctx, c.path, StatusDuplicate,
					"byte-identical to an existing corpus file", existing.Path))
				continue
			}
		}
		toCompile = append(toCompile, c)
	}
	if len(toCompile) == 0 {
		return outcomes
	}

	// Compile the batch (intra-batch IMPORTS resolve against each
	// other plus the curated tree). Bound scaled like the ingest CLI:
	// a hang backstop, never a throughput ceiling.
	files := make([]string, len(toCompile))
	byPath := make(map[string]cand, len(toCompile))
	for i, c := range toCompile {
		files[i] = c.path
		byPath[c.path] = c
	}
	smiPaths := e.smiPaths(true) // intake on the path: intra-batch IMPORTS
	comp := &compile.Compiler{
		Smidump: &compile.Smidump{Path: e.smidumpPath(), Paths: smiPaths},
	}
	if e.Smilint != "" {
		comp.Smilint = &compile.Smilint{Path: e.Smilint, Paths: smiPaths}
	}
	cctx, cancel := context.WithTimeout(ctx, scaledCompileTimeout(len(files)))
	results := comp.Compile(cctx, files)
	cancel()

	// Cross-references over the batch (refs into already-loaded
	// modules stay valid; they're keyed by qualified name).
	var smis []*compile.SMI
	for _, r := range results {
		if r.SMI != nil {
			smis = append(smis, r.SMI)
		}
	}
	refsByModule := make(map[string][]model.Reference)
	for _, ref := range compile.BuildReferences(smis) {
		refsByModule[ref.SourceModule] = append(refsByModule[ref.SourceModule], ref)
	}

	for _, r := range results {
		c := byPath[r.Target]
		switch {
		case budgetExhausted(cctx.Err(), r.Err):
			// Incomplete work, not a broken MIB: leave the file in
			// import/ — the rescan retries with a fresh budget.
			slog.Warn("import: compile budget exhausted; file deferred", "path", r.Target)
			continue
		case r.Err != nil:
			outcomes = append(outcomes, e.quarantine(ctx, r.Target, StatusFailed,
				compileReason(r.Err), ""))
			continue
		case r.Module == nil || r.Module.Name == "":
			outcomes = append(outcomes, e.quarantine(ctx, r.Target, StatusFailed,
				"compile produced no module identity", ""))
			continue
		case !mibcorpus.ValidModuleName.MatchString(r.Module.Name):
			outcomes = append(outcomes, e.quarantine(ctx, r.Target, StatusFailed,
				fmt.Sprintf("module name %q contains characters disallowed in a corpus filename", r.Module.Name), ""))
			continue
		}

		// Same module name already curated?
		existingMod, err := e.Store.GetModule(ctx, r.Module.Name)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			outcomes = append(outcomes, e.quarantine(ctx, r.Target, StatusFailed,
				fmt.Sprintf("store lookup: %v", err), ""))
			continue
		}
		if existingMod != nil && !replace {
			rel := e.relPath(existingMod.SourcePath)
			outcomes = append(outcomes, e.quarantine(ctx, r.Target, StatusDuplicate,
				fmt.Sprintf("module %s already exists; content differs", r.Module.Name), rel))
			continue
		}

		// Route into the curated tree.
		cls := mibcorpus.Classify(r.Module.OIDRoot, r.Module.Name, e.Groups, nil)
		var destRel string
		if cls.Confidence == mibcorpus.ConfidenceLow {
			// unsorted/ is keyed by filename — uniquify so distinct
			// same-named files don't overwrite each other.
			destRel, _ = filepath.Rel(e.Root,
				uniquePath(filepath.Join(e.Root, "unsorted", c.base)))
		} else {
			destRel = filepath.Join(cls.DstDir, r.Module.Name)
		}
		if !filepath.IsLocal(destRel) {
			outcomes = append(outcomes, e.quarantine(ctx, r.Target, StatusFailed,
				fmt.Sprintf("computed destination escapes corpus root: %s", destRel), ""))
			continue
		}
		dest := filepath.Join(e.Root, destRel)

		// Replacement removes the superseded source first (the
		// classification may have moved, e.g. a revised OID root).
		if existingMod != nil && replace {
			if old := e.relPath(existingMod.SourcePath); old != "" && old != destRel {
				_ = os.Remove(filepath.Join(e.Root, old))
				_ = e.Store.DeleteSourceFile(ctx, old)
			}
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			outcomes = append(outcomes, e.quarantine(ctx, r.Target, StatusFailed,
				fmt.Sprintf("create destination dir: %v", err), ""))
			continue
		}
		if err := moveFile(r.Target, dest); err != nil {
			outcomes = append(outcomes, e.quarantine(ctx, r.Target, StatusFailed,
				fmt.Sprintf("move into corpus: %v", err), ""))
			continue
		}

		// The module's source of record is its curated location.
		r.Module.SourcePath = dest
		if err := e.Store.ReplaceModule(ctx, r.Module, r.Symbols,
			refsByModule[r.Module.Name], r.Diagnostics); err != nil {
			slog.Warn("import: store replace failed", "module", r.Module.Name, "err", err)
			outcomes = append(outcomes, Outcome{Path: r.Target, Name: c.base,
				Status: StatusFailed, Reason: fmt.Sprintf("store: %v", err)})
			continue
		}
		if st, err := os.Stat(dest); err == nil {
			_ = e.Store.UpsertSourceFile(ctx, store.SourceFile{
				Path: destRel, Size: st.Size(), MtimeNS: st.ModTime().UnixNano(),
				SHA256: c.sha, ModuleName: r.Module.Name,
			})
		}
		oc := Outcome{
			Path: r.Target, Name: c.base, Status: StatusImported,
			Module: r.Module, SymbolCount: len(r.Symbols),
			Diagnostics: r.Diagnostics, Dest: destRel,
		}
		_ = e.Store.RecordImportOutcome(ctx, store.ImportOutcome{
			Name: c.base, Status: string(StatusImported),
			ModuleName: r.Module.Name, Detail: destRel,
		})
		slog.Info("imported MIB", "module", r.Module.Name, "dest", destRel)
		outcomes = append(outcomes, oc)
	}
	return outcomes
}

// quarantine moves a file into the matching outcome dir, writes its
// sidecar, and records the outcome.
func (e *Engine) quarantine(ctx context.Context, path string, st Status, reason, existing string) Outcome {
	base := filepath.Base(path)
	dir := e.FailedDir()
	if st == StatusDuplicate {
		dir = e.DuplicateDir()
	}
	dest := uniquePath(filepath.Join(dir, base))
	if err := moveFile(path, dest); err != nil {
		// Un-movable (read-only intake?): poison the path so the
		// rescan doesn't recompile it every cycle forever.
		slog.Warn("import: quarantine move failed; skipping this file until restart",
			"path", path, "err", err)
		e.poisoned[path] = struct{}{}
	}
	sc := Sidecar{Name: base, Status: st, Reason: reason, Existing: existing,
		OccurredAt: time.Now().UTC().Format(time.RFC3339)}
	if b, err := json.MarshalIndent(sc, "", "  "); err == nil {
		// #nosec G306 -- sidecar beside the quarantined file under the
		// engine-owned outcome dir; 0o600 satisfies the linter, group
		// readability is not needed (single-uid container).
		_ = os.WriteFile(sidecarPath(dest), b, 0o600)
	}
	_ = e.Store.RecordImportOutcome(ctx, store.ImportOutcome{
		Name: base, Status: string(st), Detail: firstNonEmpty(existing, reason),
	})
	slog.Info("import quarantined", "file", base, "status", string(st), "reason", reason)
	return Outcome{Path: path, Name: base, Status: st, Reason: reason, Existing: existing}
}

// QuarantineEntry is one quarantined file with its sidecar, for the
// management UI.
type QuarantineEntry struct {
	Name    string
	Size    int64
	Sidecar Sidecar
}

// ListQuarantine reads an outcome dir (FailedDir/DuplicateDir) with
// sidecar reasons. Files without a sidecar still list (reason empty).
func ListQuarantine(dir string) ([]QuarantineEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []QuarantineEntry
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || strings.HasPrefix(name, ".") || strings.HasSuffix(name, sidecarSuffix) {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		q := QuarantineEntry{Name: name, Size: info.Size()}
		if b, err := os.ReadFile(sidecarPath(filepath.Join(dir, name))); err == nil {
			_ = json.Unmarshal(b, &q.Sidecar)
		}
		out = append(out, q)
	}
	return out, nil
}

// RemoveQuarantined deletes a quarantined file and its sidecar.
func RemoveQuarantined(dir, name string) error {
	p := filepath.Join(dir, filepath.Base(name))
	if err := os.Remove(p); err != nil {
		return err
	}
	_ = os.Remove(sidecarPath(p))
	return nil
}

const sidecarSuffix = ".reason.json"

func sidecarPath(file string) string { return file + sidecarSuffix }

// settleWindow is the size/mtime stability window — the fallback
// completed-write check for writers that don't rename into place
// (web uploads do; bulk copies may not). Applied ONCE per batch.
const settleWindow = 200 * time.Millisecond

// uniquePath returns dst, or dst-1/dst-2/… when dst already exists —
// quarantine and unsorted/ destinations are keyed by FILENAME (not
// module name), so distinct files sharing a basename must not
// silently overwrite each other.
func uniquePath(dst string) string {
	if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
		return dst
	}
	for i := 1; ; i++ {
		c := fmt.Sprintf("%s-%d", dst, i)
		if _, err := os.Lstat(c); errors.Is(err, fs.ErrNotExist) {
			return c
		}
	}
}

// moveFile renames, falling back to copy+fsync+rename for
// cross-filesystem moves (EXDEV — e.g. import/ bind-mounted on a
// different volume than the curated tree).
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		// Same-FS rename failed for a real reason (perms, missing
		// dir); surface it rather than masking with a copy.
		return err
	}
	// #nosec G304 -- src/dst are engine-derived paths rooted under the
	// corpus root (intake names pass ValidModuleName before they can
	// reach a curated destination; quarantine targets are basenames
	// joined onto engine-owned dirs).
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp := dst + ".importing"
	// #nosec G302 G304 -- same engine-derived path family; 0o600 is
	// sufficient in a single-uid deployment.
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

func (e *Engine) smidumpPath() string {
	if e.Smidump != "" {
		return e.Smidump
	}
	return "smidump"
}

// smiPaths lists every directory under the corpus root (plus the
// intake dir) for the IMPORTS resolver — recomputed per batch, so
// curated directories created by earlier batches are visible without
// a restart.
func (e *Engine) smiPaths(includeIntake bool) []string {
	var dirs []string
	_ = filepath.WalkDir(e.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if path == e.Dir() && !includeIntake {
			// Pending intake files are NOT corpus members — during
			// SyncCorpus compiles a broken pending file must not
			// shadow a curated module's name on the resolver path.
			return filepath.SkipDir
		}
		base := d.Name()
		if path != e.Root && (strings.HasPrefix(base, ".") || base == "LICENSES" ||
			base == "failed" || base == "duplicate") {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	return dirs
}

// relPath converts a module SourcePath (absolute or root-relative)
// to a corpus-root-relative path; empty when outside the root.
func (e *Engine) relPath(p string) string {
	if p == "" {
		return ""
	}
	abs := p
	if !filepath.IsAbs(abs) {
		return filepath.Clean(p)
	}
	rel, err := filepath.Rel(e.Root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

func hashFile(path string) (string, int64, error) {
	// #nosec G304 -- callers pass intake/curated paths the engine
	// itself enumerated under the corpus root.
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// scaledCompileTimeout mirrors the ingest CLI's bound: a hang
// backstop (1 s/file, 5 m floor), never a throughput ceiling.
func scaledCompileTimeout(n int) time.Duration {
	if d := time.Duration(n) * time.Second; d > 5*time.Minute {
		return d
	}
	return 5 * time.Minute
}

// budgetExhausted mirrors the ingest CLI's two-shape detection:
// queued files carry the ctx error; in-flight kills surface as
// signal-terminated children under an expired bound.
func budgetExhausted(ctxErr, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return errors.Is(ctxErr, context.DeadlineExceeded) && isSignalKilled(err)
}

// compileReason trims the smidump invocation prefix down to the
// operator-relevant part of a compile error.
func compileReason(err error) string {
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return msg
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
