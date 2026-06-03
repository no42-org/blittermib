/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

// Package textutil holds small, dependency-free string helpers shared
// across blittermib layers (e.g. the web renderer and the eventconf
// exporter) without coupling them to one another.
package textutil

import (
	"strings"
	"unicode"
)

// CollapseWhitespace replaces every run of whitespace with a single
// space and trims the result. SMI descriptions are typically wrapped
// to ~70 chars with hard newlines and indentation; collapsing unwraps
// them to a single flowing line — which also means callers that feed
// the result into XML chardata avoid encoding/xml escaping newlines,
// tabs, and carriage returns as &#xA;/&#x9;/&#xD;.
func CollapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}
