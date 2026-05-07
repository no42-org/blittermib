package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Entry is one row of INDEX.yaml. Field order in the struct matches
// the on-disk emission order — keep them in sync with emitEntry.
type Entry struct {
	File    string
	Module  string
	PEN     uint32
	Vendor  string
	License string
	Imports []string
	Status  string
	AddedIn string
}

// validToken matches the conservative character set we accept in
// INDEX.yaml string fields without quoting (file paths, module names,
// vendor slugs, license tags, status, added_in). All values produced
// by the index pipeline pass this — we reject anything that doesn't
// rather than emit invalid YAML. Underscore is allowed in file paths
// even though it's non-canonical SMI, because operator collections
// occasionally use it (`OLD_VENDOR.mib`).
var validToken = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

const (
	defaultStatus  = "current"
	licenseUnknown = "unknown"
)

// indexCmd implements `blittermib-index`. Walks --root, derives
// metadata from path + source header for each MIB, merges
// _overrides.yaml, and emits sorted YAML to --out.
func indexCmd(args []string) error {
	flags := flag.NewFlagSet("blittermib-index", flag.ContinueOnError)
	root := flags.String("root", "mibs", "corpus root directory")
	out := flags.String("out", "mibs/INDEX.yaml", "output INDEX.yaml path")
	overridesPath := flags.String("overrides", "mibs/_overrides.yaml", "overrides YAML path (missing OK)")
	dateStr := flags.String("date", "", "added_in date for new entries (YYYY-MM-DD); default: today UTC")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if info, err := os.Stat(*root); err != nil {
		return fmt.Errorf("--root: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("--root must be a directory, got %s", *root)
	}

	if *dateStr == "" {
		*dateStr = time.Now().UTC().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", *dateStr); err != nil {
		return fmt.Errorf("--date %q: %w", *dateStr, err)
	}

	overrides, err := LoadOverrides(*overridesPath)
	if err != nil {
		return fmt.Errorf("load overrides: %w", err)
	}

	prevAddedIn, err := readPrevAddedIn(*out)
	if err != nil {
		return fmt.Errorf("read prior INDEX.yaml: %w", err)
	}

	entries, skipped, err := buildEntries(*root, overrides, prevAddedIn, *dateStr)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	emitYAML(&buf, entries)

	// Idempotency guard: if the existing file already matches what
	// we'd emit, skip the write so file mtimes stay clean.
	if existing, err := os.ReadFile(*out); err == nil && bytes.Equal(existing, buf.Bytes()) {
		fmt.Fprintf(os.Stderr, "INDEX.yaml unchanged (%d entries)\n", len(entries))
		if skipped > 0 {
			return fmt.Errorf("%d entries skipped during walk; see warnings above", skipped)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d entries)\n", *out, len(entries))
	if skipped > 0 {
		return fmt.Errorf("%d entries skipped during walk; see warnings above", skipped)
	}
	return nil
}

// buildEntries walks root and builds the sorted entry list. Returns
// the list, a count of files skipped due to per-file errors (so the
// caller can surface a non-zero exit), and any walk-level error.
func buildEntries(root string, overrides *Overrides, prevAddedIn map[string]string, today string) ([]Entry, int, error) {
	var entries []Entry
	var skipped int

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Don't abort the whole walk on a single permission /
			// bad-symlink error. Log and continue.
			slog.Warn("walk error; skipping", "path", path, "err", err)
			skipped++
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isMIBFile(name) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			slog.Warn("relpath failed; skipping", "path", path, "err", err)
			skipped++
			return nil
		}
		// filepath.Rel uses OS separator; INDEX.yaml uses POSIX `/`.
		rel = filepath.ToSlash(rel)

		src, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("read failed; skipping", "path", path, "err", err)
			skipped++
			return nil
		}
		e, ok, err := buildEntry(rel, src, overrides, prevAddedIn, today)
		if err != nil {
			slog.Warn("build entry failed; skipping", "path", path, "err", err)
			skipped++
			return nil
		}
		if !ok {
			// Not a MIB (no DEFINITIONS ::= BEGIN marker).
			// Silently skip — typical false positives are LICENSE,
			// README, etc. that look extensionless.
			return nil
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, skipped, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].File < entries[j].File })
	return entries, skipped, nil
}

// buildEntry produces a single INDEX.yaml entry from a relpath and
// source bytes. Returns ok=false (with no error) when the source has
// no `DEFINITIONS ::= BEGIN` marker — that's a non-MIB file we want
// to silently skip. Returns an error when the entry is structurally
// invalid (file path or values that can't be safely emitted).
func buildEntry(rel string, src []byte, overrides *Overrides, prevAddedIn map[string]string, today string) (Entry, bool, error) {
	module := extractModuleName(src)
	if module == "" {
		// No SMI module opener → not a MIB. Skip silently.
		return Entry{}, false, nil
	}

	e := Entry{
		File:   rel,
		Module: module,
		Status: defaultStatus,
	}

	if pen, slug, ok := penFromPath(rel); ok {
		e.PEN = pen
		e.Vendor = slug
	}

	if v := overrides.LicenseFor(module); v != "" {
		e.License = v
	} else {
		tag, _ := detectLicenseE(bytes.NewReader(src))
		e.License = tag
	}
	if e.License == "" {
		e.License = licenseUnknown
	}

	e.Imports = extractImports(src)

	if d, ok := prevAddedIn[rel]; ok {
		e.AddedIn = d
	} else {
		e.AddedIn = today
	}

	// Defence-in-depth: validate every string field before emission
	// so the hand-rolled YAML can't smuggle YAML-special characters.
	if err := validateEntryFields(e); err != nil {
		return e, true, err
	}
	return e, true, nil
}

// validateEntryFields rejects any string value that doesn't fit the
// `validToken` character set. Today every producer happens to be
// safe — vendor/file from path, module from SMI grammar, license
// from a fixed tag list, status hardcoded, added_in date-validated —
// but pinning the gate here keeps a future change to any of those
// from leaking into the emitted YAML.
func validateEntryFields(e Entry) error {
	checks := []struct{ name, value string }{
		{"file", e.File},
		{"module", e.Module},
		{"vendor", e.Vendor}, // empty allowed (non-vendor MIBs)
		{"license", e.License},
		{"status", e.Status},
		{"added_in", e.AddedIn},
	}
	for _, c := range checks {
		if c.value == "" {
			continue
		}
		if !validToken.MatchString(c.value) {
			return fmt.Errorf("%s contains characters disallowed in INDEX.yaml: %q", c.name, c.value)
		}
	}
	for _, imp := range e.Imports {
		if !validToken.MatchString(imp) {
			return fmt.Errorf("import name contains characters disallowed in INDEX.yaml: %q", imp)
		}
	}
	return nil
}

func isMIBFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mib", ".txt", ".my", "":
		return true
	}
	return false
}

// emitYAML writes the canonical INDEX.yaml form. Hand-rolled rather
// than yaml.v3-marshalled for two reasons: (1) flow-style imports
// match design.md's example, and (2) deterministic ordering of fields
// is trivially obvious vs. relying on struct tags.
//
// Example output:
//
//	mibs:
//	  - file: vendors/9-cisco/CISCO-RTTMON-MIB
//	    module: CISCO-RTTMON-MIB
//	    pen: 9
//	    vendor: cisco
//	    license: cisco
//	    imports: [SNMPv2-SMI, SNMPv2-TC]
//	    status: current
//	    added_in: 2026-05-04
func emitYAML(w *bytes.Buffer, entries []Entry) {
	w.WriteString("# Generated by `make index` — do not edit by hand.\n")
	w.WriteString("# Source of truth for license tags + import closures.\n")
	w.WriteString("# Hand-tweak `mibs/_overrides.yaml` to override license tags.\n")
	if len(entries) == 0 {
		// Empty corpus: emit a flow-style empty list so the YAML is
		// explicitly `mibs: []` rather than `mibs:` (which parses as
		// nil — same semantic, less obvious to a reader).
		w.WriteString("mibs: []\n")
		return
	}
	w.WriteString("mibs:\n")
	for _, e := range entries {
		fmt.Fprintf(w, "  - file: %s\n", e.File)
		fmt.Fprintf(w, "    module: %s\n", e.Module)
		if e.PEN > 0 {
			fmt.Fprintf(w, "    pen: %d\n", e.PEN)
		}
		if e.Vendor != "" {
			fmt.Fprintf(w, "    vendor: %s\n", e.Vendor)
		}
		license := e.License
		if license == "" {
			license = licenseUnknown
		}
		fmt.Fprintf(w, "    license: %s\n", license)
		if len(e.Imports) == 0 {
			w.WriteString("    imports: []\n")
		} else {
			fmt.Fprintf(w, "    imports: [%s]\n", strings.Join(e.Imports, ", "))
		}
		status := e.Status
		if status == "" {
			status = defaultStatus
		}
		fmt.Fprintf(w, "    status: %s\n", status)
		fmt.Fprintf(w, "    added_in: %s\n", e.AddedIn)
	}
}

// readPrevAddedIn loads the existing INDEX.yaml and returns
// {file: added_in} so regeneration preserves the date for entries
// that already exist. Missing file returns an empty map. A malformed
// existing file returns the empty map plus a stderr warning — better
// than aborting the regen.
func readPrevAddedIn(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	prev, err := parseIndexAddedIn(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: prior INDEX.yaml at %s unparseable; rebuilding all dates: %v\n", path, err)
		return map[string]string{}, nil
	}
	return prev, nil
}

// parseIndexAddedIn pulls the file/added_in pairs out of an existing
// INDEX.yaml using yaml.v3 — robust against indent variants, quoted
// values, comment-line additions, and CRLF line endings.
func parseIndexAddedIn(data []byte) (map[string]string, error) {
	var prior struct {
		Mibs []struct {
			File    string `yaml:"file"`
			AddedIn string `yaml:"added_in"`
		} `yaml:"mibs"`
	}
	if err := yaml.Unmarshal(data, &prior); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(prior.Mibs))
	for _, e := range prior.Mibs {
		if e.File == "" || e.AddedIn == "" {
			continue
		}
		out[e.File] = e.AddedIn
	}
	return out, nil
}
