/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"path/filepath"
	"sort"
	"strings"
)

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
