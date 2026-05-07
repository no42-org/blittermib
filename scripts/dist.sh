#!/usr/bin/env bash
# scripts/dist.sh — cross-build blittermib release artifacts.
#
# Produces a per-platform archive (tar.gz or zip) plus SHA256SUMS in
# the dist/ directory. Invoked by `make dist` and the release CI job.
#
# Environment overrides:
#   VERSION   release tag, baked into the binary via -ldflags. Defaults
#             to the most recent git tag (or "dev" if none).
#   DIST      output directory (default: dist).
#   BIN       binary name (default: blittermib).

set -euo pipefail

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
DIST=${DIST:-dist}
BIN=${BIN:-blittermib}

LDFLAGS="-s -w -X main.version=${VERSION}"

PLATFORMS=(
    linux/amd64
    linux/arm64
)

rm -rf "$DIST"
mkdir -p "$DIST"

for plat in "${PLATFORMS[@]}"; do
    os=${plat%/*}
    arch=${plat#*/}
    bin="$BIN"

    name="${BIN}-${VERSION}-${os}-${arch}"
    workdir="$DIST/$name"
    mkdir -p "$workdir"

    echo ">> building $os/$arch -> $workdir/$bin"
    GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
        go build -trimpath -ldflags="$LDFLAGS" \
            -o "$workdir/$bin" ./cmd/blittermib

    # Bundle docs alongside the binary if present.
    for f in README.md LICENSE; do
        [[ -f "$f" ]] && cp "$f" "$workdir/" || true
    done

    (
        cd "$DIST"
        tar -czf "${name}.tar.gz" "$name"
        rm -rf "$name"
    )
done

cd "$DIST"
shasum -a 256 *.tar.gz 2>/dev/null > SHA256SUMS || \
    sha256sum *.tar.gz > SHA256SUMS
ls -la
