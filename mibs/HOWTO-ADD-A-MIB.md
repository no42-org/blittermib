# How to add a MIB to blittermib

A concrete, step-by-step walkthrough. **5–10 minutes per MIB** once you
have a clean source file.

For the reference matter (full license-tag matrix, override semantics,
4-tier CI table), see [CONTRIBUTING.md](CONTRIBUTING.md).

## Prerequisites

```bash
git clone https://github.com/no42-org/blittermib.git
cd blittermib

# libsmi (Tier 3 parser + smilint debugging)
brew install libsmi              # macOS
sudo apt install libsmi2-tools   # Debian/Ubuntu
sudo dnf install libsmi-devel    # Fedora/RHEL

# Go 1.26+ for `make index` / `make verify`
go version

# GNU grep on macOS (Tier 1 uses `grep -P`)
brew install grep
```

## 1. Drop the file in `mibs/upload/` and run `make ingest`

```bash
cp ~/Downloads/CISCO-RTTMON-MIB.mib mibs/upload/
make ingest
```

The ingest tool walks `mibs/upload/`, parses each MIB via libsmi,
classifies its destination per the routing rules below, and moves
the file to the canonical corpus path with the extension stripped.
After all moves complete it auto-runs `make index` so
`mibs/INDEX.yaml` stays in sync.

Outcomes by classification confidence:

| Outcome                           | What happens                              |
|-----------------------------------|-------------------------------------------|
| **high** (clean parse, PEN known) | move to `mibs/vendors/{PEN}-{slug}/<NAME>` (or `mibs/ietf/{group}/<NAME>`, etc.) |
| **medium** (PEN not in curated registry) | move to `mibs/vendors/{PEN}-unknown/<NAME>` with a warning |
| **low** (OID outside known prefixes) | move to `mibs/unsorted/<original-filename>` for operator review |
| destination already exists        | refuse + leave in `mibs/upload/`; operator resolves manually |
| no MIB marker / parse failed      | leave in `mibs/upload/`; check the log for the reason |

Useful flags (run the binary directly rather than `make ingest`):

```bash
go run ./cmd/mib-ingest --dry-run     # preview without touching files
go run ./cmd/mib-ingest --git-add     # stage moved files via `git add`
go run ./cmd/mib-ingest --no-index    # skip the post-ingest make index
```

`--git-add` is opt-in — the default leaves moves unstaged so you can
review with `git status` before staging.

## 2. (Optional) Routing reference

You don't need to know the routing rules for normal use — the
ingest tool handles them. The reference is here for two cases:

- A file ends up in `mibs/unsorted/` and you want to know why.
- You want to extend the routing rules
  (`internal/mibcorpus/classify.go::Classify`).

| OID prefix              | Destination                              |
|-------------------------|------------------------------------------|
| `.1.3.6.1.4.1.{PEN}.*`  | `mibs/vendors/{PEN}-{slug}/`             |
| `.1.3.6.1.2.1.*`        | `mibs/ietf/{group}/` per `_groups.yaml`  |
| `.1.3.6.1.6.*`          | `mibs/iana/`                             |
| `.1.3.6.1.3.*`          | `mibs/experimental/`                     |
| anything else           | `mibs/unsorted/`                         |

Vendor slug rules: lowercase the IANA registry name, strip suffix
words (`Inc`, `Corp`, `Networks`, `Systems`, …), kebab-case,
truncate to 20 chars. See `internal/iana/pen.go::Slug` for the
canonical implementation. Existing examples: `9-cisco`,
`22610-a10`, `2636-juniper`.

## 3. Manual placement (fallback when ingest can't classify)

If your MIB ends up in `mibs/upload/` (parse failed, no marker)
or `mibs/unsorted/` (OID outside known prefixes), you can resolve
it manually:

1. Open the file and find the `MODULE-IDENTITY` declaration.
2. Look up the destination in the routing table above.
3. Move + rename:

```bash
mv mibs/upload/CISCO-RTTMON-MIB.mib mibs/vendors/9-cisco/CISCO-RTTMON-MIB
#                    extension dropped ──────────────────────────────────^
```

Then run `make index` to update the catalog.

## 4. Regenerate the metadata index

```bash
make index
```

This rewrites `mibs/INDEX.yaml`. Open the diff and verify two things:

1. **`license:`** matches the actual copyright owner. The auto-detector
   reads the first 200 lines and matches 11 starter patterns.
   `Copyright (c) <year> Cisco Systems` → `license: cisco`. No match →
   `license: unknown`.
2. **`pen:` and `vendor:`** match the directory you placed the file in.
   `vendors/9-cisco/...` → `pen: 9` / `vendor: cisco`.

## 5. Fix the license tag if needed

If step 4 produced `license: unknown` and you know the correct tag:

```yaml
# mibs/_overrides.yaml
licenses:
  YOUR-MODULE-NAME: vendor-public   # or rfc-editor, cisco, etc.
```

Re-run `make index`. The override wins over the auto-detector.

The valid tags are the files in `mibs/LICENSES/`. Need a new tag? Add
a regex to `cmd/mib-index/license.go::licensePatterns` AND a matching
`LICENSES/<tag>.txt` — discuss with the maintainer first.

## 6. Local pre-flight

```bash
make verify-mibs    # Tier 1 + 2 + 3 (libsmi-driven)
make verify         # gofmt + vet + tests
```

If Tier 3 fails on smilint, run it directly for per-MIB diagnostics:

```bash
smilint -p mibs/vendors/9-cisco mibs/vendors/9-cisco/CISCO-RTTMON-MIB
```

Most common Tier 3 failure: **missing IMPORTS**. Your MIB imports a
parent (e.g. `CISCO-SMI`) that isn't in the corpus yet. Add the parent
in the same PR — or verify it's already there under another directory.

## 7. Commit and open the PR

```bash
git checkout -b add-cisco-rttmon-mib
git add mibs/vendors/9-cisco/CISCO-RTTMON-MIB mibs/INDEX.yaml
# (and mibs/_overrides.yaml if you edited it)
git commit -m "feat(mibs): add CISCO-RTTMON-MIB"
git push -u origin add-cisco-rttmon-mib
```

Open the PR via the **picker URL** so the checklist appears:

```
https://github.com/no42-org/blittermib/compare/main...add-cisco-rttmon-mib?template=add-mib.md
```

Without `?template=add-mib.md`, GitHub renders an empty PR body — the
template at `.github/PULL_REQUEST_TEMPLATE/add-mib.md` is only surfaced
via the picker URL.

## 8. What CI does

Four blocking tiers:

| Tier | Check                                     | Typical failure                  |
|------|-------------------------------------------|----------------------------------|
| 1    | ASCII clean + `DEFINITIONS ::= BEGIN ... END` | non-ASCII bytes, missing END |
| 2    | filename = MODULE-IDENTITY name           | extension not stripped           |
| 3    | smidump + smilint zero errors             | unsatisfied IMPORTS              |
| 4    | per-module error-set comparison vs `main` | redefined symbol, namespace collision |

Plus an **INDEX.yaml drift check** — fails if you forgot to commit
`make index` output.

A green CI + maintainer review = merge.

## Common gotchas

| Symptom                                                       | Cause                                                  | Fix                                                       |
|---------------------------------------------------------------|--------------------------------------------------------|-----------------------------------------------------------|
| `make ingest` left my file in `mibs/upload/`                  | parse failed, no `DEFINITIONS ::= BEGIN`, or destination already exists | check the ingest log for the per-file reason; resolve manually then run again |
| `make ingest` moved my file to `mibs/unsorted/`               | OID is outside the known prefixes (`.1.3.6.1.{2,3,4,6}`) | move manually to the right directory, OR extend `internal/mibcorpus/classify.go::Classify` if the prefix should be supported |
| Tier 2: filename mismatch                                      | left the `.mib` / `.txt` extension on (only happens with the manual fallback flow) | rename the file (or use `make ingest`, which strips it) |
| Tier 3: "module 'X' not found"                                 | imported MIB isn't in the corpus                        | add the parent MIB to the same PR                          |
| Drift check fails                                              | forgot `make index` (after manual placement)            | run `make index`, commit the diff. `make ingest` runs it automatically. |
| Sticky comment lists your MIB as `unknown`                     | header doesn't match any auto-detect pattern            | add an entry to `_overrides.yaml`                          |
| Tier 4 fails on a MIB you didn't touch                         | your new MIB redefines a symbol the existing MIB uses   | rename the conflict, or coordinate with the maintainer    |
| `make verify-mibs-lexical` fails on macOS with `grep: invalid option -- P` | macOS BSD grep doesn't support PCRE                  | `brew install grep`, put GNU grep first on PATH           |
