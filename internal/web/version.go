/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package web

// Version is the build version surfaced in the footer tooltip. Set
// once at startup by cmd/blittermib/main before any HTTP server
// starts; read-only thereafter, so no synchronization is needed for
// render-time reads. Tests that need a non-"dev" value assign it
// directly in their fixture before constructing a Server — within a
// test package go test runs sequentially, so the write is race-free.
var Version = "dev"
