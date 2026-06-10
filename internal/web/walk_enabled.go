/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package web

import "context"

// walkPageCtxKey marks a render as the standalone /walk intake page so
// the shared layout suppresses the topbar decode control + modal — that
// page already shows the intake form, and opening an empty modal over a
// half-filled page form is confusing.
type walkPageCtxKey struct{}

// WithWalkPage returns ctx marked as rendering the /walk intake page.
// The handler for GET /walk wraps its request context with this.
func WithWalkPage(ctx context.Context) context.Context {
	return context.WithValue(ctx, walkPageCtxKey{}, true)
}

// isWalkPage reports whether ctx was marked by WithWalkPage. Read from
// base.templ to drop the redundant topbar control + modal on /walk.
func isWalkPage(ctx context.Context) bool {
	v, _ := ctx.Value(walkPageCtxKey{}).(bool)
	return v
}

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
