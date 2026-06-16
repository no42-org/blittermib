/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package correlate

import (
	"fmt"
	"strings"
)

// Coverage is the distribution of inference outcomes across one or more
// modules — the corpus report's accounting (FR26). Every notification is
// counted under exactly one class and one confidence band, so the totals
// reconcile and nothing is silently dropped.
type Coverage struct {
	Modules       int
	Notifications int
	ByClass       map[Classification]int
	ByConfidence  map[Confidence]int
	Pairs         int // total clear→raise edges
}

// NewCoverage returns a zeroed Coverage with its maps initialised.
func NewCoverage() Coverage {
	return Coverage{
		ByClass:      make(map[Classification]int),
		ByConfidence: make(map[Confidence]int),
	}
}

// AddModule folds one module's relationships into the coverage tally.
// The maps are lazily initialised so a zero-value Coverage is usable
// (NewCoverage remains the documented constructor).
func (c *Coverage) AddModule(rels []Relationship) {
	if c.ByClass == nil {
		c.ByClass = make(map[Classification]int)
	}
	if c.ByConfidence == nil {
		c.ByConfidence = make(map[Confidence]int)
	}
	c.Modules++
	for _, r := range rels {
		c.Notifications++
		c.ByClass[r.Class]++
		c.ByConfidence[r.Confidence]++
		if r.Class == ClassClear {
			c.Pairs += len(r.Clears)
		}
	}
}

// classOrder / confidenceOrder fix the report's row order so output is
// deterministic regardless of map iteration.
var classOrder = []Classification{ClassRaise, ClassClear, ClassOrphan}
var confidenceOrder = []Confidence{ConfHigh, ConfLikely, ConfGuess}

// String renders the coverage as a stable, human-readable report.
func (c Coverage) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "modules:        %d\n", c.Modules)
	fmt.Fprintf(&b, "notifications:  %d\n", c.Notifications)
	fmt.Fprintf(&b, "clear→raise edges: %d\n", c.Pairs)
	b.WriteString("by classification:\n")
	for _, k := range classOrder {
		fmt.Fprintf(&b, "  %-7s %d\n", k, c.ByClass[k])
	}
	b.WriteString("by confidence:\n")
	for _, k := range confidenceOrder {
		fmt.Fprintf(&b, "  %-7s %d\n", k, c.ByConfidence[k])
	}
	return b.String()
}
