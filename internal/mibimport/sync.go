/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mibimport

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/mibcorpus"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// SyncCorpus validates the persisted cache against the curated tree
// (design D10): because the import pipeline is the tree's only
// writer, boot needs a stat walk, not a rebuild. New or changed
// files (size/mtime drift confirmed by hash) are compiled in one
// batch; fingerprints whose source vanished drop their module. An
// unchanged tree compiles nothing. The first boot after upgrading
// (no fingerprints yet) compiles everything once to build the index.
func (e *Engine) SyncCorpus(ctx context.Context) (compiled, removed int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	start := time.Now()

	known, err := e.Store.ListSourceFiles(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list fingerprints: %w", err)
	}
	if len(known) == 0 {
		slog.Info("no source fingerprints yet — building the index (one-time full compile)")
	}

	// Stat walk of the curated tree (import/ excluded — its files
	// are not corpus members until routed).
	onDisk := map[string]os.FileInfo{}
	walkErr := filepath.WalkDir(e.Root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			slog.Warn("corpus walk error; skipping", "path", path, "err", werr)
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if path != e.Root && (strings.HasPrefix(base, ".") || base == "LICENSES") {
				return filepath.SkipDir
			}
			if path == e.Dir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".mib", ".txt", ".my", "":
		default:
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(e.Root, path)
		if rerr != nil {
			return nil
		}
		onDisk[rel] = info
		return nil
	})
	if walkErr != nil {
		return 0, 0, fmt.Errorf("walk corpus: %w", walkErr)
	}

	// Diff: what needs compiling?
	var toCompile []string
	shaByRel := map[string]string{}
	for rel, info := range onDisk {
		fp, ok := known[rel]
		if ok && fp.Size == info.Size() && fp.MtimeNS == info.ModTime().UnixNano() {
			continue // fingerprint match — trust the cache
		}
		abs := filepath.Join(e.Root, rel)
		if ok2, _ := mibcorpus.HasMIBOpener(abs); !ok2 {
			continue // non-MIB stragglers (docs, metadata)
		}
		sha, _, herr := hashFile(abs)
		if herr != nil {
			slog.Warn("sync: hash failed; skipping", "path", rel, "err", herr)
			continue
		}
		if ok && fp.SHA256 == sha {
			// mtime-only drift (backup/restore, touch): refresh the
			// fingerprint, skip the compile.
			_ = e.Store.UpsertSourceFile(ctx, store.SourceFile{
				Path: rel, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(),
				SHA256: sha, ModuleName: fp.ModuleName,
			})
			continue
		}
		shaByRel[rel] = sha
		toCompile = append(toCompile, abs)
	}

	// Vanished sources: drop the fingerprint, and the module too
	// unless another curated file still declares it.
	moduleBackers := map[string]int{}
	for rel, fp := range known {
		if _, exists := onDisk[rel]; exists {
			moduleBackers[fp.ModuleName]++
		}
	}
	for rel, fp := range known {
		if _, exists := onDisk[rel]; exists {
			continue
		}
		_ = e.Store.DeleteSourceFile(ctx, rel)
		if moduleBackers[fp.ModuleName] == 0 && fp.ModuleName != "" {
			if derr := e.Store.DeleteModule(ctx, fp.ModuleName); derr == nil {
				slog.Info("removed module whose source vanished",
					"module", fp.ModuleName, "path", rel)
				removed++
			}
		}
	}

	if len(toCompile) == 0 {
		removed += e.pruneGhosts(ctx)
		slog.Info("corpus cache validated",
			"files", len(onDisk), "compiled", 0, "removed", removed,
			"duration", time.Since(start))
		return 0, removed, nil
	}

	smiPaths := e.smiPaths(false) // curated tree only — pending intake
	// files must not shadow curated modules during validation compiles
	comp := &compile.Compiler{
		Smidump: &compile.Smidump{Path: e.smidumpPath(), Paths: smiPaths},
	}
	if e.Smilint != "" {
		comp.Smilint = &compile.Smilint{Path: e.Smilint, Paths: smiPaths}
	}
	cctx, cancel := context.WithTimeout(ctx, scaledCompileTimeout(len(toCompile)))
	results := comp.Compile(cctx, toCompile)
	cancel()

	var smis []*compile.SMI
	for _, r := range results {
		if r.SMI != nil {
			smis = append(smis, r.SMI)
		}
	}
	refsByModule := map[string][]model.Reference{}
	for _, ref := range compile.BuildReferences(smis) {
		refsByModule[ref.SourceModule] = append(refsByModule[ref.SourceModule], ref)
	}

	for _, r := range results {
		rel, rerr := filepath.Rel(e.Root, r.Target)
		if rerr != nil {
			continue
		}
		if r.Err != nil || r.Module == nil || r.Module.Name == "" {
			// A curated file that no longer compiles (out-of-band
			// edit or first-boot discovery of a broken straggler):
			// keep any previously-stored module data, drop the
			// fingerprint so the next boot retries, and say so.
			slog.Warn("sync: curated file failed to compile",
				"path", rel, "err", r.Err)
			_ = e.Store.DeleteSourceFile(ctx, rel)
			continue
		}
		r.Module.SourcePath = r.Target
		if err := e.Store.ReplaceModule(ctx, r.Module, r.Symbols,
			refsByModule[r.Module.Name], r.Diagnostics); err != nil {
			slog.Warn("sync: store replace failed", "module", r.Module.Name, "err", err)
			continue
		}
		info := onDisk[rel]
		_ = e.Store.UpsertSourceFile(ctx, store.SourceFile{
			Path: rel, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(),
			SHA256: shaByRel[rel], ModuleName: r.Module.Name,
		})
		compiled++
	}

	removed += e.pruneGhosts(ctx)
	slog.Info("corpus cache validated",
		"files", len(onDisk), "compiled", compiled, "removed", removed,
		"duration", time.Since(start))
	return compiled, removed, nil
}

// pruneGhosts removes modules with no backing source_file row —
// pre-upgrade orphans (the fingerprint table is newer than their
// rows) and curated files that stopped compiling (their fingerprint
// is dropped on failure). Honest removal beats serving stale data.
func (e *Engine) pruneGhosts(ctx context.Context) int {
	names, err := e.Store.PruneModulesWithoutSource(ctx)
	if err != nil {
		slog.Warn("sync: ghost-module prune failed", "err", err)
		return 0
	}
	for _, n := range names {
		slog.Info("removed module without a backing corpus file", "module", n)
	}
	return len(names)
}
