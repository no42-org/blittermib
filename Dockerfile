# syntax=docker/dockerfile:1.7

# blittermib — multi-stage Docker build.
#
# Stage 1 builds the static Go binary using a Go alpine image. Build
# tools (make, git, libsmi for `make check-tools`) live only here.
#
# Stage 2 is the runtime image: an alpine base with libsmi installed
# so smidump and smilint are available to the running binary.
# CGO is off, so the binary is fully static — alpine's musl is
# irrelevant to the Go side, but libsmi must be present in the
# runtime layer.

ARG GO_VERSION=1.26.4
ARG ALPINE_VERSION=3.21

# --- build stage ----------------------------------------------------

# `golang:<ver>-alpine` resolves to whichever alpine variant the Go
# release was actually built for. Don't pin the alpine version here
# — the official Go image doesn't publish an alpine tag for every
# (Go-patch, alpine-version) pair, so a hard pin breaks unpredictably
# (we got bitten by `1.26.2-alpine3.21` not existing on Docker Hub).
# The runtime stage still pins ALPINE_VERSION because `alpine:3.21`
# is a real tag that always exists.
FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

# System tooling needed by the Makefile during the build.
# templ generation and go build don't need git — dist.sh's
# `git describe` runs in CI, not inside the image.
RUN apk add --no-cache make

# Cache go modules.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source. .dockerignore keeps this minimal.
COPY . .

# Generate templ output and embed assets, then build the static
# server binary. The ingest CLI no longer ships in the image — the
# always-on import pipeline (drop files into the corpus root's
# import/ directory) replaced the in-container runbook. The read-only
# MCP server (blittermib-mcp) ships alongside it for the `docker exec`
# stdio workflow; it needs no assets, so it's a plain go build.
ARG VERSION=docker
ENV CGO_ENABLED=0
RUN make generate \
    && make prepare-assets \
    && go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/blittermib ./cmd/blittermib \
    && go build -trimpath -ldflags="-s -w" \
        -o /out/blittermib-mcp ./cmd/blittermib-mcp

# --- runtime stage --------------------------------------------------

FROM alpine:${ALPINE_VERSION} AS runtime

# libsmi provides smidump and smilint at runtime (subprocessed by
# the compile pipeline). ca-certificates and tzdata are standard
# baseline for any HTTP service.
RUN apk add --no-cache libsmi ca-certificates tzdata su-exec \
    && addgroup -g 1000 -S blittermib \
    && adduser -u 1000 -S -G blittermib -h /home/blittermib blittermib \
    && mkdir -p /var/lib/blittermib/data \
    && chown -R blittermib:blittermib /var/lib/blittermib

# NOTE: no `USER blittermib` here. The image starts as root so the
# entrypoint can repair ownership of the data volume (Docker creates
# nested bind-mount parents as root), then drops to uid 1000 via
# su-exec. The server itself never runs as root.
WORKDIR /home/blittermib

COPY --from=build /out/blittermib /usr/local/bin/blittermib
COPY --from=build /out/blittermib-mcp /usr/local/bin/blittermib-mcp
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# Standard corpus only (IETF + IANA + corpus metadata), at a
# READ-ONLY path outside the runtime corpus root. The boot-time
# standard sync mirrors it into <data>/mibs on every start —
# upgrades refresh the standard set, operator-imported MIBs persist
# untouched. Vendor/unsorted MIBs never ship in the image; they
# enter through the import pipeline.
COPY mibs/ietf/        /usr/share/blittermib/mibs/ietf/
COPY mibs/iana/        /usr/share/blittermib/mibs/iana/
COPY mibs/_groups.yaml /usr/share/blittermib/mibs/_groups.yaml
COPY mibs/LICENSES/    /usr/share/blittermib/mibs/LICENSES/

EXPOSE 8080

# The corpus root defaults to <data>/mibs — curated tree, import/
# intake, and SQLite cache persist as ONE unit on the data volume.
# Override with -mibs to keep a legacy split layout.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["-data", "/var/lib/blittermib/data", "-listen", "0.0.0.0:8080"]
