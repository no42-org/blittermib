/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package server

import (
	"archive/zip"
	"context"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/no42-org/blittermib/internal/store"
)

// Shared machinery for the two bundle ZIP endpoints (module bundle and
// walk bundle): partition an import closure into shippable vs missing,
// and stream the shippable MIB sources into the archive.

// bundleEntry is a closure member whose source file ships in the ZIP.
type bundleEntry struct {
	Module     string
	SourcePath string
}

// bundleMissing is a closure member listed in MISSING.txt instead.
// Symbols is populated by the module bundle's closure (which tracks the
// imported symbols per edge) and stays empty for walk bundles.
type bundleMissing struct {
	Module     string
	Reason     string
	ImportedBy string
	Symbols    []string
}

// partitionClosure splits an import closure into "ship in the ZIP" vs
// "list in MISSING.txt". The spec defines exactly two reason markers
// (`not loaded` and `source file unreadable`); an empty source path or
// a path escaping the configured roots shares the `source file
// unreadable` marker rather than inventing a third — from the user's
// perspective the file isn't readable either way.
func partitionClosure(closure []store.ClosureEntry, roots []string) ([]bundleEntry, []bundleMissing) {
	var shippable []bundleEntry
	var missings []bundleMissing
	for _, e := range closure {
		switch {
		case !e.Loaded:
			missings = append(missings, bundleMissing{
				Module: e.Module, Reason: "not loaded",
				ImportedBy: e.ImportedBy, Symbols: e.Symbols,
			})
		case e.SourcePath == "" || !pathUnderAny(e.SourcePath, roots):
			missings = append(missings, bundleMissing{
				Module: e.Module, Reason: "source file unreadable",
				ImportedBy: e.ImportedBy, Symbols: e.Symbols,
			})
		default:
			shippable = append(shippable, bundleEntry{Module: e.Module, SourcePath: e.SourcePath})
		}
	}
	return shippable, missings
}

// copyMIBsToZip streams each shippable module into zw as
// `{Module}.mib`. Each entry is stamped with the source file's mtime so
// two clients downloading the same bundle one second apart get
// byte-identical archives (`time.Now()` would forfeit reproducibility).
// A per-file open failure degrades into an extra MISSING.txt row; a zip
// header/copy error or request-context cancellation is unrecoverable
// mid-stream and returns ok=false (the caller must stop writing).
func copyMIBsToZip(ctx context.Context, zw *zip.Writer, shippable []bundleEntry, logPrefix string) (extraMissing []bundleMissing, ok bool) {
	for _, ship := range shippable {
		// Honor request-context cancellation between entries — a
		// disconnected client shouldn't keep us reading and zipping
		// MIBs into a TCP void.
		if err := ctx.Err(); err != nil {
			slog.Warn(logPrefix+": ctx cancelled", "err", err)
			return extraMissing, false
		}
		f, err := os.Open(ship.SourcePath)
		if err != nil {
			slog.Warn(logPrefix+": open source", "module", ship.Module, "path", ship.SourcePath, "err", err)
			extraMissing = append(extraMissing, bundleMissing{
				Module: ship.Module, Reason: "source file unreadable",
			})
			continue
		}
		var modTime time.Time
		if info, err := f.Stat(); err == nil {
			modTime = info.ModTime()
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{
			Name:     ship.Module + ".mib",
			Method:   zip.Deflate,
			Modified: modTime,
		})
		if err != nil {
			slog.Warn(logPrefix+": zip header", "module", ship.Module, "err", err)
			_ = f.Close()
			return extraMissing, false
		}
		if _, err := io.Copy(fw, f); err != nil {
			slog.Warn(logPrefix+": copy source", "module", ship.Module, "err", err)
			_ = f.Close()
			return extraMissing, false
		}
		_ = f.Close()
	}
	return extraMissing, true
}

// writeZipString adds a deflate-compressed text entry to the archive.
func writeZipString(zw *zip.Writer, name, content string) error {
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return err
	}
	_, err = io.WriteString(fw, content)
	return err
}
