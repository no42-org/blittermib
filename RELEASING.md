# Releasing blittermib

Cutting a release is **a git tag push, then publishing the draft it
produces**. Everything in between is automated by
`.github/workflows/release.yml`.

## Versioning

Semantic versioning (`vMAJOR.MINOR.PATCH`):

- **MAJOR** — breaking change to a public surface: the binary's flag
  set, the `/api/v1/*` HTTP API, the on-disk SQLite schema in a way
  that requires manual migration, or the `mibs/` corpus directory
  layout.
- **MINOR** — new feature, new flag, new API endpoint, additive schema
  migration.
- **PATCH** — bug fixes, doc updates, dependency bumps, refactors with
  no behavior change.

Conventional Commits drive the inference:

- `feat!: …` or any commit footer with `BREAKING CHANGE:` → MAJOR
- `feat: …` → MINOR
- `fix: …` / `chore: …` / `docs: …` / `refactor: …` / `perf: …` /
  `ci: …` / `build: …` / `test: …` → PATCH

Pre-1.0 (current): treat `v0.MINOR.PATCH` the same way; breaking
changes bump MINOR.

## Release pipeline

A push of any tag matching `v*.*.*` triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml):

0. **gates** — the same
   [`gates.yml`](.github/workflows/gates.yml) reusable workflow every
   PR runs (verify, cross-build, lint, govulncheck, gosec, docker
   smoke, actionlint, zizmor). Nothing below starts until it is green,
   so a release can never publish through a weaker bar than a PR
   clears.

1. **meta** — resolves the tag shape once. `v1.2.3` is a stable
   release; `v1.2.3-rc1` matches the same glob but is a **prerelease**,
   is flagged as such on the GitHub Release, and never moves the
   floating `latest` / `X.Y` image tags.

2. **artifacts** — `make dist` cross-builds the release archives:
   - `blittermib-vX.Y.Z-linux-amd64.tar.gz`
   - `blittermib-vX.Y.Z-linux-arm64.tar.gz`
   - `blittermib-mcp-vX.Y.Z-darwin-amd64.tar.gz`
   - `blittermib-mcp-vX.Y.Z-darwin-arm64.tar.gz`
   - `blittermib-mcp-vX.Y.Z-windows-amd64.zip`

   The `linux` archives contain both binaries — `blittermib` and the
   read-only `blittermib-mcp` — plus `README.md` and `LICENSE`. The
   `blittermib-mcp-*` archives are the standalone MCP server for the
   desktop clients (macOS/Windows) where Claude Desktop/Code runs.
   `SHA256SUMS` is generated alongside.

   The job also generates an **SBOM** (syft, SPDX JSON) and attests
   **SLSA build provenance** for the archives and the SBOM. Nothing is
   published from this job — the artifacts are handed onward as
   workflow artifacts.

3. **sign-checksums** — cosign-signs `SHA256SUMS` keyless, producing
   `SHA256SUMS.sigstore.json`. A separate job so a Fulcio/Rekor outage
   can be re-run on its own without redoing the cross-build. One blob
   signature covers every archive via its checksum.

4. **publish** — creates the GitHub Release as a **draft**, with every
   archive, `SHA256SUMS`, the signature bundle, and the SBOM attached,
   and auto-generated notes as a starting point. Prerelease tags are
   marked `prerelease`. Publishing is a deliberate manual step (below)
   — users never see a half-built release.

5. **docker** — multi-arch image for `linux/amd64` and `linux/arm64`,
   pushed to GHCR, signed by digest, with build provenance attested to
   the registry. It is tagged **`staging-X.Y.Z` only** — no
   user-facing tag exists yet.

Publishing the release (step 6 below) triggers
[`promote.yml`](.github/workflows/promote.yml), which attaches the real
tags to that same digest:

- `ghcr.io/no42-org/blittermib:X.Y.Z` (no leading `v` — the pipeline
  strips it for Docker tag conventions)
- `ghcr.io/no42-org/blittermib:X.Y` — floating minor
- `ghcr.io/no42-org/blittermib:latest`

`X.Y` and `latest` always point at the newest **stable** release; a
prerelease publishes only its exact `X.Y.Z-rcN` and leaves the floating
tags alone.

Promotion **re-tags an existing digest, it does not rebuild** — the
bytes users pull are the bytes that were signed and attested, and a
digest signature covers every tag pointing at it, so nothing is
re-signed. Publishing the release is therefore the human approval gate
for the image as well as for the notes: until you publish, nobody can
pull the new version by any normal tag.

The `staging-X.Y.Z` tags are left in place as an audit trail of what
each release was built from. GHCR has no delete-one-tag operation, and
deleting the underlying version would take the promoted tags with it.

Verification commands for all of the above: README *Verifying
releases*, plus `gh attestation verify` for provenance (below).

The version string baked into the binary (`./blittermib -version`)
comes from the git tag, passed as `-ldflags "-X main.version=$VERSION"`.

### Preview builds (not releases)

Every green push to `main` publishes a signed multi-arch **`rc`**
image via [`preview.yml`](.github/workflows/preview.yml):

```bash
docker pull ghcr.io/no42-org/blittermib:rc
```

The `rc` tag is overwritten on each build — that overwrite is the
registry-space control, which is why there are no per-commit tags and
no untagged-version pruning (pruning would break multi-arch releases,
whose manifest lists reference untagged per-architecture children).
`rc` never touches `latest`, `X.Y`, or any `X.Y.Z`. Its version string
is a `git describe` value like `v0.17.7-12-gfdca5b1`, so a preview is
never mistakable for a release.

### Helm chart (separate repository)

The Helm chart lives in
[no42-org/blittermib-chart](https://github.com/no42-org/blittermib-chart)
with its own tag-driven releases and signing identity. After cutting
an application release here, open a PR there bumping the chart's
`appVersion` to the new release and cut a chart release when ready —
that bump PR is the integration point (the chart's kind smoke runs
against the pinned image).

## Cutting a release

### 1. Confirm `main` is ready

```bash
git checkout main
git pull --ff-only
make verify          # gofmt + vet + race tests
```

CI on `main` should be green. No in-flight PRs that need to land
first.

### 2. Decide the version

Run `git log $(git describe --tags --abbrev=0)..HEAD --oneline` and
infer per the rules above. If you're unsure between MINOR and PATCH,
prefer MINOR — it's cheap, and a too-conservative bump is harder to
recover from than a too-generous one.

### 3. Tag and push

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

The tag must match `v*.*.*` (with the leading `v`) — the workflow's
`tags:` filter is exact.

### 4. Watch the workflow

```bash
gh run watch --repo no42-org/blittermib
```

The jobs typically finish in 5–7 minutes plus the gates. Nothing is
user-visible yet: the release is a **draft** and the image is tagged
only `staging-X.Y.Z`. A failure here is fully recoverable without
anyone having seen a broken release. `docker` runs independently of the
draft, so it can succeed while `publish` fails, or vice versa; check
both.

### 5. Verify the draft

```bash
# Binary — `--pattern` works against the draft too
gh release download vX.Y.Z --repo no42-org/blittermib \
    --pattern '*linux-amd64*' --pattern SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf blittermib-vX.Y.Z-linux-amd64.tar.gz
./blittermib-vX.Y.Z-linux-amd64/blittermib -version
# the linux archive also bundles blittermib-mcp; macOS/Windows ship it
# as a standalone blittermib-mcp-<os>-<arch> archive

# Docker — the STAGING tag; the release tags don't exist yet
docker pull ghcr.io/no42-org/blittermib:staging-X.Y.Z
docker run --rm ghcr.io/no42-org/blittermib:staging-X.Y.Z -version
```

Both should print the tag. Then check the supply-chain material:

```bash
IDENTITY='^https://github.com/no42-org/blittermib/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
ISSUER='https://token.actions.githubusercontent.com'

# signature on the checksums file
cosign verify-blob SHA256SUMS \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp="$IDENTITY" \
  --certificate-oidc-issuer="$ISSUER"

# signature on the image — verify the staged tag now; the same digest
# signature will cover X.Y.Z / X.Y / latest after promotion
cosign verify ghcr.io/no42-org/blittermib:staging-X.Y.Z \
  --certificate-identity-regexp="$IDENTITY" \
  --certificate-oidc-issuer="$ISSUER"

# SLSA build provenance
gh attestation verify blittermib-vX.Y.Z-linux-amd64.tar.gz \
  --repo no42-org/blittermib
gh attestation verify oci://ghcr.io/no42-org/blittermib:staging-X.Y.Z \
  --repo no42-org/blittermib
```

Confirm the draft carries every archive, `SHA256SUMS`,
`SHA256SUMS.sigstore.json`, and the `.spdx.json` SBOM. GHCR should show
**only** `staging-X.Y.Z` at this point — `X.Y.Z`, `X.Y`, and `latest`
appear after you publish.

### 6. Write the notes and publish

The auto-generated notes are a starting point, not the release notes.
Replace them with a curated body — `## Highlights` (a handful of
user-facing bullets linking their PR/issue), `## Breaking changes`
with the migration path if any, `## Fixes` one line each. Drop the
chore/CI/dependency noise; collapse it to a single line if it's worth
mentioning at all.

```bash
gh release edit vX.Y.Z --repo no42-org/blittermib \
    --notes-file notes.md --draft=false
```

For a prerelease, keep it flagged and do **not** let it become
"Latest":

```bash
gh release edit vX.Y.Z-rc1 --repo no42-org/blittermib \
    --notes-file notes.md --draft=false --prerelease
```

Publishing fires [`promote.yml`](.github/workflows/promote.yml), which
attaches `X.Y.Z` (plus `X.Y` and `latest` for a stable release) to the
already-signed digest and re-verifies the signature on the promoted
tag. Watch it, then confirm:

```bash
gh run watch --repo no42-org/blittermib
docker pull ghcr.io/no42-org/blittermib:X.Y.Z
docker run --rm ghcr.io/no42-org/blittermib:X.Y.Z -version
```

If promotion fails, the release is published but users on `latest` are
still on the previous version — a safe state. Fix and re-run the
workflow; nothing needs re-tagging.

## Recovering from a bad release

If you spot the problem **before publishing the draft**, this is cheap:
delete the draft and the tag, fix forward, re-tag. Nobody saw it — the
image only ever reached `staging-X.Y.Z`, which no user pulls, and the
floating tags never moved.

If the binary or image is broken and nobody has pulled it yet:

```bash
gh release delete vX.Y.Z --repo no42-org/blittermib --yes --cleanup-tag
# Land the fix on main, then re-tag.
```

If users may already have pulled it, **don't delete** — issue
`vX.Y.Z+1` with the fix and document the breakage in the new
release notes. Deleting a published release breaks anyone who pinned
to it.

The Docker `latest` and `X.Y` tags re-point on every successful stable
release; users on either get the fix automatically. Users pinned to
`:X.Y.Z` need to re-pin.

## Content refreshes (not releases)

Two corpus-content refreshes happen on a separate cadence and don't
need a version bump unless they ride alongside code changes:

- **IANA PEN registry** — `make refresh-pen` updates
  `internal/iana/pen.txt`. Automated quarterly via
  [`.github/workflows/refresh-pen.yml`](.github/workflows/refresh-pen.yml),
  which opens a PR with the diff. Merge it like any other PR.
- **Standard MIBs** — `make fetch-standard-mibs && make ingest`
  refreshes `mibs/ietf/` + `mibs/iana/` against the upstream libsmi
  tarball. Operator-driven; review the diff and merge as a regular PR.

Both ride into the next release naturally.

## See also

- [`scripts/dist.sh`](scripts/dist.sh) — what `make dist` actually
  runs locally and in CI.
- [`.github/workflows/release.yml`](.github/workflows/release.yml) —
  the workflow this document mirrors.
