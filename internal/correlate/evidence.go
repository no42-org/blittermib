/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package correlate

import "strings"

// SignalHit records one signal that fired during inference, with a
// human-readable detail (e.g. "shared varbind ifIndex").
type SignalHit struct {
	Kind   SignalKind `json:"kind"`
	Detail string     `json:"detail"`
}

// Evidence is the trail behind a single inference. It is the single
// source rendered to BOTH the UI popover and the eventconf export
// provenance comment, so the two can never diverge. Summary is a
// one-line synthesis derived from Signals.
//
// Signals are kept in a fixed kind order (name, varbind, description,
// group) by the classifier so serialized evidence is deterministic.
type Evidence struct {
	Signals []SignalHit `json:"signals"`
	Summary string      `json:"summary"`
}

// String renders the evidence as a one-line human explanation —
// "summary (signal-detail; signal-detail)". It is the single source
// behind both the UI's evidence popover and the eventconf export's
// provenance comment, so the two never diverge (D4).
func (e Evidence) String() string {
	if len(e.Signals) == 0 {
		return e.Summary
	}
	details := make([]string, 0, len(e.Signals))
	for _, s := range e.Signals {
		details = append(details, s.Detail)
	}
	return e.Summary + " (" + strings.Join(details, "; ") + ")"
}
