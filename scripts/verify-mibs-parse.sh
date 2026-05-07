#!/usr/bin/env bash
# Tier 3 — Parser: every MIB must produce non-empty XML from
# `smidump -f xml -k` and zero errors from `smilint`. Warnings
# are tolerated. (smidump has no `--strict` flag in upstream
# libsmi; the strict-error gate is enforced by smilint below.)
#
# The smidump search path is built recursively from the corpus root
# (sorted via `LC_ALL=C sort` so libsmi resolves IMPORTS
# deterministically — first-match semantics across duplicate module
# definitions in different vendor dirs).
#
# v1.0: no SHA cache. Cache infrastructure lands in a follow-up
# (task §6.7 + §7.5) once the corpus is large enough that the
# uncached run-time becomes a concern. For a corpus of ~hundreds of
# MIBs this runs in well under a minute.

set -euo pipefail

ROOT="${MIBS_ROOT:-mibs}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-mib-walk.sh
source "$SCRIPT_DIR/lib-mib-walk.sh"

if [ ! -d "$ROOT" ]; then
    echo "verify-mibs-parse: $ROOT does not exist; nothing to verify"
    exit 0
fi

if ! command -v smidump >/dev/null 2>&1; then
    echo "verify-mibs-parse: smidump not on PATH (install libsmi)" >&2
    exit 1
fi
if ! command -v smilint >/dev/null 2>&1; then
    echo "verify-mibs-parse: smilint not on PATH (install libsmi)" >&2
    exit 1
fi

# Build smidump/smilint -p arguments. `-prune` (rather than a
# substring `! -path '*/LICENSES*'` filter) matches directories
# whose basename is exactly `.something` or `LICENSES`, not just
# anything containing the string. The resulting list is sorted so
# IMPORTS resolution is reproducible across runs.
SMI_PATHS=()
while IFS= read -r d; do
    SMI_PATHS+=(-p "$d")
done < <(find "$ROOT" \( -name '.*' -o -name 'LICENSES' \) -prune -o -type d -print | LC_ALL=C sort)

fail=0
checked=0
xml_out="$(mktemp)"
err_out="$(mktemp)"
trap 'rm -f "$xml_out" "$err_out"' EXIT

while IFS= read -r f; do
    checked=$((checked + 1))

    # smidump: success + non-empty XML on stdout. Capture stdout
    # and stderr separately so the empty-XML check operates on the
    # actual XML body (not on stderr diagnostics that a `2>&1`
    # merge would conflate).
    if ! smidump -f xml -k "${SMI_PATHS[@]}" "$f" >"$xml_out" 2>"$err_out"; then
        echo "FAIL [smidump $f]" >&2
        sed 's/^/  /' < "$err_out" >&2
        fail=1
        continue
    fi
    if [ ! -s "$xml_out" ]; then
        echo "FAIL [smidump empty XML]: $f" >&2
        sed 's/^/  /' < "$err_out" >&2
        fail=1
        continue
    fi

    # smilint: zero errors. libsmi's smilint exit code is non-zero
    # only on genuine errors (warnings keep exit 0). The `-m` flag
    # was previously passed but means "print module name with
    # diagnostics" — adds noise without changing the gate.
    if ! smilint_out="$(smilint "${SMI_PATHS[@]}" "$f" 2>&1)"; then
        echo "FAIL [smilint $f]" >&2
        echo "$smilint_out" | sed 's/^/  /' >&2
        fail=1
    fi
done < <(walk_mib_files "$ROOT")

if [ $fail -ne 0 ]; then
    echo "verify-mibs-parse: FAILED ($checked files checked)" >&2
    exit 1
fi
echo "verify-mibs-parse: OK ($checked files checked)"
