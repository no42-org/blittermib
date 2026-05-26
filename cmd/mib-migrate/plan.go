package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/no42-org/blittermib/internal/compile"
	"github.com/no42-org/blittermib/internal/mibcorpus"
)

type planEntry struct {
	SrcPath    string
	DstPath    string
	Module     string
	PEN        string
	Confidence Confidence
}

// validModuleName is re-exported from internal/mibcorpus to avoid
// the duplicate-regex drift surface (same gate ships in mib-ingest
// and the loader). See `mibcorpus.ValidModuleName` for the rule.
var validModuleName = mibcorpus.ValidModuleName

// planCmd implements `blittermib-migrate plan`.
func planCmd(args []string) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	src := flags.String("src", "", "source directory (current flat MIBs collection) — required")
	out := flags.String("out", "migration-plan.tsv", "output TSV path")
	groupsPath := flags.String("groups", "mibs/_groups.yaml", "IETF groups map (read-only; missing OK)")
	smidump := flags.String("smidump", "smidump", "smidump binary path")
	smilint := flags.String("smilint", "smilint", "smilint binary path; pass '' to skip")
	prefix := flags.String("prefix", "mibs", "destination prefix prepended to every dst_path (e.g. 'mibs')")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *src == "" {
		return fmt.Errorf("--src is required")
	}
	info, err := os.Stat(*src)
	if err != nil {
		return fmt.Errorf("--src: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--src must be a directory, got %s", *src)
	}

	groups, err := LoadGroups(*groupsPath)
	if err != nil {
		return fmt.Errorf("load groups: %w", err)
	}

	files, err := walkSource(*src)
	if err != nil {
		return fmt.Errorf("walk %s: %w", *src, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no MIB-shaped files found under %s", *src)
	}

	c := &compile.Compiler{
		Smidump: &compile.Smidump{Path: *smidump, Paths: []string{*src}},
	}
	if *smilint != "" {
		c.Smilint = &compile.Smilint{Path: *smilint, Paths: []string{*src}}
	}

	results := c.Compile(context.Background(), files)

	entries := make([]planEntry, 0, len(results))
	for _, r := range results {
		e := planEntry{SrcPath: r.Target}

		// Bail early to unsorted if compile failed or the module data
		// is structurally incomplete (non-nil Module but empty Name
		// or OIDRoot — defensive against future compile changes).
		bad := r.Err != nil ||
			r.Module == nil ||
			r.Module.Name == "" ||
			r.Module.OIDRoot == "" ||
			!validModuleName.MatchString(r.Module.Name)

		if bad {
			name := "?"
			if r.Module != nil && r.Module.Name != "" && validModuleName.MatchString(r.Module.Name) {
				name = r.Module.Name
			}
			e.Module = name
			e.PEN = "-"
			e.Confidence = ConfidenceLow
			e.DstPath = filepath.Join(*prefix, "unsorted", filepath.Base(r.Target))
			entries = append(entries, e)
			continue
		}

		cls := Classify(r.Module.OIDRoot, r.Module.Name, groups, MigrationSlugOverrides)
		e.Module = r.Module.Name
		if cls.PEN > 0 {
			e.PEN = fmt.Sprintf("%d", cls.PEN)
		} else {
			e.PEN = "-"
		}
		e.Confidence = cls.Confidence
		e.DstPath = filepath.Join(*prefix, cls.DstDir, e.Module)
		entries = append(entries, e)
	}

	// Detect duplicate destinations (two MIBs producing the same
	// dst_path silently collide on apply otherwise). Downgrade to
	// unsorted/<basename> with low confidence so the maintainer can
	// reconcile by hand-editing the TSV.
	dupCount := dedupDestinations(entries, *prefix)

	sort.Slice(entries, func(i, j int) bool { return entries[i].SrcPath < entries[j].SrcPath })

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Comma = '\t'
	if err := w.Write([]string{"src_path", "dst_path", "module", "pen", "confidence"}); err != nil {
		_ = f.Close()
		return err
	}
	for _, e := range entries {
		if err := w.Write([]string{e.SrcPath, e.DstPath, e.Module, e.PEN, string(e.Confidence)}); err != nil {
			_ = f.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", *out, err)
	}

	summarize(entries, dupCount, os.Stderr)
	return nil
}

// dedupDestinations rewrites entries that share a dst_path so all
// duplicates land in unsorted/<basename> for manual review. Returns
// the number of entries that were re-routed.
func dedupDestinations(entries []planEntry, prefix string) int {
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		counts[e.DstPath]++
	}
	rerouted := 0
	for i := range entries {
		if counts[entries[i].DstPath] > 1 {
			entries[i].DstPath = filepath.Join(prefix, "unsorted", filepath.Base(entries[i].SrcPath))
			entries[i].Confidence = ConfidenceLow
			rerouted++
		}
	}
	return rerouted
}

// walkSource returns MIB-shaped files under dir. Hidden files and
// directories are skipped, as are symlinks and irregular file types.
// Filename heuristics match the existing loader: `.mib`, `.txt`,
// `.my`, or no extension. The lexical-marker check filters out
// non-MIB files that share those extensions (LICENSE, README,
// LICENSES/*.txt, vendor docs) without paying the libsmi-parse cost.
func walkSource(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
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
		// Skip symlinks and irregular file types (FIFO, socket,
		// device) — `os.Open` on a FIFO would block indefinitely
		// and a symlink could escape the corpus root.
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

func summarize(entries []planEntry, dupCount int, w *os.File) {
	var hi, md, lo int
	perVendor := make(map[string]int)
	for _, e := range entries {
		switch e.Confidence {
		case ConfidenceHigh:
			hi++
		case ConfidenceMedium:
			md++
		case ConfidenceLow:
			lo++
		}
		// Count vendor-bucket entries by their {PEN}-{slug} segment.
		if i := strings.Index(e.DstPath, "vendors/"); i >= 0 {
			rest := e.DstPath[i+len("vendors/"):]
			if j := strings.Index(rest, "/"); j > 0 {
				perVendor[rest[:j]]++
			}
		}
	}
	_, _ = fmt.Fprintf(w, "Plan summary: %d entries (high=%d, medium=%d, low=%d)\n", len(entries), hi, md, lo)
	if dupCount > 0 {
		_, _ = fmt.Fprintf(w, "Duplicates rerouted to unsorted/: %d\n", dupCount)
	}
	if len(perVendor) > 0 {
		keys := make([]string, 0, len(perVendor))
		for k := range perVendor {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		_, _ = fmt.Fprintln(w, "Vendor breakdown:")
		for _, k := range keys {
			_, _ = fmt.Fprintf(w, "  %-30s %d\n", k, perVendor[k])
		}
	}
}
