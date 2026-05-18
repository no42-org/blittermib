/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package web

// version is the build version surfaced in the footer tooltip. Set
// once at startup via SetVersion from cmd/blittermib/main, before
// any HTTP server starts; read-only thereafter, so no synchronization
// is needed for render-time reads. Unexported so handlers can't
// mutate it at request time and silently re-open the race.
var version = "dev"

// SetVersion records the build version for the footer tooltip. Call
// once from main before any goroutine that can render templates is
// started. Tests do the same from TestMain.
func SetVersion(v string) {
	version = v
}
