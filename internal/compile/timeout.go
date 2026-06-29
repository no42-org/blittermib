/*
 * Copyright 2026 Ronny Trommer <ronny@no42.org>
 * SPDX-License-Identifier: MIT
 */

package compile

import (
	"log/slog"
	"os"
	"time"
)

// defaultTimeoutFloor is the hang-backstop floor used when
// BLITTERMIB_COMPILE_TIMEOUT is unset/invalid — the value the compile
// bound has always defaulted to.
const defaultTimeoutFloor = 5 * time.Minute

// timeoutFloor is the effective hang-backstop floor, resolved ONCE from
// BLITTERMIB_COMPILE_TIMEOUT at package init (env config is static for
// the process lifetime; no need to re-read it per compile pass).
var timeoutFloor = parseTimeoutFloor(os.Getenv("BLITTERMIB_COMPILE_TIMEOUT"))

// parseTimeoutFloor resolves the compile-timeout floor from a raw
// BLITTERMIB_COMPILE_TIMEOUT value. The floor defaults to 5 m and is
// overridable UPWARD (a Go duration like "20m") so very large single-file
// MIBs — e.g. METASWITCH-MIB, ~92k objects, ~10 min in smidump — can be
// imported without raising the bound for everyone.
//
// Lowering below the default is rejected: it only invites false compile
// timeouts (and the quarantine churn that follows), with no real use
// case — a sub-default value is logged and ignored. Empty, unparseable,
// or non-positive values also keep the default. It reads no environment
// itself (the caller passes the raw value), so it is directly
// unit-testable; its only side effect is the warning on a rejected
// sub-default value.
func parseTimeoutFloor(v string) time.Duration {
	if v == "" {
		return defaultTimeoutFloor
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return defaultTimeoutFloor
	}
	if d < defaultTimeoutFloor {
		slog.Warn("compile: BLITTERMIB_COMPILE_TIMEOUT is below the default floor; ignoring it to avoid false compile timeouts",
			"requested", d, "using", defaultTimeoutFloor)
		return defaultTimeoutFloor
	}
	return d
}

// ScaledTimeout is the compile hang backstop: 1 s/file scaling with the
// configured floor (BLITTERMIB_COMPILE_TIMEOUT), never a throughput
// ceiling. Both the import engine and the mib-ingest CLI call it, so the
// bound — and the env override — stay identical across entry points.
func ScaledTimeout(n int) time.Duration {
	if d := time.Duration(n) * time.Second; d > timeoutFloor {
		return d
	}
	return timeoutFloor
}
