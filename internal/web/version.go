/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package web

// Version is the build version surfaced in the footer tooltip. The
// server wires it from cmd/blittermib's linker-set version at
// startup; it stays "dev" for unset builds and tests.
var Version = "dev"
