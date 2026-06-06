#!/usr/bin/env bash
# Tier 4 — Diff-parse. Captures per-module smilint error sets from
# the parent commit's mibs/ and the PR commit's mibs/, then fails
# when any module gains a new specific error that wasn't present
# before. "Strict" semantics per design.md Decision 8: an old error
# replaced by a new one is still a regression.
#
# Usage:
#     scripts/diff-parse.sh <parent-sha> <pr-sha>
#
# Cold-start: when the parent commit has no `mibs/` directory (the
# introductory PR per §9), the script passes with a one-line note —
# every error in PR's capture would otherwise be flagged "new"
# relative to the empty parent.
#
# Cleanup: the EXIT trap fires on normal exit AND on SIGINT/SIGTERM
# (cancelled CI jobs leak worktrees otherwise) and preserves the
# original failure exit code rather than overriding it with the
# trap body's last command.
#
# v1.0: no SHA cache. Tier 4 caching lands with task §6.7 + §7.5.

set -euo pipefail

if [ $# -ne 2 ]; then
    echo "Usage: $0 <parent-sha> <pr-sha>" >&2
    exit 2
fi

PARENT_SHA="$1"
PR_SHA="$2"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

WORKDIR="$(mktemp -d)"
PARENT_TREE="$WORKDIR/parent"
PR_TREE="$WORKDIR/pr"
PARENT_JSON="$WORKDIR/parent-errors.json"
PR_JSON="$WORKDIR/pr-errors.json"

cleanup() {
    local rc=$?
    git worktree remove --force "$PARENT_TREE" 2>/dev/null || true
    git worktree remove --force "$PR_TREE" 2>/dev/null || true
    git worktree prune 2>/dev/null || true
    rm -rf "$WORKDIR"
    exit "$rc"
}
trap cleanup EXIT INT TERM

git worktree add --detach "$PARENT_TREE" "$PARENT_SHA" >/dev/null
git worktree add --detach "$PR_TREE" "$PR_SHA" >/dev/null

# Cold-start: parent commit predates the corpus introduction. Allow
# the PR through; the regression check has no baseline to compare
# against.
if [ ! -d "$PARENT_TREE/mibs" ] && [ -d "$PR_TREE/mibs" ]; then
    echo "Tier 4: cold start (parent has no mibs/ — allowing)" >&2
    exit 0
fi

# capture <root> <out.json>
# Walks <tree>/mibs and writes a JSON {file: [errors]} keyed by the
# relpath inside mibs/. Errors are sorted to ensure deterministic
# diffs; a missing mibs/ directory yields an empty {} (no errors).
capture() {
    local tree="$1"
    local out="$2"
    local root="$tree/mibs"
    if [ ! -d "$root" ]; then
        echo '{}' > "$out"
        return
    fi
    # Build smidump -p paths with the same prune + sort treatment as
    # verify-mibs-parse.sh so libsmi resolves IMPORTS deterministically.
    local paths=()
    while IFS= read -r d; do
        paths+=(-p "$d")
    done < <(find "$root" \( -name '.*' -o -name 'LICENSES' \) -prune -o -type d -print | LC_ALL=C sort)

    python3 - <<'PY' "$tree" "$root" "$out" "${paths[@]}"
import json, os, subprocess, sys

tree = sys.argv[1]
root = sys.argv[2]
out_path = sys.argv[3]
paths = sys.argv[4:]

result = {}

def is_mib(path):
    base = os.path.basename(path)
    if base.startswith('.'):
        return False
    if base in ('README.md', 'CONTRIBUTING.md',
                '_groups.yaml', '_overrides.yaml', 'INDEX.yaml'):
        return False
    if '/LICENSES/' in path or path.endswith('/LICENSES'):
        return False
    name, ext = os.path.splitext(base)
    if ext and ext.lower() not in ('.mib', '.txt', '.my'):
        return False
    # 32 KB sniff — matches the loader/mib-migrate sniff size after
    # the "verbose-header MIB" hardening patch.
    try:
        with open(path, 'rb') as f:
            head = f.read(32 * 1024)
    except OSError:
        return False
    return b'DEFINITIONS ::= BEGIN' in head

for dirpath, dirs, files in os.walk(root):
    dirs[:] = [d for d in dirs if not d.startswith('.') and d != 'LICENSES']
    for fn in sorted(files):
        full = os.path.join(dirpath, fn)
        if not is_mib(full):
            continue
        rel = os.path.relpath(full, root)
        try:
            r = subprocess.run(
                ['smilint'] + paths + [full],
                capture_output=True, timeout=180,
            )
        except subprocess.TimeoutExpired:
            result[rel] = ['TIMEOUT']
            continue
        # smilint prints to stderr; treat each non-empty line as an
        # error signature for the strict-set comparison. Strip BOTH
        # the worktree root prefix AND the absolute file path so
        # parent/PR worktree paths produce identical diffs.
        #
        # SMIPATH-directory noise: libsmi emits one
        # "<dir>: unable to determine SMI version" line per -p
        # DIRECTORY entry while resolving imports. Those lines name
        # the search path, not the module under test — recording
        # them made every module's error set change when a path
        # directory was renamed (upload/ -> import/ flagged a
        # phantom regression on the whole corpus). Drop any line
        # whose subject is one of our own -p entries.
        lines = []
        for line in r.stderr.decode('utf-8', errors='replace').splitlines():
            line = line.strip()
            if not line:
                continue
            line = line.replace(full, rel)
            line = line.replace(tree + '/mibs/', '')
            line = line.replace(tree + '/', '')
            subject, sep, msg = line.partition(': ')
            if sep and msg == 'unable to determine SMI version' and (
                    subject == 'mibs' or
                    os.path.isdir(os.path.join(root, subject))):
                continue
            lines.append(line)
        if lines:
            result[rel] = sorted(set(lines))

with open(out_path, 'w') as f:
    json.dump(result, f, indent=2, sort_keys=True)
PY
}

capture "$PARENT_TREE" "$PARENT_JSON"
capture "$PR_TREE" "$PR_JSON"

# Regression check: a new specific error in the PR that wasn't in
# the parent's set for the same module is a regression. Modules that
# moved (different relpath) are treated as new — matches the
# design's intent that the relpath is the identity.
python3 - <<'PY' "$PARENT_JSON" "$PR_JSON"
import json, sys
parent = json.load(open(sys.argv[1]))
pr = json.load(open(sys.argv[2]))
regressions = {}
for mod, errs in pr.items():
    parent_set = set(parent.get(mod, []))
    new = [e for e in errs if e not in parent_set]
    if new:
        regressions[mod] = new
if regressions:
    print('Tier 4: per-module error-set regressions detected:', file=sys.stderr)
    for mod in sorted(regressions):
        print(f'  {mod}:', file=sys.stderr)
        for e in regressions[mod]:
            print(f'    {e}', file=sys.stderr)
    sys.exit(1)
print('Tier 4: OK')
PY
