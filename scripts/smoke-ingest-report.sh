#!/usr/bin/env bash
# Copyright 2026 Ronny Trommer <ronny@no42.org>
# SPDX-License-Identifier: MIT
#
# End-to-end CLI smoke tests for `mib-ingest --report` and
# `--auto-collapse-identical`. Each scenario mirrors a verification
# task from openspec/changes/ingest-triage-report/tasks.md Group 8.
#
# The script synthesises fixtures in a TMPDIR, runs the binary,
# asserts the expected behaviour (exit code + key output
# substrings), and reports pass/fail per scenario. Exits non-zero
# if any scenario fails.
#
# Usage:
#   ./scripts/smoke-ingest-report.sh

set -u
set -o pipefail

# Pin locale so sort/grep are byte-deterministic across hosts.
export LC_ALL=C

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Preflight: required external tools. `go` and `jq` are mandatory;
# the sha256 tool is auto-resolved (sha256sum on Linux, shasum -a 256
# on macOS).
for tool in go jq; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "smoke-ingest-report: required tool '$tool' not found on PATH" >&2
        exit 2
    fi
done
if command -v sha256sum >/dev/null 2>&1; then
    SHA256=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
    SHA256=(shasum -a 256)
else
    echo "smoke-ingest-report: need sha256sum or shasum on PATH" >&2
    exit 2
fi

# Preflight: required corpus fixture. Several scenarios copy this
# file as a known-good MIB.
if [ ! -f mibs/ietf/core/SNMPv2-SMI ]; then
    echo "smoke-ingest-report: required fixture mibs/ietf/core/SNMPv2-SMI not found" >&2
    exit 2
fi

TMPROOT="$(mktemp -d)"
trap 'rm -rf "$TMPROOT"' EXIT

PASS=0
FAIL=0
FAILURES=()

# Minimal SMIv2 MODULE-IDENTITY body — caller substitutes
# {{MODULE_NAME}} and {{LAST_UPDATED}} via shell interpolation.
smi_v2_with_module_identity() {
    local module="$1" last_updated="$2" enterprise_n="${3:-99999}"
    cat <<EOF
$module DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, enterprises
        FROM SNMPv2-SMI;

theModule MODULE-IDENTITY
    LAST-UPDATED "$last_updated"
    ORGANIZATION "no42"
    CONTACT-INFO "noc"
    DESCRIPTION  "smoke fixture"
    ::= { enterprises $enterprise_n }

END
EOF
}

# Minimal SMIv1 module (no MODULE-IDENTITY clause — bare opener +
# closer, used for the empty-OIDRoot scenario).
smi_v1_no_module_identity() {
    local module="$1"
    cat <<EOF
$module DEFINITIONS ::= BEGIN
END
EOF
}

# A scenario helper. Usage:
#   scenario "N.M: title"
#   ... commands that set OK=true/false ...
#   record
SCENARIO=""
OK=""
scenario() {
    SCENARIO="$1"
    OK="true"
}
fail() {
    OK="false"
    FAILURES+=("$SCENARIO: $1")
}
record() {
    if [ "$OK" = "true" ]; then
        PASS=$((PASS + 1))
        printf '  ✓ %s\n' "$SCENARIO"
    else
        FAIL=$((FAIL + 1))
        printf '  ✗ %s\n' "$SCENARIO"
    fi
}

#-------------------------------------------------------------
# 8.1 — `make verify` passes locally.
#-------------------------------------------------------------
scenario "8.1: make verify passes locally"
if make verify >/dev/null 2>&1; then
    :
else
    fail "make verify returned non-zero"
fi
record

#-------------------------------------------------------------
# 8.2 — Two byte-identical SNMPv2-SMI files in separate subdirs
#        → one byte-identical info finding with
#        detail.cross_directory: true. Exit 0.
#-------------------------------------------------------------
scenario "8.2: byte-identical in separate subdirs → info, cross_directory=true"
SRC="$TMPROOT/8.2/upload"
mkdir -p "$SRC/archive-a" "$SRC/archive-b"
# Use a real SNMPv2-SMI from the corpus so smidump can parse.
cp mibs/ietf/core/SNMPv2-SMI "$SRC/archive-a/SNMPv2-SMI"
cp mibs/ietf/core/SNMPv2-SMI "$SRC/archive-b/SNMPv2-SMI"
# Capture stdout and exit-code on separate lines for clarity —
# the original `&& code=$? || code=$?` chain works but obscures
# intent and gets read wrong (it captures go-run's exit because
# the assignment-command propagates the substitution's status,
# but reviewers expect explicit capture).
out=$(go run ./cmd/mib-ingest --report --src "$SRC" --root "$TMPROOT/8.2" 2>/dev/null)
code=$?
if [ $code -ne 0 ]; then fail "exit code $code, want 0"; fi
printf '%s\n' "$out" | grep -q '# byte-identical (1)' || fail "no byte-identical section"
printf '%s\n' "$out" | grep -q 'cross_dir=true' || fail "cross_dir flag != true"
record

#-------------------------------------------------------------
# 8.3 — Two CISCO-FOO-MIB with different LAST-UPDATED →
#        module-name-collision finding. Exit non-zero.
#-------------------------------------------------------------
scenario "8.3: module-name-collision on different LAST-UPDATED → warn, exit !=0"
SRC="$TMPROOT/8.3/upload"
mkdir -p "$SRC"
smi_v2_with_module_identity "CISCO-FOO-MIB" "201803191200Z" 9 > "$SRC/CISCO-FOO-MIB-old.mib"
smi_v2_with_module_identity "CISCO-FOO-MIB" "202205101200Z" 9 > "$SRC/CISCO-FOO-MIB-new.mib"
out=$(go run ./cmd/mib-ingest --report --src "$SRC" --root "$TMPROOT/8.3" 2>/dev/null)
code=$?
if [ $code -eq 0 ]; then fail "exit code 0, want non-zero (warn finding)"; fi
printf '%s\n' "$out" | grep -q '# module-name-collision' || fail "no module-name-collision section"
printf '%s\n' "$out" | grep -q 'CISCO-FOO-MIB' || fail "module name not rendered"
printf '%s\n' "$out" | grep -q '201803191200Z' || fail "old last_updated absent from candidates"
printf '%s\n' "$out" | grep -q '202205101200Z' || fail "new last_updated absent from candidates"
record

#-------------------------------------------------------------
# 8.4 — One SMIv1 MIB missing MODULE-IDENTITY →
#        no oid-arc-sharing finding (empty-OIDRoot excluded).
#-------------------------------------------------------------
scenario "8.4: SMIv1 without MODULE-IDENTITY → no spurious oid-arc-sharing"
SRC="$TMPROOT/8.4/upload"
mkdir -p "$SRC"
smi_v1_no_module_identity "FOO-TC" > "$SRC/FOO-TC"
out=$(go run ./cmd/mib-ingest --report --src "$SRC" --root "$TMPROOT/8.4" 2>/dev/null) || true
if echo "$out" | grep -q '# oid-arc-sharing'; then
    fail "oid-arc-sharing section present (should be suppressed for empty OIDRoot)"
fi
record

#-------------------------------------------------------------
# 8.5 — Stale/missing INDEX.yaml → single WARN line to stderr,
#        cross-check skipped, missing-index alone doesn't
#        cause non-zero exit.
#-------------------------------------------------------------
scenario "8.5: missing INDEX.yaml → stderr warn, cross-check skipped, info-only exit 0"
SRC="$TMPROOT/8.5/upload"
ROOT="$TMPROOT/8.5"
mkdir -p "$SRC"
# Single MIB that parses cleanly and produces no warn/error
# findings → only finding source should be the parsing itself.
# Use a real SNMPv2-SMI copy so the only finding will be...
# actually a single isolated file produces no findings at all
# (no group of size > 1, no corpus to collide with).
cp mibs/ietf/core/SNMPv2-SMI "$SRC/SNMPv2-SMI"
# Deliberately omit ROOT/mibs/INDEX.yaml.
stderr_file="$TMPROOT/8.5/stderr.log"
go run ./cmd/mib-ingest --report --src "$SRC" --root "$ROOT" >/dev/null 2>"$stderr_file"
code=$?
if [ $code -ne 0 ]; then fail "exit code $code, want 0 (no findings)"; fi
if ! grep -q 'INDEX.yaml not found' "$stderr_file"; then
    fail "stderr missing 'INDEX.yaml not found' warn line"
fi
# Sanity: the warn line should appear exactly once
warn_count=$(grep -c 'INDEX.yaml not found' "$stderr_file")
if [ "$warn_count" != "1" ]; then
    fail "warn line emitted $warn_count times, want 1"
fi
record

#-------------------------------------------------------------
# 8.6 — JSON output → jq '.[] | .category' produces a flat list
#        from the documented seven-category set.
#-------------------------------------------------------------
scenario "8.6: jq category round-trip yields documented categories only"
SRC="$TMPROOT/8.6/upload"
mkdir -p "$SRC"
# Fixture: two byte-identical copies → guarantees at least one
# 'byte-identical' category in the output.
cp mibs/ietf/core/SNMPv2-SMI "$SRC/SNMPv2-SMI-a"
cp mibs/ietf/core/SNMPv2-SMI "$SRC/SNMPv2-SMI-b"
out=$(go run ./cmd/mib-ingest --report --report-format=json --src "$SRC" --root "$TMPROOT/8.6" 2>/dev/null) || true
# Round-trip through jq. The output must be a non-empty list of
# strings drawn from the documented set.
categories=$(echo "$out" | jq -r '.[].category' | sort -u)
if [ -z "$categories" ]; then
    fail "no categories in JSON output"
fi
# Every emitted category must be one of the documented seven.
documented_set="byte-identical broken corpus-collision divergent-identity module-name-collision non-mib oid-arc-sharing"
for cat in $categories; do
    case " $documented_set " in
        *" $cat "*) ;;
        *) fail "unknown category in output: '$cat'" ;;
    esac
done
record

#-------------------------------------------------------------
# 8.7 — Truncation boundary: 73 byte-identical pairs → text caps
#        at 50, prints "... and 23 more (use --report-format=json
#        for the full list)"; JSON shows all 73.
#-------------------------------------------------------------
scenario "8.7: 73-pair truncation boundary"
SRC="$TMPROOT/8.7/upload"
mkdir -p "$SRC"
# Build 73 distinct hash-groups, each with 2 copies, by writing
# 73 distinct payloads. Per-group internal content is identical;
# across-group payloads differ by one byte.
for i in $(seq -f "%03g" 1 73); do
    # Heredoc gives real newlines; the prior `printf '%s'` with a
    # `\n` literal inside the body string wrote two-character
    # `\n` sequences (no parse-able MIB content) and only worked
    # because byte-identical detection operates on bytes
    # regardless of parse outcome.
    cat > "$SRC/grp-${i}-a.mib" <<EOF
UNIQUE-${i}-MIB DEFINITIONS ::= BEGIN
-- payload $i
END
EOF
    cp "$SRC/grp-${i}-a.mib" "$SRC/grp-${i}-b.mib"
done
text_out=$(go run ./cmd/mib-ingest --report --src "$SRC" --root "$TMPROOT/8.7" 2>/dev/null) || true
echo "$text_out" | grep -q '# byte-identical (73)' || fail "header doesn't show total count of 73"
echo "$text_out" | grep -qF '... and 23 more (use --report-format=json for the full list)' \
    || fail "trailer line absent or wrong format"
# JSON should have all 73
json_count=$(go run ./cmd/mib-ingest --report --report-format=json --src "$SRC" --root "$TMPROOT/8.7" 2>/dev/null \
    | jq '[.[] | select(.category=="byte-identical")] | length')
if [ "$json_count" != "73" ]; then
    fail "JSON byte-identical count = $json_count, want 73"
fi
record

#-------------------------------------------------------------
# 8.8 — LastUpdated pivot: SMIv1 upload "9908311200Z" against
#        corpus SMIv2 "200101011200Z" → label corpus-newer.
#-------------------------------------------------------------
scenario "8.8: SMIv1 pivot bucket matches SMIv2 corpus → corpus-newer"
SRC="$TMPROOT/8.8/upload"
ROOT="$TMPROOT/8.8"
mkdir -p "$SRC" "$ROOT/mibs"
# Upload uses SMIv1 LAST-UPDATED form (10 digits + Z).
smi_v2_with_module_identity "PIVOT-MIB" "9908311200Z" 99999 > "$SRC/PIVOT-MIB.mib"
# Synthesised INDEX.yaml carries SMIv2 form for the same module.
cat > "$ROOT/mibs/INDEX.yaml" <<EOF
mibs:
  - file: ietf/other/PIVOT-MIB
    module: PIVOT-MIB
    license: unknown
    imports: [SNMPv2-SMI]
    status: current
    last_updated: 200101011200Z
    added_in: 2026-05-23
EOF
out=$(go run ./cmd/mib-ingest --report --src "$SRC" --root "$ROOT" 2>/dev/null) || true
echo "$out" | grep -q 'PIVOT-MIB label=corpus-newer' || fail "expected 'PIVOT-MIB label=corpus-newer' in output"
record

#-------------------------------------------------------------
# 8.9 — Auto-collapse: five identical files → 4 deleted, lex-first
#        kept; second run is a no-op.
#-------------------------------------------------------------
scenario "8.9: --auto-collapse-identical 5→1 idempotent"
SRC="$TMPROOT/8.9/upload"
ROOT="$TMPROOT/8.9"
mkdir -p "$SRC"
body=$(smi_v2_with_module_identity "BYTE-IDENT-MIB" "202205101200Z" 99999)
# Files named in non-lex order so the kept-lex-first invariant
# isn't trivially satisfied by walk order.
for n in e.mib a.mib d.mib c.mib b.mib; do
    printf '%s' "$body" > "$SRC/$n"
done
# First run: --no-index because the synthesised ROOT has no
# Makefile and the post-ingest make index would otherwise fail.
go run ./cmd/mib-ingest --auto-collapse-identical --no-index --src "$SRC" --root "$ROOT" >/dev/null 2>"$TMPROOT/8.9/first.stderr" || true
if ! grep -q 'auto-collapsed 4 byte-identical' "$TMPROOT/8.9/first.stderr"; then
    fail "stderr missing 'auto-collapsed 4 byte-identical' line"
fi
# Lex-first assertion: after auto-collapse the survivor is the
# lex-first source path (a.mib among {e, a, d, c, b}). ingest
# then classifies it; PEN 99999 isn't in the curated registry
# AND the OID arc routing for this synthesised PEN lands the
# file in `mibs/unsorted/`, which preserves the SOURCE basename
# verbatim (`classificationToDst` for ConfidenceLow uses
# `filepath.Base(srcPath)`). So the on-disk artefact is exactly
# `<root>/mibs/unsorted/a.mib` iff lex-first survived.
if [ ! -f "$ROOT/mibs/unsorted/a.mib" ]; then
    fail "expected mibs/unsorted/a.mib (lex-first survivor) on disk"
fi
for dup in b c d e; do
    if [ -f "$ROOT/mibs/unsorted/${dup}.mib" ]; then
        fail "non-survivor ${dup}.mib should not appear under mibs/unsorted/"
    fi
done
# Second run on the now-emptied src must be a no-op (0 collapses).
go run ./cmd/mib-ingest --auto-collapse-identical --no-index --src "$SRC" --root "$ROOT" >/dev/null 2>"$TMPROOT/8.9/second.stderr" || true
if grep -q 'auto-collapsed' "$TMPROOT/8.9/second.stderr"; then
    fail "second run reported a collapse (should be idempotent no-op)"
fi
record

#-------------------------------------------------------------
# 8.10 — --report --auto-collapse-identical → exit non-zero with
#         a clear mutex error message, no files touched.
#-------------------------------------------------------------
scenario "8.10: --report + --auto-collapse-identical mutex → non-zero, no side effects"
SRC="$TMPROOT/8.10/upload"
mkdir -p "$SRC"
cp mibs/ietf/core/SNMPv2-SMI "$SRC/SNMPv2-SMI"
sha_before=$("${SHA256[@]}" "$SRC/SNMPv2-SMI" | awk '{print $1}')
stderr_file="$TMPROOT/8.10/stderr.log"
go run ./cmd/mib-ingest --report --auto-collapse-identical --src "$SRC" --root "$TMPROOT/8.10" >/dev/null 2>"$stderr_file"
code=$?
if [ $code -eq 0 ]; then fail "exit code 0, want non-zero (mutex)"; fi
if ! grep -q 'mutually exclusive' "$stderr_file"; then
    fail "stderr missing 'mutually exclusive' error message"
fi
sha_after=$("${SHA256[@]}" "$SRC/SNMPv2-SMI" | awk '{print $1}')
if [ -z "$sha_before" ] || [ -z "$sha_after" ]; then
    fail "sha256 tool produced empty output — preflight should have caught this"
elif [ "$sha_before" != "$sha_after" ]; then
    fail "file in --src was touched by the mutex-rejected run"
fi
record

#-------------------------------------------------------------
# Summary
#-------------------------------------------------------------
total=$((PASS + FAIL))
echo
echo "smoke-ingest-report: $PASS / $total passed"
if [ $FAIL -gt 0 ]; then
    echo
    echo "failures:"
    for f in "${FAILURES[@]}"; do
        echo "  $f"
    done
    exit 1
fi
exit 0
