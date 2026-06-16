/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package correlate

// Confidence scoring (Story 1.5).
//
// This file owns the scoring STRUCTURE — how the four signals and the
// shape of a pairing map to a confidence band. The threshold VALUES are
// intentionally conservative defaults here; Story 1.6 calibrates them
// against the bundled standard corpus so that the High band meets its
// measured precision target (FR10). Keeping the policy in one place is
// what makes that calibration a localized change.

// Band thresholds. Named (not inline literals) so Story 1.6 can
// calibrate them against the bundled corpus without hunting through the
// scoring logic (architecture D3).
const (
	// highMinSignals: signal count that earns High on its own.
	// name+varbind also earns High at two signals (strong, independent).
	highMinSignals = 3
	// likelyMinSignals: signal count that earns Likely. A bare
	// name-only match (one signal, no structural corroboration) stays
	// Guess by design.
	likelyMinSignals = 2
	// maxFanOutForHigh: a notification paired with more than this many
	// partners is a one-to-many match — too uncertain to report High.
	maxFanOutForHigh = 1
)

// signalSet records which of the four independent signals fired for a
// pairing.
type signalSet struct {
	name    bool
	varbind bool
	desc    bool
	group   bool
}

func (s signalSet) count() int {
	n := 0
	for _, b := range []bool{s.name, s.varbind, s.desc, s.group} {
		if b {
			n++
		}
	}
	return n
}

// scoreConfidence maps the signals that paired a notification, how many
// partners it ended up with (fanOut), and whether its own name and
// description directions disagree (conflict), to a confidence band.
//
// Base band by signal strength, then two uncertainty caps: a name that
// contradicts its description, or a one-to-many pairing, is never
// reported as High — both are real sources of doubt that the raw signal
// count would otherwise hide.
func scoreConfidence(s signalSet, fanOut int, conflict bool) Confidence {
	band := ConfGuess
	switch {
	case (s.name && s.varbind) || s.count() >= highMinSignals:
		band = ConfHigh
	case s.count() >= likelyMinSignals:
		band = ConfLikely
	}
	if band == ConfHigh && (conflict || fanOut > maxFanOutForHigh) {
		band = ConfLikely
	}
	return band
}
