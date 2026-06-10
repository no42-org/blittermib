/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package web

// walkEnabled gates the walk-overlay client asset in the base layout.
// Set once at startup via SetWalkEnabled from cmd/blittermib/main,
// before any HTTP server starts; read-only thereafter, so render-time
// reads need no synchronization. Mirrors the version global. Unexported
// so handlers can't flip it at request time.
var walkEnabled = false

// SetWalkEnabled records whether the walk decoder is live so the base
// layout includes or omits walk-overlay.js. Call once from main before
// any goroutine that can render templates is started; tests pin it from
// TestMain.
func SetWalkEnabled(v bool) {
	walkEnabled = v
}
