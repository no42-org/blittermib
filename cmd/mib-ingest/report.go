/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// errReportActionable is the sentinel ingestCmd returns when
// --report mode produces at least one finding with severity `warn`
// or `error`. main() recognises it and exits 1 silently (no stderr
// pollution so JSON output stays clean for `jq` pipelines).
var errReportActionable = errors.New("report has actionable findings")

// textTruncationCap bounds the per-category section in text-format
// reports. JSON output is uncapped.
const textTruncationCap = 50

// Category constants enumerate the seven finding categories the
// report mode emits. Renaming any of these is a JSON-schema
// breaking change for downstream tooling that filters by category.
const (
	CategoryByteIdentical       = "byte-identical"
	CategoryModuleNameCollision = "module-name-collision"
	CategoryOIDArcSharing       = "oid-arc-sharing"
	CategoryDivergentIdentity   = "divergent-identity"
	CategoryCorpusCollision     = "corpus-collision"
	CategoryBroken              = "broken"
	CategoryNonMIB              = "non-mib"
)

// Severity constants drive the report-mode exit-code policy:
// `warn` and `error` cause non-zero exit; `info` does not.
const (
	SeverityInfo  = "info"
	SeverityWarn  = "warn"
	SeverityError = "error"
)

// Finding is the unit of output for `mib-ingest --report`. Wire
// shape is stable for the top-level fields; `Detail` is a
// category-specific map whose keys may evolve in future versions
// (additions non-breaking, removals/renames are breaking).
type Finding struct {
	Category   string         `json:"category"`
	Severity   string         `json:"severity"`
	ModuleName string         `json:"module_name"`
	Sources    []string       `json:"sources"`
	Detail     map[string]any `json:"detail"`
}

// findByteIdentical groups parsed results by sha256 and emits one
// `byte-identical` finding per group of size > 1. Each finding
// carries the shared hash, file size, and a `cross_directory`
// flag in detail. Severity is always `info` per spec — operators
// who want CI to fail on cross-archive duplication filter the JSON
// output for `detail.cross_directory == true`.
//
// Inputs with empty sha are skipped (unreadable files have no hash
// to dedup against).
func findByteIdentical(parsed []result, srcRoot string) []Finding {
	groups := make(map[string][]result, len(parsed))
	for _, r := range parsed {
		if r.sha == "" {
			continue
		}
		groups[r.sha] = append(groups[r.sha], r)
	}
	out := make([]Finding, 0)
	for sha, members := range groups {
		if len(members) < 2 {
			continue
		}
		sources := sourcesOf(members)
		out = append(out, Finding{
			Category: CategoryByteIdentical,
			Severity: SeverityInfo,
			// No module_name for byte-identical groups: members
			// may span any number of module names (typically one,
			// since identical bytes → identical Module.Name; but
			// the category is sha-keyed, not name-keyed).
			ModuleName: "",
			Sources:    sources,
			Detail: map[string]any{
				"hash":            sha,
				"size":            members[0].size,
				"cross_directory": crossDirectory(sources, srcRoot),
			},
		})
	}
	sortFindings(out)
	return out
}

// findModuleNameCollisions groups parsed results by Module.Name and
// emits a `module-name-collision` finding per group of size > 1,
// applying overlap suppression: if every member of a group shares
// one sha256, the byte-identical finding alone is informative and
// the module-name-collision is suppressed.
func findModuleNameCollisions(parsed []result) []Finding {
	groups := make(map[string][]result, len(parsed))
	for _, r := range parsed {
		if r.moduleName == "" {
			continue
		}
		groups[r.moduleName] = append(groups[r.moduleName], r)
	}
	out := make([]Finding, 0)
	for name, members := range groups {
		if len(members) < 2 {
			continue
		}
		// Overlap suppression: if every member shares a single
		// (non-empty) sha, the byte-identical finding covers the
		// same source set. Skip this finding to avoid double-
		// counting. Mixed-hash groups still emit.
		if allShareOneSha(members) {
			continue
		}
		candidates := make([]map[string]any, 0, len(members))
		for _, r := range members {
			candidates = append(candidates, map[string]any{
				"source":                  r.src,
				"size":                    r.size,
				"sha":                     r.sha,
				"last_updated_raw":        r.lastUpdated,
				"last_updated_normalised": normalizeLastUpdated(r.lastUpdated),
			})
		}
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i]["source"].(string) < candidates[j]["source"].(string)
		})
		out = append(out, Finding{
			Category:   CategoryModuleNameCollision,
			Severity:   SeverityWarn,
			ModuleName: name,
			Sources:    sourcesOf(members),
			Detail: map[string]any{
				"candidates": candidates,
			},
		})
	}
	sortFindings(out)
	return out
}

// findOIDArcSharing groups parsed results by Module.OIDRoot and
// emits an `oid-arc-sharing` finding when a single OIDRoot is
// claimed by > 1 distinct Module.Name. Groups keyed by empty
// OIDRoot are excluded — SMIv1 modules and TC-only modules cluster
// there and would produce one spurious mega-finding bundling every
// no-MODULE-IDENTITY file as if they all conflict.
func findOIDArcSharing(parsed []result) []Finding {
	groups := make(map[string][]result, len(parsed))
	for _, r := range parsed {
		if r.oidRoot == "" {
			continue
		}
		groups[r.oidRoot] = append(groups[r.oidRoot], r)
	}
	out := make([]Finding, 0)
	for oid, members := range groups {
		names := distinctModuleNames(members)
		if len(names) < 2 {
			continue
		}
		candidates := make([]map[string]any, 0, len(members))
		for _, r := range members {
			candidates = append(candidates, map[string]any{
				"module_name":             r.moduleName,
				"source":                  r.src,
				"last_updated_raw":        r.lastUpdated,
				"last_updated_normalised": normalizeLastUpdated(r.lastUpdated),
			})
		}
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i]["source"].(string) < candidates[j]["source"].(string)
		})
		out = append(out, Finding{
			Category: CategoryOIDArcSharing,
			Severity: SeverityWarn,
			// No single module_name for an oid-arc-sharing finding —
			// the OID is shared across multiple distinct names.
			ModuleName: "",
			Sources:    sourcesOf(members),
			Detail: map[string]any{
				"oid":          oid,
				"module_names": names,
				"candidates":   candidates,
			},
		})
	}
	sortFindings(out)
	return out
}

// findDivergentIdentity groups parsed results by
// (Module.Name, normalised LastUpdated) and emits an `error`-severity
// finding for any group whose members have differing sha256. Groups
// whose normalised LastUpdated is empty are skipped — too many
// false positives across legacy SMIv1 modules that legitimately
// differ between archives.
func findDivergentIdentity(parsed []result) []Finding {
	type key struct {
		name, lu string
	}
	groups := make(map[key][]result, len(parsed))
	for _, r := range parsed {
		if r.moduleName == "" {
			continue
		}
		lu := normalizeLastUpdated(r.lastUpdated)
		if lu == "" {
			continue
		}
		k := key{name: r.moduleName, lu: lu}
		groups[k] = append(groups[k], r)
	}
	out := make([]Finding, 0)
	for k, members := range groups {
		if len(members) < 2 {
			continue
		}
		if allShareOneSha(members) {
			continue // not divergent — they're byte-identical
		}
		candidates := make([]map[string]any, 0, len(members))
		for _, r := range members {
			candidates = append(candidates, map[string]any{
				"source": r.src,
				"sha":    r.sha,
				"size":   r.size,
			})
		}
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i]["source"].(string) < candidates[j]["source"].(string)
		})
		out = append(out, Finding{
			Category:   CategoryDivergentIdentity,
			Severity:   SeverityError,
			ModuleName: k.name,
			Sources:    sourcesOf(members),
			Detail: map[string]any{
				"last_updated": k.lu,
				"candidates":   candidates,
			},
		})
	}
	sortFindings(out)
	return out
}

// findBrokenAndNonMIB converts the parseErrors slice (the second
// return of classifyFiles) into `broken` and `non-mib` findings
// so the report surfaces every upload-folder leftover in one
// place. `outcomeParseError` → `broken` (severity `warn`, the
// file has the SMI marker but smidump rejected it); other
// outcomes that surface in parseErrors are non-MIB skips —
// unreadable files or files lacking the marker — and become
// `non-mib` (severity `info`).
//
// Each finding carries exactly one source. The reason string
// from the result is surfaced as `detail.reason`.
func findBrokenAndNonMIB(parseErrors []result) []Finding {
	out := make([]Finding, 0, len(parseErrors))
	for _, r := range parseErrors {
		var category, severity string
		switch r.outcome {
		case outcomeParseError:
			category = CategoryBroken
			severity = SeverityWarn
		case outcomeSkippedNonMIB:
			category = CategoryNonMIB
			severity = SeverityInfo
		default:
			continue
		}
		out = append(out, Finding{
			Category:   category,
			Severity:   severity,
			ModuleName: "",
			Sources:    []string{r.src},
			Detail: map[string]any{
				"reason": r.reason,
			},
		})
	}
	sortFindings(out)
	return out
}

// sourcesOf returns the source paths of `members` in lexicographic
// order — the stable ordering required for deterministic tests and
// reproducible jq pipelines.
func sourcesOf(members []result) []string {
	out := make([]string, 0, len(members))
	for _, r := range members {
		out = append(out, r.src)
	}
	sort.Strings(out)
	return out
}

// distinctModuleNames returns the sorted distinct module names in
// `members`. Excludes empty names (which shouldn't reach a parsed
// result but defensively guarded).
func distinctModuleNames(members []result) []string {
	seen := make(map[string]struct{}, len(members))
	for _, r := range members {
		if r.moduleName == "" {
			continue
		}
		seen[r.moduleName] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// allShareOneSha returns true when every member has the same
// non-empty sha. Used by overlap-suppression in
// findModuleNameCollisions and the byte-identical carve-out in
// findDivergentIdentity. Empty shas cause the function to return
// false (a member without a hash can't be confirmed to share one
// either).
func allShareOneSha(members []result) bool {
	if len(members) == 0 {
		return false
	}
	first := members[0].sha
	if first == "" {
		return false
	}
	for _, r := range members[1:] {
		if r.sha != first {
			return false
		}
	}
	return true
}

// crossDirectory returns true when the source paths span more than
// one distinct immediate-parent-under-srcRoot. Used by the
// byte-identical finding's `detail.cross_directory` flag so
// operators can filter the JSON output for cross-archive
// duplication (the high-value signal) vs same-archive duplication
// (typically just a vendor archive shipping the same file twice).
func crossDirectory(sources []string, srcRoot string) bool {
	if len(sources) < 2 {
		return false
	}
	first := immediateParentUnder(sources[0], srcRoot)
	for _, s := range sources[1:] {
		if immediateParentUnder(s, srcRoot) != first {
			return true
		}
	}
	return false
}

// immediateParentUnder returns the name of the immediate child of
// `srcRoot` on `src`'s ancestor chain. For `srcRoot=mibs/upload`
// and `src=mibs/upload/archive-a/foo.mib` it returns `archive-a`.
// For `src=mibs/upload/foo.mib` (file directly under srcRoot) it
// returns the empty string. Returns the empty string on any
// `filepath.Rel` failure (e.g. abs-vs-rel mismatch).
func immediateParentUnder(src, srcRoot string) string {
	rel, err := filepath.Rel(srcRoot, src)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) <= 1 {
		return ""
	}
	return parts[0]
}

// sortFindings orders findings deterministically: first by category,
// then by module_name, then by the lexicographically-first source
// path. Stable order across re-runs is required for golden tests
// and diffable output.
func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Category != findings[j].Category {
			return findings[i].Category < findings[j].Category
		}
		if findings[i].ModuleName != findings[j].ModuleName {
			return findings[i].ModuleName < findings[j].ModuleName
		}
		ai, aj := "", ""
		if len(findings[i].Sources) > 0 {
			ai = findings[i].Sources[0]
		}
		if len(findings[j].Sources) > 0 {
			aj = findings[j].Sources[0]
		}
		return ai < aj
	})
}

// collectReportFindings runs every grouping pass and returns the
// concatenated + sorted finding list. The corpus cross-check is
// gated on `LoadCorpusIndex` succeeding — when the index is missing
// or malformed, the cross-check yields no findings (per spec the
// absence does not affect the exit code on its own).
func collectReportFindings(srcDir, root string, parsed, parseErrors []result) []Finding {
	out := make([]Finding, 0)
	out = append(out, findByteIdentical(parsed, srcDir)...)
	out = append(out, findModuleNameCollisions(parsed)...)
	out = append(out, findOIDArcSharing(parsed)...)
	out = append(out, findDivergentIdentity(parsed)...)
	out = append(out, findCorpusCollisions(parsed, LoadCorpusIndex(root))...)
	out = append(out, findBrokenAndNonMIB(parseErrors)...)
	sortFindings(out)
	return out
}

// hasActionableFinding returns true when at least one finding has
// severity `warn` or `error`. Drives the report-mode exit code.
func hasActionableFinding(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityWarn || f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// renderReport dispatches to text or JSON output. Returns the
// errReportActionable sentinel when at least one finding has
// severity warn/error, so the caller propagates a non-zero exit.
func renderReport(w io.Writer, format string, findings []Finding) error {
	switch format {
	case "json":
		if err := renderReportJSON(w, findings); err != nil {
			return err
		}
	case "text":
		if err := renderReportText(w, findings); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown --report-format %q (must be text or json)", format)
	}
	if hasActionableFinding(findings) {
		return errReportActionable
	}
	return nil
}

// renderReportJSON emits findings as a flat top-level JSON array
// with two-space indent. Spec mandates `[]` (not `null`) for the
// empty case; we guarantee that by encoding a non-nil slice.
func renderReportJSON(w io.Writer, findings []Finding) error {
	if findings == nil {
		findings = []Finding{}
	}
	data, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

// renderReportText emits a human-readable per-category report.
// Each non-empty category becomes a section; sections with more
// than `textTruncationCap` findings show the first 50 then a
// trailing `... and N more (use --report-format=json for the
// full list)` line. Within each section, finding ordering is the
// sort order produced by `sortFindings` (category → module_name
// → first source) — already applied by `collectReportFindings`.
func renderReportText(w io.Writer, findings []Finding) error {
	if len(findings) == 0 {
		_, err := fmt.Fprintln(w, "no findings")
		return err
	}

	byCategory := make(map[string][]Finding)
	for _, f := range findings {
		byCategory[f.Category] = append(byCategory[f.Category], f)
	}

	// Stable category ordering — alphabetical matches the per-finding
	// sort key in sortFindings, so within a category sections nest
	// without re-ordering surprises.
	categories := []string{
		CategoryBroken,
		CategoryByteIdentical,
		CategoryCorpusCollision,
		CategoryDivergentIdentity,
		CategoryModuleNameCollision,
		CategoryNonMIB,
		CategoryOIDArcSharing,
	}
	for _, cat := range categories {
		section := byCategory[cat]
		if len(section) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "# %s (%d)\n", cat, len(section))
		limit := len(section)
		if limit > textTruncationCap {
			limit = textTruncationCap
		}
		for i := 0; i < limit; i++ {
			renderOneFindingText(w, section[i])
		}
		if len(section) > textTruncationCap {
			_, _ = fmt.Fprintf(w, "  ... and %d more (use --report-format=json for the full list)\n",
				len(section)-textTruncationCap)
		}
		_, _ = fmt.Fprintln(w)
	}
	return nil
}

// renderOneFindingText writes one finding to w with a per-category
// shape tuned for fast human triage. Lines stay under 120 columns
// for typical paths; pathological inputs may overflow but the
// trade-off is readability for normal cases.
func renderOneFindingText(w io.Writer, f Finding) {
	switch f.Category {
	case CategoryByteIdentical:
		_, _ = fmt.Fprintf(w, "  [%s] sha=%s size=%v cross_dir=%v\n",
			f.Severity, f.Detail["hash"], f.Detail["size"], f.Detail["cross_directory"])
		for _, s := range f.Sources {
			_, _ = fmt.Fprintf(w, "    - %s\n", s)
		}
	case CategoryModuleNameCollision:
		_, _ = fmt.Fprintf(w, "  [%s] %s\n", f.Severity, f.ModuleName)
		if candidates, ok := f.Detail["candidates"].([]map[string]any); ok {
			for _, c := range candidates {
				_, _ = fmt.Fprintf(w, "    - %s  last_updated=%s  sha=%s\n",
					c["source"], c["last_updated_normalised"], c["sha"])
			}
		}
	case CategoryOIDArcSharing:
		names, _ := f.Detail["module_names"].([]string)
		_, _ = fmt.Fprintf(w, "  [%s] oid=%s names=%s\n",
			f.Severity, f.Detail["oid"], strings.Join(names, ","))
		for _, s := range f.Sources {
			_, _ = fmt.Fprintf(w, "    - %s\n", s)
		}
	case CategoryDivergentIdentity:
		_, _ = fmt.Fprintf(w, "  [%s] %s last_updated=%s\n",
			f.Severity, f.ModuleName, f.Detail["last_updated"])
		for _, s := range f.Sources {
			_, _ = fmt.Fprintf(w, "    - %s\n", s)
		}
	case CategoryCorpusCollision:
		_, _ = fmt.Fprintf(w, "  [%s] %s label=%s upload=%s corpus=%s\n",
			f.Severity, f.ModuleName,
			f.Detail["label"],
			f.Detail["upload_last_updated"],
			f.Detail["corpus_last_updated"])
		for _, s := range f.Sources {
			_, _ = fmt.Fprintf(w, "    - %s\n", s)
		}
	case CategoryBroken, CategoryNonMIB:
		src := ""
		if len(f.Sources) > 0 {
			src = f.Sources[0]
		}
		_, _ = fmt.Fprintf(w, "  [%s] %s\n", f.Severity, src)
		if reason, ok := f.Detail["reason"]; ok {
			_, _ = fmt.Fprintf(w, "    reason: %v\n", reason)
		}
	default:
		_, _ = fmt.Fprintf(w, "  [%s] %s (%d source(s))\n",
			f.Severity, f.ModuleName, len(f.Sources))
	}
}
