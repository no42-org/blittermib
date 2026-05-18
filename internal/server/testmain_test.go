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

// TestMain sets the package-level web.Version that the footer
// tooltip reads, before any test runs. In production main does this
// once at startup; the tests don't go through main, so we pin it
// here so golden HTML stays deterministic.
func TestMain(m *testing.M) {
	web.Version = "test"
	os.Exit(m.Run())
}
