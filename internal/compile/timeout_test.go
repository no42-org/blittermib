/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package compile

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestParseTimeoutFloor covers BLITTERMIB_COMPILE_TIMEOUT resolution:
// the floor is raise-only, and empty / sub-default / unparseable /
// non-positive values fall back to defaultTimeoutFloor.
func TestParseTimeoutFloor(t *testing.T) {
	// The sub-default case intentionally triggers parseTimeoutFloor's
	// warning; discard logs so `go test` output stays clean.
	saved := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(saved) })

	cases := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"empty -> default", "", defaultTimeoutFloor},
		{"raise floor", "20m", 20 * time.Minute},
		{"exactly default -> default", "5m", defaultTimeoutFloor},
		{"sub-default -> default (warns)", "30s", defaultTimeoutFloor},
		{"unparseable -> default", "garbage", defaultTimeoutFloor},
		{"zero -> default", "0s", defaultTimeoutFloor},
		{"negative -> default", "-5m", defaultTimeoutFloor},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseTimeoutFloor(c.in); got != c.want {
				t.Errorf("parseTimeoutFloor(%q) = %s, want %s", c.in, got, c.want)
			}
		})
	}
}

// TestScaledTimeout covers the 1 s/file scaling against the floor.
// timeoutFloor is pinned per case (and restored) so the test is hermetic
// regardless of BLITTERMIB_COMPILE_TIMEOUT in the environment.
func TestScaledTimeout(t *testing.T) {
	saved := timeoutFloor
	t.Cleanup(func() { timeoutFloor = saved })

	cases := []struct {
		name  string
		floor time.Duration
		n     int
		want  time.Duration
	}{
		{"default floor, empty batch -> floor", defaultTimeoutFloor, 0, defaultTimeoutFloor},
		{"default floor, small batch -> floor", defaultTimeoutFloor, 1, defaultTimeoutFloor},
		{"default floor, just below boundary -> floor", defaultTimeoutFloor, 299, defaultTimeoutFloor},
		{"default floor, exactly at boundary -> floor", defaultTimeoutFloor, 300, defaultTimeoutFloor},
		{"default floor, just past boundary -> scaling wins", defaultTimeoutFloor, 301, 301 * time.Second},
		{"default floor, large batch -> scaling wins", defaultTimeoutFloor, 1000, 1000 * time.Second},
		{"raised floor wins over smaller scaling", 20 * time.Minute, 1000, 20 * time.Minute},
		{"raised floor, larger scaling wins", 20 * time.Minute, 2000, 2000 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			timeoutFloor = c.floor
			if got := ScaledTimeout(c.n); got != c.want {
				t.Errorf("ScaledTimeout(%d) with floor %s = %s, want %s", c.n, c.floor, got, c.want)
			}
		})
	}
}
