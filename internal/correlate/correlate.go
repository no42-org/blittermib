/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

// Package correlate infers raise/clear/orphan relationships between a
// module's SNMP notifications from already-compiled symbols and
// references.
//
// It is a PURE package: no database, no IO, no globals, no time or
// randomness. Classify is a deterministic function of its inputs — the
// same module yields byte-identical, stably-ordered output every run —
// so the inference can be golden-tested without a store, and the store
// can rebuild the derived tables on every ingest without surprises.
//
// The four MVP signals (name-token, varbind-signature,
// DESCRIPTION-prose, NOTIFICATION-GROUP/OID-sibling) land in later
// stories; this scaffold establishes the vocabulary constants, the
// Evidence contract, and the Classify entry point.
package correlate

import "github.com/no42-org/blittermib/internal/model"

// Classification is the per-notification verdict. The string values
// are load-bearing: they are persisted verbatim and map directly to
// the OpenNMS alarm-type (raise=1, clear=2, orphan=3).
type Classification string

const (
	ClassRaise  Classification = "raise"
	ClassClear  Classification = "clear"
	ClassOrphan Classification = "orphan"
)

// Confidence is the inference confidence band. Cutoffs are calibrated
// (Story 1.6) so High meets the precision target; only High-confidence
// relationships are eligible for alarm-data export.
type Confidence string

const (
	ConfHigh   Confidence = "high"
	ConfLikely Confidence = "likely"
	ConfGuess  Confidence = "guess"
)

// SignalKind names one of the four independent inference signals.
type SignalKind string

const (
	SignalName        SignalKind = "name"
	SignalVarbind     SignalKind = "varbind"
	SignalDescription SignalKind = "description"
	SignalGroup       SignalKind = "group"
)

// Relationship is the inference result for one notification. Clears
// lists the raise notification(s) this notification resolves; it is
// empty unless Class == ClassClear.
type Relationship struct {
	Notification string
	Class        Classification
	Confidence   Confidence
	Evidence     Evidence
	Clears       []string
}

// Classify infers relationships for every NOTIFICATION-TYPE/TRAP-TYPE
// in syms, using refs for the varbind and group signals.
//
// Contract: deterministic (identical input → identical, stably-ordered
// output), never panics on malformed input, and never returns an error
// — inference is best-effort enrichment, never a gate on ingest.
//
// Scaffold: returns no relationships yet. The signals are implemented
// in Stories 1.2–1.5.
func Classify(syms []model.Symbol, refs []model.Reference) []Relationship {
	return nil
}
