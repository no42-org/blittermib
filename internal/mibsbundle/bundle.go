// Package mibsbundle exposes the IETF/IANA standard MIB collection
// bundled into the binary at build time. blittermib stages the
// bundle into a directory at startup so libsmi's smidump can parse
// it alongside user MIBs.
//
// The bundle is intentionally separate from the user's MIB
// directory: it lives under {data}/standard-mibs/ and only files
// that don't already exist there are written. A user MIB with the
// same filename takes precedence simply by being loaded second.
//
// Population: run `make fetch-standard-mibs` to populate
// internal/mibsbundle/bundle/ from libsmi's standard MIB collection.
// The repo ships with the directory present (an embed root must
// exist at compile time) but content is opt-in.
package mibsbundle

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed bundle
var bundleFS embed.FS

// Stage extracts every bundled MIB into dir, creating dir if needed.
// Returns the absolute paths of files written this call. Files that
// already exist are left alone — fresh runs over a populated dir are
// idempotent and don't overwrite a user-customised standard MIB.
func Stage(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	var staged []string
	err := fs.WalkDir(bundleFS, "bundle", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		// Skip dotfiles and the README that documents the directory.
		if strings.HasPrefix(name, ".") || strings.EqualFold(name, "README.md") {
			return nil
		}
		dst := filepath.Join(dir, name)
		if _, err := os.Stat(dst); err == nil {
			return nil // already present
		}
		data, err := bundleFS.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
		staged = append(staged, dst)
		return nil
	})
	return staged, err
}

// Count returns the number of MIB files in the embedded bundle.
// Useful for startup logging and tests.
func Count() int {
	n := 0
	_ = fs.WalkDir(bundleFS, "bundle", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, ".") || strings.EqualFold(name, "README.md") {
			return nil
		}
		n++
		return nil
	})
	return n
}
