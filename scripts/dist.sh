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

    # The read-only MCP server ships alongside the web binary, stamped
    # with the same version.
    echo ">> building $os/$arch -> $workdir/${bin}-mcp"
    GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
        go build -trimpath -ldflags="$LDFLAGS" \
            -o "$workdir/${bin}-mcp" ./cmd/blittermib-mcp

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

# The MCP server is a stdio binary launched locally by desktop clients
# (Claude Desktop/Code), so it also ships prebuilt for the desktop
# platforms — as standalone, MCP-only archives, since the web binary
# stays linux-only (server/Docker workload). Windows ships a .zip with a
# .exe; the others ship a .tar.gz.
MCP_PLATFORMS=(
    darwin/amd64
    darwin/arm64
    windows/amd64
)

# The windows archive is zipped; fail early with a clear message rather than
# a cryptic mid-loop abort if `zip` is missing on the build host.
if ! command -v zip >/dev/null 2>&1; then
    echo "error: 'zip' is required to package the windows archive" >&2
    exit 1
fi

for plat in "${MCP_PLATFORMS[@]}"; do
    os=${plat%/*}
    arch=${plat#*/}
    ext=""
    [[ "$os" == windows ]] && ext=".exe"

    name="${BIN}-mcp-${VERSION}-${os}-${arch}"
    workdir="$DIST/$name"
    mkdir -p "$workdir"

    echo ">> building $os/$arch -> $workdir/${BIN}-mcp${ext}"
    GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
        go build -trimpath -ldflags="$LDFLAGS" \
            -o "$workdir/${BIN}-mcp${ext}" ./cmd/blittermib-mcp

    for f in README.md LICENSE; do
        [[ -f "$f" ]] && cp "$f" "$workdir/" || true
    done

    (
        cd "$DIST"
        if [[ "$os" == windows ]]; then
            zip -qr "${name}.zip" "$name"
        else
            tar -czf "${name}.tar.gz" "$name"
        fi
        rm -rf "$name"
    )
done

cd "$DIST"
# Both globs always match: the linux loop emits *.tar.gz and MCP_PLATFORMS
# always includes windows (*.zip). Do not switch to `shopt -s nullglob` — an
# empty glob would leave the checksum tool with no args and it would block on
# stdin.
shasum -a 256 *.tar.gz *.zip 2>/dev/null > SHA256SUMS || \
    sha256sum *.tar.gz *.zip > SHA256SUMS
ls -la
