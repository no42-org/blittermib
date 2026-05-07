# Releasing blittermib

Cutting a release is **a single git tag push**. Everything else is
automated by `.github/workflows/release.yml`.

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
[`.github/workflows/release.yml`](.github/workflows/release.yml),
which runs two jobs in sequence:

1. **artifacts** — `make dist` cross-builds five archives:
   - `blittermib-vX.Y.Z-linux-amd64.tar.gz`
   - `blittermib-vX.Y.Z-linux-arm64.tar.gz`

   Each archive contains the binary plus `README.md` and `LICENSE`.
   `SHA256SUMS` is generated alongside. All six files are attached to
   the GitHub Release with auto-generated release notes (commits since
   the previous tag, grouped by Conventional Commit type).

2. **docker** — Multi-arch Docker image built for `linux/amd64` and
   `linux/arm64`, pushed to GHCR with two tags:
   - `ghcr.io/no42-org/blittermib:X.Y.Z` (no leading `v` — the
     workflow strips it from the git tag for Docker tag conventions)
   - `ghcr.io/no42-org/blittermib:latest`

The version string baked into the binary (`./blittermib -version`)
comes from the git tag, passed as `-ldflags "-X main.version=$VERSION"`.

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

Both jobs typically finish in 5–7 minutes. If `artifacts` fails the
release page never publishes; `docker` failing leaves binaries
released but no new image — investigate and either fix forward or
delete + re-tag (see *Recovering* below).

### 5. Verify the release

```bash
# Binary
gh release download vX.Y.Z --repo no42-org/blittermib \
    --pattern '*linux-amd64*'
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf blittermib-vX.Y.Z-linux-amd64.tar.gz
./blittermib-vX.Y.Z-linux-amd64/blittermib -version

# Docker (note: image tag drops the leading `v`)
docker pull ghcr.io/no42-org/blittermib:X.Y.Z
docker run --rm ghcr.io/no42-org/blittermib:X.Y.Z -version
```

Both should print the tag.

## Recovering from a bad release

If the binary or image is broken and nobody has pulled it yet:

```bash
gh release delete vX.Y.Z --repo no42-org/blittermib --yes --cleanup-tag
# Land the fix on main, then re-tag.
```

If users may already have pulled it, **don't delete** — issue
`vX.Y.Z+1` with the fix and document the breakage in the new
release notes. Deleting a published release breaks anyone who pinned
to it.

The Docker `latest` tag re-points on every successful release; users
on `latest` get the fix automatically. Users pinned to `:vX.Y.Z` need
to re-pin.

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

- [docs/self-host.md](docs/self-host.md) — how operators consume
  released artifacts (Docker, bare-metal, systemd).
- [`scripts/dist.sh`](scripts/dist.sh) — what `make dist` actually
  runs locally and in CI.
- [`.github/workflows/release.yml`](.github/workflows/release.yml) —
  the workflow this document mirrors.
