/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package server

import (
	"os"
	"testing"

	"github.com/no42-org/blittermib/internal/web"
)

// TestMain sets the build version that the footer tooltip reads,
// before any test runs. In production main does this once at
// startup; the tests don't go through main, so we pin it here so
// golden HTML stays deterministic.
func TestMain(m *testing.M) {
	web.SetVersion("test")
	// Production main sets this from srv.WalkEnabled(); the golden HTML
	// fixtures were captured with the walk decoder on, so pin it here.
	web.SetWalkEnabled(true)
	os.Exit(m.Run())
}
