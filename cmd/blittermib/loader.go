package main

import (
	"context"
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
		if reason, ok := rejectReason(r); !ok {
			failed++
			slog.Warn("compile result rejected; skipping",
				"target", r.Target, "reason", reason)
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

// rejectReason returns ("", true) when a compile result is fit to
// persist, or (reason, false) when it should be skipped before
// reaching the store.
//
// Two failure modes need filtering:
//
//   - empty module name: smidump-with-`-k` can emit a stub `<smi>`
//     with no `<module>` element on truly unparseable input; the
//     resulting model.Module has Name="" which would poison
//     refsByModule keys and the module index.
//
//   - 0 symbols AND 0 imports: signature of a non-MIB file (e.g.
//     README, Makefile) that smidump happily fed through `-k` to
//     produce a phantom `<module name="…">`. Real macro-only
//     modules (RFC-1212, SNMPv2-CONF, vendor wrappers) legitimately
//     have 0 symbols but always declare IMPORTS, so this combination
//     is reliably "junk" rather than valid SMI.
func rejectReason(r compile.Result) (string, bool) {
	if r.Module == nil {
		return "nil module", false
	}
	if r.Module.Name == "" {
		return "empty module name", false
	}
	if len(r.Symbols) == 0 && len(r.Module.Imports) == 0 {
		return "phantom module (no symbols, no imports)", false
	}
	return "", true
}

// walkMIBFiles returns absolute paths of MIB-shaped files under dir.
// Walks recursively (post mib-corpus §4): the corpus layout is
// `mibs/{ietf,iana,vendors}/.../FILE`, so the loader needs to descend
// past the top level. Skip rules:
//
//   - directories whose basename starts with `.` (hidden / `.git` /
//     `.github`) — `filepath.SkipDir` short-circuits the subtree.
//   - symlinks (filepath.WalkDir uses Lstat, so symlinked dirs
//     appear as files and are filtered by the extension/marker
//     check below; symlinked files fail the extension/marker check
//     unless they reproduce a valid MIB body, which is fine).
//   - files whose extension isn't one of `.mib` / `.txt` / `.my` /
//     "" — the historical filename heuristic.
//   - files that pass the extension filter but lack the
//     `DEFINITIONS ::= BEGIN` lexical anchor — keeps non-MIB files
//     under the corpus (LICENSE, READMEs) from being parsed.
func walkMIBFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Don't abort the whole walk on a single permission /
			// stat error — log and continue.
			slog.Warn("walk error; skipping", "path", path, "err", err)
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		// Skip symlinks and other irregular file types. filepath.WalkDir
		// uses Lstat, so a symlinked-dir surfaces as a non-dir entry
		// with type fs.ModeSymlink (we don't follow it — security;
		// avoids reading outside the corpus root). FIFO/socket/device
		// would block os.Open or yield meaningless reads.
		if d.Type()&(fs.ModeSymlink|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeDevice|fs.ModeIrregular) != 0 {
			return nil
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".mib", ".txt", ".my", "":
		default:
			return nil
		}
		ok, err := mibcorpus.HasMIBOpener(path)
		if err != nil {
			slog.Warn("read failed; skipping", "path", path, "err", err)
			return nil
		}
		if !ok {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}

// sweepUploadTmp removes any *.upload tempfiles left over in
// `<mibsDir>/upload/.tmp/` from a crashed upload. Per the web-upload
// design (D6a), in-progress multipart writes go to .tmp/ and rename
// into upload/ on completion; any leftovers in .tmp/ are by
// definition partial and unsafe to keep around. Returns the number
// of files removed. Missing directories are not an error.
func sweepUploadTmp(mibsDir string) (int, error) {
	tmpDir := filepath.Join(mibsDir, "upload", ".tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var removed int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".upload") {
			continue
		}
		if err := os.Remove(filepath.Join(tmpDir, e.Name())); err != nil {
			slog.Warn("upload tmp sweep: remove failed", "file", e.Name(), "err", err)
			continue
		}
		removed++
	}
	return removed, nil
}

// walkMIBDirs returns every subdirectory under root (root itself
// included), suitable for handing to libsmi via SMIPATH so the
// parser can resolve IMPORTS across the whole corpus regardless of
// layout. Skip rules mirror walkMIBFiles' directory pruning:
//
//   - directories whose basename starts with `.` (hidden / `.git`)
//   - the corpus's `LICENSES/` directory (it never holds MIBs)
//
// Symlinked directories are not followed (filepath.WalkDir uses
// Lstat, so symlinks surface as non-dir entries). On a fresh
// corpus root that doesn't yet exist on disk we return the root
// alone — the caller (cfg.mibsDir is mkdir-all'd before this) just
// gets an empty SMIPATH entry, which libsmi tolerates.
func walkMIBDirs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("walk error; skipping", "path", path, "err", err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (strings.HasPrefix(name, ".") || name == "LICENSES") {
			return filepath.SkipDir
		}
		out = append(out, path)
		return nil
	})
	return out, err
}
