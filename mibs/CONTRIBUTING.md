# Contributing to the MIB Corpus

> **First time? Start with [HOWTO-ADD-A-MIB.md](HOWTO-ADD-A-MIB.md)** —
> a concrete 8-step walkthrough with example commands, prerequisites,
> and a "common gotchas" table. This document is the reference matter
> behind it: license-tag rules, override semantics, the canonical CI
> tier description.

Adding a MIB takes three steps in the common case:

1. **Drop the file in `mibs/upload/`** and run `make ingest`. The
   tool classifies each MIB, moves it to the canonical destination
   (with the extension stripped), and re-runs `make index`. Files
   that don't classify cleanly stay in `mibs/upload/` or land in
   `mibs/unsorted/` for manual review.
2. **Tag the license** (in `_overrides.yaml` if the auto-detector
   produced `unknown`).
3. **Submit the PR.**

The manual flow (place file → run `make index` → tag license)
remains available as a fallback for files the ingest tool can't
classify; see [HOWTO-ADD-A-MIB.md](HOWTO-ADD-A-MIB.md).

## 1. Where does the file go?

Determine the file's `MODULE-IDENTITY` OID, then route by prefix:

| OID prefix              | Goes in                                |
|-------------------------|----------------------------------------|
| `.1.3.6.1.4.1.{PEN}.*`  | `vendors/{PEN}-{slug}/`                |
| `.1.3.6.1.2.1.*`        | `ietf/{group}/` per `_groups.yaml`     |
| `.1.3.6.1.6.*`          | `iana/`                                |
| `.1.3.6.1.3.*`          | `experimental/`                        |
| anything else           | `unsorted/` (review-then-reclassify)   |

**Vendor slug rules**: lowercase the IANA registry name; drop common
suffix words (`Inc`, `Corp`, `Ltd`, `LLC`, `Co`, `GmbH`, `AG`, `plc`,
`Networks`, `Systems`, `Technologies`); join words with `-`; truncate
to 20 chars; trim trailing `-`. The canonical implementation is
`internal/iana/pen.go::Slug`.

**Filename**: must equal the `MODULE-IDENTITY` name with no extension
(e.g. `CISCO-RTTMON-MIB`). CI Tier 2 rejects mismatches.

## 2. Regenerate `INDEX.yaml`

```bash
make index
```

This rewrites `INDEX.yaml` to reflect the corpus on disk. Commit
the regenerated file alongside your MIB.

`make index` is idempotent: running it twice on the same corpus
produces no diff. CI re-runs it on every PR and fails if your
committed `INDEX.yaml` differs from a fresh generation.

## 3. License tagging

Auto-detection scans the first 200 lines of each MIB for known
copyright patterns. **Pattern order matters**: patterns are tried in
slice order; the first match wins. More-specific patterns (e.g.
`Hewlett[- ]Packard Enterprise`) come before more-general ones (e.g.
`Hewlett[- ]Packard`) for this reason.

| Tag           | Matches header containing                              |
|---------------|--------------------------------------------------------|
| `cisco`       | "Copyright ... Cisco Systems"                          |
| `juniper`     | "Copyright ... Juniper Networks"                       |
| `hpe`         | "Copyright ... Hewlett[- ]Packard Enterprise"          |
| `hp`          | "Copyright ... Hewlett[- ]Packard"                     |
| `aruba`       | "Copyright ... Aruba Networks"                         |
| `huawei`      | "Copyright ... Huawei Technologies"                    |
| `a10`         | "Copyright ... A10 Networks"                           |
| `mellanox`    | "Copyright ... Mellanox"                               |
| `brocade`     | "Copyright ... Brocade"                                |
| `extreme`     | "Copyright ... Extreme Networks"                       |
| `rfc-editor`  | "Copyright ... The Internet Society" or "IETF Trust"   |

Two more tags exist as **sentinels** outside the auto-detector:

- `unknown` — fallback when no pattern matches. CI surfaces the
  count in a sticky PR comment but does NOT block merge.
- `vendor-public` — set ONLY via `_overrides.yaml` for MIBs whose
  vendor publishes redistribution-permitted MIBs but doesn't fit
  any of the auto-detector patterns above.

If your MIB's header doesn't match any pattern, `make index` will
write `license: unknown` and CI will surface the count in a sticky
PR comment. Either:

- Verify the header matches one of the known patterns and re-run
  `make index`, or
- Add an override:

  ```yaml
  # mibs/_overrides.yaml
  licenses:
    YOUR-MIB-NAME: vendor-public
  ```

The full set of accepted license tags is whatever appears in
`LICENSES/`; add a new tag only if you also add the corresponding
`LICENSES/<tag>.txt` reference.

`license: unknown` does NOT block merge. The maintainer reviews
the sticky comment and either accepts the tag or asks you to add
an override.

## 4. CI tier expectations

PRs touching `mibs/**` run four validation tiers in order, each
blocking on failure:

| Tier | Check                                          | When it fires                                                                     | How to fix                                                                       |
|------|------------------------------------------------|-----------------------------------------------------------------------------------|----------------------------------------------------------------------------------|
| 1    | Lexical (ASCII clean, structural markers)      | File contains binary bytes; missing `DEFINITIONS ::= BEGIN ... END` opener        | Save as ASCII; ensure structural markers are present                             |
| 2    | Naming + structure                             | Filename ≠ `MODULE-IDENTITY` name; directory doesn't match PEN derived from OID   | Rename file or move to correct dir                                               |
| 3    | Parser (`smidump` + `smilint` zero errors)     | smilint reports a hard error; an `IMPORTS` reference can't be resolved            | Fix smilint warnings; add the missing imported MIB to the corpus                 |
| 4    | Diff-parse (no module gains a new error)       | Your PR causes a previously-clean module to fail parsing                          | Investigate the namespace collision / redefined symbol; revert or rename         |

Tier 4 specifically catches "this PR silently broke an unrelated
MIB" — the kind of regression a single-file parser miss.

## Local pre-flight

```bash
make index                  # regenerate INDEX.yaml; commit the diff
make verify-mibs            # tier 1 + tier 2 + tier 3 (libsmi-driven)
make verify                 # Go fmt + vet + tests
```

`verify-mibs-lexical` (Tier 1) calls `grep -P` for the non-ASCII
sweep. **GNU grep is required** — macOS BSD grep doesn't support
`-P` and the script will fail with `grep: invalid option -- P`.
On macOS, install GNU grep with `brew install grep` and either alias
`grep` to `ggrep` or place GNU grep first on your `PATH`.

The Tier 4 diff-parse step runs only in CI — it requires `git
worktree` against the PR's base commit. To debug a Tier 4 failure
locally, run `bash scripts/diff-parse.sh <base-sha> HEAD` against
the same SHAs CI sees.

## Override key matching

Override-map keys in `_overrides.yaml` are matched against each
MIB's `MODULE-IDENTITY` name (canonical SMI uppercase, hyphenated)
case-sensitively. Use the module name as it appears in the MIB's
own `<NAME> DEFINITIONS ::= BEGIN` opener — NOT the filename, NOT a
lowercased variant.

## PR template

GitHub stores PR templates at two locations:

- `.github/PULL_REQUEST_TEMPLATE.md` — the default for every PR.
- `.github/PULL_REQUEST_TEMPLATE/<name>.md` — the picker location;
  contributors see it only by appending
  `?template=<name>.md` to the new-PR URL.

This corpus uses the picker location at
`.github/PULL_REQUEST_TEMPLATE/add-mib.md`. To open a PR using the
checklist, use:

```
https://github.com/<owner>/<repo>/compare/main...<branch>?template=add-mib.md
```
