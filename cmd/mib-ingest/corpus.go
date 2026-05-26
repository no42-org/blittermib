/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// LoadCorpusIndex reads `<root>/mibs/INDEX.yaml` and returns a
// `module name → raw LAST-UPDATED` map covering every entry in
// the index. On any failure (missing file, unreadable, malformed
// YAML, unexpected schema), the function logs ONE warning line to
// stderr and returns nil — the caller then skips the corpus
// cross-check entirely per the spec's "absent/malformed INDEX.yaml
// is a soft failure" contract.
//
// The raw LAST-UPDATED is returned unmodified (SMIv2 12-digit form
// or SMIv1 10-digit form, or empty when the corpus module has no
// MODULE-IDENTITY). Comparison-time normalisation is the caller's
// responsibility via `normalizeLastUpdated`.
func LoadCorpusIndex(root string) map[string]string {
	path := filepath.Join(root, "mibs", "INDEX.yaml")
	// #nosec G304 -- path is filepath.Join under the operator-supplied --root.
	data, err := os.ReadFile(path)
	if err != nil {
		// Distinguish "no corpus on disk" (an entirely reasonable
		// operator configuration — out-of-tree workflow, fresh
		// checkout pre-`make index`) from "corpus exists but I/O
		// failed" (permission denied, EISDIR, transient FS error).
		// The former is benign and operators shouldn't have to
		// chase it; the latter is actionable.
		if os.IsNotExist(err) {
			slog.Warn("corpus cross-check skipped: INDEX.yaml not found", "path", path)
		} else {
			slog.Warn("corpus cross-check skipped: INDEX.yaml unreadable", "path", path, "err", err)
		}
		return nil
	}
	var parsed struct {
		Mibs []struct {
			Module      string `yaml:"module"`
			LastUpdated string `yaml:"last_updated"`
		} `yaml:"mibs"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		slog.Warn("corpus cross-check skipped: INDEX.yaml malformed", "path", path, "err", err)
		return nil
	}
	out := make(map[string]string, len(parsed.Mibs))
	for _, e := range parsed.Mibs {
		if e.Module == "" {
			continue
		}
		out[e.Module] = e.LastUpdated
	}
	return out
}

// findCorpusCollisions matches each parsed upload entry's
// Module.Name against the loaded corpus index and emits one
// `corpus-collision` finding per match. The label is one of
// `corpus-newer`, `corpus-older`, `same-revision`, or `unknown`,
// determined by comparing normalised LAST-UPDATED values.
//
// Returns an empty slice when corpusIndex is nil (load failed) —
// the spec requires that an absent corpus check NOT cause a
// non-zero exit on its own.
func findCorpusCollisions(parsed []result, corpusIndex map[string]string) []Finding {
	if corpusIndex == nil {
		return []Finding{}
	}
	out := make([]Finding, 0)
	for _, r := range parsed {
		if r.moduleName == "" {
			continue
		}
		corpusRaw, ok := corpusIndex[r.moduleName]
		if !ok {
			continue
		}
		uploadNorm := normalizeLastUpdated(r.lastUpdated)
		corpusNorm := normalizeLastUpdated(corpusRaw)
		label, severity := corpusCollisionLabel(uploadNorm, corpusNorm)
		out = append(out, Finding{
			Category:   CategoryCorpusCollision,
			Severity:   severity,
			ModuleName: r.moduleName,
			Sources:    []string{r.src},
			Detail: map[string]any{
				"label":                   label,
				"upload_last_updated_raw": r.lastUpdated,
				"upload_last_updated":     uploadNorm,
				"corpus_last_updated_raw": corpusRaw,
				"corpus_last_updated":     corpusNorm,
			},
		})
	}
	sortFindings(out)
	return out
}

// corpusCollisionLabel returns the label + severity for a
// corpus-vs-upload LAST-UPDATED comparison. Both inputs are
// expected to be normalised (4-digit-year ISO-8601 form, or empty).
//
//   - Either side empty → `unknown` / `warn` (relative age
//     undetermined, but the collision itself is real and the
//     operator should be aware).
//   - Equal non-empty → `same-revision` / `info`.
//   - Upload < corpus → `corpus-newer` / `warn`.
//   - Upload > corpus → `corpus-older` / `warn`.
//
// Lexical comparison is correct because normalised values share a
// canonical fixed-width form.
func corpusCollisionLabel(uploadNorm, corpusNorm string) (label, severity string) {
	if uploadNorm == "" || corpusNorm == "" {
		return "unknown", SeverityWarn
	}
	if uploadNorm == corpusNorm {
		return "same-revision", SeverityInfo
	}
	if uploadNorm < corpusNorm {
		return "corpus-newer", SeverityWarn
	}
	return "corpus-older", SeverityWarn
}
