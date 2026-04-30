package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/model"
	"github.com/no42-org/blittermib/internal/store"
)

// loader coordinates the compile pipeline and the store: walk the
// MIB directory, parse each file, build cross-references, and replace
// the affected modules in a transaction-per-module.
type loader struct {
	compiler *compile.Compiler
	store    *store.Store
}

// loadAll scans every dir in order and ingests every MIB it finds.
//
// Earlier dirs are loaded before later ones; on filename collision
// the later directory's file wins because ReplaceModule is per-module
// and the second compile overwrites. Pass standard-mibs first and
// the user dir last so user MIBs take precedence.
func (l *loader) loadAll(ctx context.Context, dirs ...string) error {
	var files []string
	for _, dir := range dirs {
		f, err := walkMIBFiles(dir)
		if err != nil {
			slog.Warn("walk mib dir failed", "dir", dir, "err", err)
			continue
		}
		files = append(files, f...)
	}
	if len(files) == 0 {
		slog.Info("no MIB files found", "dirs", dirs)
		return nil
	}
	return l.loadFiles(ctx, files)
}

// loadFiles compiles + stores a specific list of files. Used both
// for the initial scan and for hot-reload from the watcher.
func (l *loader) loadFiles(ctx context.Context, files []string) error {
	start := time.Now()
	results := l.compiler.Compile(ctx, files)

	// Build cross-references over the SMIs we just parsed; refs FROM
	// these modules are written below as part of ReplaceModule. Refs
	// INTO these modules from already-loaded modules stay valid
	// because they're keyed by qualified Module::Name pair, not row id.
	var smis []*compile.SMI
	for _, r := range results {
		if r.SMI != nil {
			smis = append(smis, r.SMI)
		}
	}
	allRefs := compile.BuildReferences(smis)
	refsByModule := make(map[string][]model.Reference, len(results))
	for _, ref := range allRefs {
		refsByModule[ref.SourceModule] = append(refsByModule[ref.SourceModule], ref)
	}

	loaded, failed := 0, 0
	for _, r := range results {
		if r.Err != nil {
			failed++
			slog.Warn("compile failed", "target", r.Target, "err", r.Err)
			continue
		}
		if r.Module == nil {
			failed++
			continue
		}
		modRefs := refsByModule[r.Module.Name]
		if err := l.store.ReplaceModule(ctx, r.Module, r.Symbols, modRefs, r.Diagnostics); err != nil {
			failed++
			slog.Warn("store replace failed", "module", r.Module.Name, "err", err)
			continue
		}
		loaded++
	}

	slog.Info("mib load complete",
		"loaded", loaded, "failed", failed,
		"files", len(files), "duration", time.Since(start),
	)
	return nil
}

// walkMIBFiles returns absolute paths of MIB-shaped files in dir.
// Filename heuristics: `.mib`, `.txt`, `.my`, or no extension. Hidden
// files (dotfiles) and subdirectories are skipped — the watcher only
// observes the top level so deep recursion would surprise the user.
func walkMIBFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".mib", ".txt", ".my", "":
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out, nil
}
