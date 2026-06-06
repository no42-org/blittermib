/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package mibimport

import (
	"errors"
	"os/exec"
	"syscall"
)

// isCrossDevice reports a rename(2) that crossed filesystems —
// os.Rename wraps the syscall error in *os.LinkError, which
// errors.Is unwraps.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// isSignalKilled reports a child terminated by a signal (ExitCode
// -1) — the shape exec.CommandContext's kill leaves when the compile
// bound fires mid-run (Wait prefers the child's ExitError over the
// context error; verified empirically in the ingest-compile-timeout
// change).
func isSignalKilled(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == -1
}
