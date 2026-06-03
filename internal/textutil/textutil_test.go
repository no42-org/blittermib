/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package textutil

import "testing"

func TestCollapseWhitespace(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "abc", "abc"},
		{"multi-space run", "a    b", "a b"},
		{"newline plus indentation", "acting in\n          an agent role", "acting in an agent role"},
		{"tabs", "a\t\tb", "a b"},
		{"carriage returns", "a\r\nb", "a b"},
		{"mixed run", "a \t\r\n  b", "a b"},
		{"leading and trailing", "  \n hello \n  ", "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CollapseWhitespace(tt.in); got != tt.want {
				t.Errorf("CollapseWhitespace(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
