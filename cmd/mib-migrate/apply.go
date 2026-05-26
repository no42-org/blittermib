package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// applyCmd implements `blittermib-migrate apply`. Reads a TSV plan
// (possibly hand-edited after `plan`) and runs `git mv` per row,
// creating destination directories as needed and refusing to clobber
// an existing destination path.
//
// **Prerequisite for the §9 corpus migration:** `git mv` requires the
// source files to be tracked. The migration sequence per
// design.md / tasks.md §9 is:
//
//  1. remove `/mibs/` from `.gitignore`
//  2. `git add mibs/` to start tracking the existing operator collection
//  3. `blittermib-migrate plan --src ./mibs --out migration-plan.tsv`
//  4. review (and optionally hand-edit) `migration-plan.tsv`
//  5. `blittermib-migrate apply --plan migration-plan.tsv`
//
// Running `apply` against a plan whose sources aren't tracked will
// fail per-row with `git mv: not under version control`.
//
// The expected TSV header (validated on read) is:
//
//	src_path	dst_path	module	pen	confidence
//
// Lines starting with `#` and blank lines are tolerated so reviewers
// can comment-out or annotate rows during hand-editing.
func applyCmd(args []string) error {
	flags := flag.NewFlagSet("apply", flag.ContinueOnError)
	planPath := flags.String("plan", "migration-plan.tsv", "TSV plan path")
	root := flags.String("root", ".", "repository root for git mv")
	dryRun := flags.Bool("dry-run", false, "print git mv commands without executing them")
	if err := flags.Parse(args); err != nil {
		return err
	}

	rows, err := loadPlan(*planPath)
	if err != nil {
		return err
	}

	cleanRoot := filepath.Clean(*root)
	rootAbs, err := filepath.Abs(cleanRoot)
	if err != nil {
		return fmt.Errorf("resolve --root: %w", err)
	}

	var moved, skipped, failed int
	perVendor := make(map[string]int)
	for i, rec := range rows {
		src, dst := rec[0], rec[1]
		// Reject empty fields up front — saves a confusing per-row
		// `git mv ""` failure later.
		if src == "" || dst == "" {
			fmt.Fprintf(os.Stderr, "row %d: skip — empty src or dst\n", i+1)
			skipped++
			continue
		}
		// Path-safety: dst MUST stay inside --root. Reject absolute
		// paths and `..` traversal — a hand-edited plan with
		// "../../../etc/passwd" would otherwise be honoured.
		if !pathStaysInside(rootAbs, dst) {
			fmt.Fprintf(os.Stderr, "row %d: skip — dst %q escapes --root\n", i+1, dst)
			skipped++
			continue
		}
		// Resolve src/dst relative to --root for execution. We pass
		// repo-relative paths to `git mv` and set cmd.Dir explicitly
		// so git always operates on the intended worktree.
		joinedSrc := joinRepoPath(cleanRoot, src)
		joinedDst := joinRepoPath(cleanRoot, dst)

		if joinedSrc == joinedDst {
			fmt.Fprintf(os.Stderr, "row %d: skip — src and dst resolve to same path %q\n", i+1, joinedSrc)
			skipped++
			continue
		}

		// Refuse to clobber an existing destination. Use Lstat so a
		// dangling symlink at dst is treated as "exists" (the
		// conservative choice).
		if _, err := os.Lstat(joinedDst); err == nil {
			fmt.Fprintf(os.Stderr, "row %d: skip — %s already exists\n", i+1, joinedDst)
			skipped++
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "row %d: stat %s: %v\n", i+1, joinedDst, err)
			failed++
			continue
		}

		if err := os.MkdirAll(filepath.Dir(joinedDst), 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "row %d: mkdir %s: %v\n", i+1, filepath.Dir(joinedDst), err)
			failed++
			continue
		}

		if *dryRun {
			fmt.Printf("git mv -- %s %s\n", src, dst)
			moved++
			perVendor[vendorBucket(dst)]++
			continue
		}

		// `--` separator guards against src/dst that begin with `-`
		// being parsed as flags. cmd.Dir = cleanRoot so git resolves
		// the right `.git` directory regardless of caller cwd.
		// #nosec G204 -- offline CLI; src/dst come from the operator-supplied --plan TSV and are passed after a `--` separator to git mv.
		cmd := exec.Command("git", "mv", "--", src, dst)
		cmd.Dir = cleanRoot
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "row %d: git mv %s %s: %v\n", i+1, src, dst, err)
			failed++
			continue
		}
		moved++
		perVendor[vendorBucket(dst)]++
	}

	fmt.Printf("Apply done: %d moved, %d skipped, %d failed\n", moved, skipped, failed)
	printVendorSummary(perVendor)
	if failed > 0 {
		return fmt.Errorf("%d entries failed", failed)
	}
	return nil
}

// loadPlan reads the TSV, validates its header, tolerates blank lines
// and `#`-prefixed comments, and returns the data rows.
func loadPlan(path string) ([][]string, error) {
	// #nosec G304 -- path is the operator-supplied --plan flag value; mib-migrate is an offline CLI.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	r.Comma = '\t'
	r.Comment = '#'
	// FieldsPerRecord = -1 lets us tolerate blank/short rows so
	// reviewers can hand-edit; we validate width per row below.
	r.FieldsPerRecord = -1

	wantHeader := []string{"src_path", "dst_path", "module", "pen", "confidence"}
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if !equalSlice(header, wantHeader) {
		return nil, fmt.Errorf("plan header mismatch: got %v, want %v", header, wantHeader)
	}

	var rows [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		// Tolerate fully-blank rows (csv.Reader returns a single
		// empty field on a blank line when FieldsPerRecord <= 0).
		if isBlankRow(rec) {
			continue
		}
		if len(rec) != len(wantHeader) {
			return nil, fmt.Errorf("plan row has %d fields, want %d: %v", len(rec), len(wantHeader), rec)
		}
		rows = append(rows, rec)
	}
	return rows, nil
}

func isBlankRow(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pathStaysInside reports whether a relative or absolute dst, when
// joined to rootAbs, stays inside rootAbs after Clean. Defends
// against `..` traversal and absolute-path destinations.
func pathStaysInside(rootAbs, dst string) bool {
	if filepath.IsAbs(dst) {
		return false
	}
	joined := filepath.Clean(filepath.Join(rootAbs, dst))
	rel, err := filepath.Rel(rootAbs, joined)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// joinRepoPath joins a repo-relative path under root, returning a
// path suitable for both filesystem checks and `git mv`'s cmd.Dir
// resolution. When root is "." we keep paths relative; otherwise we
// produce paths relative to the caller's cwd via Clean.
func joinRepoPath(root, p string) string {
	if root == "." || root == "" {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(root, p))
}

// vendorBucket extracts the {PEN}-{slug} segment from a dst_path
// like "mibs/vendors/9-cisco/CISCO-RTTMON-MIB"; returns "" for
// non-vendor destinations.
func vendorBucket(dst string) string {
	i := strings.Index(dst, "vendors/")
	if i < 0 {
		return ""
	}
	rest := dst[i+len("vendors/"):]
	if j := strings.Index(rest, "/"); j > 0 {
		return rest[:j]
	}
	return ""
}

func printVendorSummary(perVendor map[string]int) {
	if len(perVendor) == 0 {
		return
	}
	delete(perVendor, "")
	if len(perVendor) == 0 {
		return
	}
	keys := make([]string, 0, len(perVendor))
	for k := range perVendor {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("Vendor breakdown:")
	for _, k := range keys {
		fmt.Printf("  %-30s %d\n", k, perVendor[k])
	}
}
