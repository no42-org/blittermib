/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mibimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// standardEntries are the image-owned subtrees of the corpus root:
// the boot sync mirrors them from the read-only image copy and never
// touches anything else (vendors/, unsorted/, import/ are
// operator-owned).
var standardEntries = []string{"ietf", "iana", "_groups.yaml", "LICENSES"}

// standardManifest records which files the sync itself placed —
// ownership is tracked explicitly because the import pipeline ALSO
// files custom MIBs into ietf/{group}/: pruning "anything not in the
// image" would delete operator imports co-located in the standard
// subtrees. Only files listed in the PREVIOUS manifest and absent
// from the new image are pruned; with no manifest (first boot,
// pre-manifest upgrade) nothing is pruned.
const standardManifest = ".standard-manifest.json"

// SyncStandard mirrors the standard corpus from srcRoot (the
// read-only copy baked into the image) into the writable corpus
// root: files are copied only when content differs (so fingerprints
// stay stable and the validation walk recompiles only real
// upstream changes), and files the image no longer ships are
// removed (the image is authoritative for the standard set).
// Operator-owned paths are never created, modified, or deleted.
//
// A missing srcRoot is a no-op — bare-metal/dev runs operate on a
// full checkout and have no image copy to mirror.
func (e *Engine) SyncStandard(srcRoot string) (copied, removed int, err error) {
	if _, statErr := os.Stat(srcRoot); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			slog.Debug("no standard corpus image copy; skipping sync", "src", srcRoot)
			return 0, 0, nil
		}
		return 0, 0, statErr
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	start := time.Now()

	manifestPath := filepath.Join(e.Root, standardManifest)
	var previous []string
	if b, rerr := os.ReadFile(manifestPath); rerr == nil { // #nosec G304 -- engine-owned path
		_ = json.Unmarshal(b, &previous)
	}

	var (
		current  []string // root-relative paths the sync owns after this pass
		firstErr error
	)
	for _, entry := range standardEntries {
		src := filepath.Join(srcRoot, entry)
		dst := filepath.Join(e.Root, entry)
		srcInfo, statErr := os.Stat(src)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue // image doesn't ship this entry
		}
		if statErr != nil {
			if firstErr == nil {
				firstErr = statErr
			}
			continue
		}

		if !srcInfo.IsDir() {
			c, cErr := syncFile(src, dst)
			if cErr != nil {
				slog.Warn("standard sync: file failed; continuing", "path", entry, "err", cErr)
				if firstErr == nil {
					firstErr = cErr
				}
				continue
			}
			copied += c
			current = append(current, entry)
			continue
		}

		walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				if p != src && strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel, rErr := filepath.Rel(src, p)
			if rErr != nil {
				return rErr
			}
			c, cErr := syncFile(p, filepath.Join(dst, rel))
			if cErr != nil {
				slog.Warn("standard sync: file failed; continuing",
					"path", filepath.Join(entry, rel), "err", cErr)
				if firstErr == nil {
					firstErr = cErr
				}
				return nil // best-effort: a bad file must not strand the rest
			}
			copied += c
			current = append(current, filepath.Join(entry, rel))
			return nil
		})
		if walkErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("sync %s: %w", entry, walkErr)
			}
		}
	}

	// Prune ONLY files the sync previously placed that the new image
	// no longer ships — operator files co-located in ietf/iana (the
	// import pipeline routes there) are never candidates.
	owned := make(map[string]struct{}, len(current))
	for _, rel := range current {
		owned[rel] = struct{}{}
	}
	for _, rel := range previous {
		if _, still := owned[rel]; still {
			continue
		}
		abs := filepath.Join(e.Root, rel)
		if !filepath.IsLocal(rel) {
			continue
		}
		if os.Remove(abs) == nil {
			removed++
			// Best-effort empty-dir cleanup up to the entry root.
			for d := filepath.Dir(abs); d != e.Root; d = filepath.Dir(d) {
				if os.Remove(d) != nil {
					break
				}
			}
		}
	}

	if b, mErr := json.MarshalIndent(current, "", " "); mErr == nil {
		_ = os.WriteFile(manifestPath, b, 0o640) // #nosec G306 -- engine-owned manifest
	}
	err = firstErr

	if copied > 0 || removed > 0 {
		slog.Info("standard corpus synced from image",
			"copied", copied, "removed", removed, "duration", time.Since(start))
	}
	return copied, removed, err
}

// syncFile copies src over dst only when the content differs;
// returns 1 when a copy happened. Equal files are left untouched so
// their fingerprints (size+mtime) stay valid and the validation
// walk skips them.
func syncFile(src, dst string) (int, error) {
	sb, err := os.ReadFile(src) // #nosec G304 -- image-owned standard corpus path
	if err != nil {
		return 0, err
	}
	if db, err := os.ReadFile(dst); err == nil && bytes.Equal(sb, db) { // #nosec G304
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, err
	}
	tmp := dst + ".syncing"
	if err := os.WriteFile(tmp, sb, 0o640); err != nil { // #nosec G306 G703 -- dst derives from engine-owned standardEntries joined under the corpus root via a trusted image-copy walk (no user input); group-readable corpus file
		return 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return 1, nil
}
