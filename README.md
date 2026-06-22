# blittermib

**Pixelperfect MIB browser** — browse SNMP MIBs, beautifully.

A self-hostable, browser-based reference tool for SNMP MIB files. Drop
a directory of MIBs and get a typographically-disciplined web UI that
lets you search, navigate, and understand them — without sending
anything to a third party.

## Features

- **Search-first navigation** — `⌘K` palette over symbol names, OIDs,
  modules, and full-text descriptions
- **Semantic table rendering** — SMIv2 tables show their columns inline,
  with `INDEX` columns flagged
- **Cross-references** — every symbol page lists what indexes,
  augments, groups, or notifications reference it
- **OpenNMS event export** — modules with notifications offer a
  `↓ events.xml` download (`/m/{name}/events.xml`): an OpenNMS
  eventconf document, one `<event>` per `NOTIFICATION-TYPE`/`TRAP-TYPE`.
  Scalar varbinds are referenced by OID (`%parm[{oid}.0]%`, robust to
  reordering); columnar varbinds stay position-based. UEI base
  overridable via `?uei=`
- **Walk decoder** — paste or upload an `snmpwalk`/`snmpbulkwalk -On`
  capture at `/walk`: it resolves each OID against the loaded MIBs and
  shows a per-module summary — one row per MIB the walk touched, with
  object/value counts — plus a derived list of which of them define
  notifications/traps. Click a module to open its workspace filtered to
  the walk, where rows are decorated with the decoded values. OIDs no
  module covers fall back to the IANA Private Enterprise Number registry
  for a vendor hint
  (`PEN 2636 (Juniper Networks, Inc.) — load a vendor MIB`). Download a
  ZIP bundle (`walk.txt` + `README.txt` + the union import-closure MIBs +
  a `MISSING.txt` manifest) to decode the walk offline. The walk is
  parsed in memory and **never stored, logged, or written to disk**;
  workspace pages decorate rows with walk values via a client-side
  `sessionStorage` overlay
- **Hot reload** — drop a MIB anywhere under the watched directory and
  it appears in seconds (recursive `fsnotify` + 250 ms debounce +
  transactional ingest)
- **Self-hosted** — single binary, no telemetry, no phone-home
- **Standard MIBs included** — IETF/IANA core MIBs ship in the corpus
  alongside vendor MIBs; refresh via `make fetch-standard-mibs && make ingest`
- **Diagnostics surface** — parse failures show file, line, severity,
  and code; failed MIBs never block successful ones
- **Two interactive islands** — virtualised `⌘K` palette over the
  search API + inline glossary popovers with `localStorage` dismissal

## Quickstart

### Docker

The image ships the **standard corpus** (~322 IETF/IANA MIBs) at a
read-only path and mirrors it into the working corpus at every boot —
so upgrades refresh the standard set while your imported MIBs persist.
Everything that matters (curated tree, `import/` intake, SQLite
cache) lives in ONE data volume:

```bash
docker run --rm -p 8080:8080 \
    -v blittermib-data:/var/lib/blittermib/data \
    ghcr.io/no42-org/blittermib:latest
```

#### Importing your own MIBs

Drop files into the corpus's `import/` directory (or use the web
upload, `BLITTERMIB_UPLOAD_ENABLED=true`). The import pipeline
compiles each file and routes it into the curated tree
(`vendors/{PEN}-{vendor}/`, `ietf/{group}/`, `iana/`) — imported
modules are browsable immediately, no restart. Files that fail land
in `import/failed/`, already-known content in `import/duplicate/`,
each with a `.reason.json` sidecar explaining why.

The bundled `compose.yml` binds `./import` on the host into the
intake, so dropping and reviewing quarantined files is a plain
filesystem affair:

```bash
git clone https://github.com/no42-org/blittermib.git
cd blittermib
mkdir -p import
docker compose up
# in another shell:
cp ~/Downloads/SOME-VENDOR-MIB ./import/
ls ./import/failed ./import/duplicate   # outcomes + reasons
```

Open <http://localhost:8080>; the `/import` page shows pending files,
quarantines with reasons, and recent imports.

#### Upgrading from ≤ v0.9.x

- The corpus root moved INSIDE the data directory (`<data>/mibs`).
  Deployments that mounted a corpus at `/var/lib/blittermib/mibs`
  can keep that layout by passing `-mibs /var/lib/blittermib/mibs`,
  or migrate by copying the tree into the data volume
  (`<data>/mibs/`) and dropping the old mount.
- Images no longer ship vendor MIBs or the `blittermib-ingest`
  binary — vendor MIBs enter through `import/` (drop your previous
  vendor set in once), and the old in-container ingest runbook is
  fully replaced by the pipeline.
- A legacy `upload/` directory is renamed to `import/` automatically
  when the server's corpus root points at the old tree (i.e. with
  `-mibs`); on the relocated default a populated legacy corpus is
  flagged with a boot warning instead.

### Bare metal

Requires Go 1.26+ and libsmi (`smidump`, `smilint`):

```bash
brew install libsmi                       # macOS
sudo apt install libsmi2-dev smitools     # Debian / Ubuntu
sudo dnf install libsmi-devel             # Fedora / RHEL

git clone https://github.com/no42-org/blittermib.git
cd blittermib
make build
./blittermib -mibs ./mibs
```

### Kubernetes (Helm)

The Helm chart lives in its own repository —
[no42-org/blittermib-chart](https://github.com/no42-org/blittermib-chart) —
with independent versioning: the chart's `appVersion` pins the
blittermib release it is tested against.

```bash
helm install blittermib oci://ghcr.io/no42-org/charts/blittermib --version <chart-version>
```

### Verifying releases

Everything the release pipeline publishes — the container image and
the binary archives — is signed with
[cosign](https://github.com/sigstore/cosign) **keyless signing**: the
signing identity is this repository's `release.yml` workflow (no
project-held keys exist), and every signature is recorded in the
public Rekor transparency log. (The Helm chart is published and
signed independently from
[no42-org/blittermib-chart](https://github.com/no42-org/blittermib-chart);
see its README for chart verification.)

`<version>` below is the release version with the `v` stripped (e.g.
`0.8.0`).

```bash
IDENTITY='^https://github.com/no42-org/blittermib/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
ISSUER='https://token.actions.githubusercontent.com'

# container image — the digest signature also covers `latest`
cosign verify ghcr.io/no42-org/blittermib:<version> \
  --certificate-identity-regexp="$IDENTITY" \
  --certificate-oidc-issuer="$ISSUER"

# release tarballs — verify the signed checksums file (the
# .sigstore.json bundle carries signature, certificate, and
# transparency-log entry), then chain:
cosign verify-blob SHA256SUMS \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp="$IDENTITY" \
  --certificate-oidc-issuer="$ISSUER"
sha256sum -c SHA256SUMS --ignore-missing   # macOS: shasum -a 256 -c
```

The identity regexp deliberately pins the repository, the workflow
file, *and* the tag-ref shape — a signature from any other workflow
(or a non-tag ref) fails verification.

## Configuration

```
Flags:
  -data PATH           directory for SQLite + state      (./data)
  -mibs PATH           corpus directory override         (<data>/mibs)
  -standard-mibs PATH  read-only standard set mirrored
                       into the corpus at boot           (/usr/share/blittermib/mibs;
                                                          missing = skip)
  -rebuild             discard cache fingerprints and
                       recompile every MIB at boot
  -listen ADDR         HTTP listen address               (:8080)
  -v                   verbose logging (DEBUG level)
  -version             print version and exit
```

Environment variables:

```
  BLITTERMIB_UPLOAD_ENABLED=true
       Expose POST /api/v1/upload (drop zone on the landing page)
       and DELETE /api/v1/upload/{name}. Off by default. This is an
       UNAUTHENTICATED write surface — only enable on deployments
       you control end-to-end (private LAN, reverse proxy with auth,
       single-user dev box). Files land in mibs/import/ and load
       through the same watcher pipeline as files copied with `cp`.

  BLITTERMIB_WALK_DECODER_ENABLED=true
       Expose the SNMP walk decoder at /walk: paste or upload an
       snmpwalk/snmpbulkwalk -On capture and resolve each OID against
       the loaded MIBs. Off by default. Walks are parsed in memory and
       never written to disk, logged, or stored; when disabled the
       routes 404 and the walk-overlay client asset is omitted.
```

URL surfaces:

```
   /                       landing
   /m, /m/{module}         module index + detail
   /s/{module}::{name}     canonical symbol page
   /o/{oid}                OID lookup → 302 to /s/...
   /search?q=…             search results
   /diagnostics            parse warnings + errors
   /api/v1/search?q=…      JSON for the ⌘K palette
   /api/v1/symbol/{m}/{n}  symbol JSON
   /static/*               embedded design system + JS islands
   /healthz                liveness (200 once the process serves)
   /readyz                 readiness (corpus loaded + store usable)
   /version                build info

   When BLITTERMIB_UPLOAD_ENABLED=true (off by default):
   /upload                 management page: drop zone + file list
   /api/v1/upload          multi-file POST → mibs/import/, sync compile
   /api/v1/upload/{name}   DELETE single file from mibs/import/

   When BLITTERMIB_WALK_DECODER_ENABLED=true (off by default):
   /walk                   capture intake: paste or upload an snmpwalk
   /walk/decode            resolve a walk against the loaded MIBs (POST)
   /walk/bundle            offline decode ZIP bundle (POST)
```

## Architecture

```
   MIB files            libsmi              SQLite + FTS5            templ + HTMX
   ─────────────────    ──────              ─────────────            ────────────
   ./mibs/  ──fsnotify──►  smidump XML  ──►  module/symbol/        Pixelperfect HTML
                           smilint diags     reference/diagnostic    ⌘K palette JS
                                             symbol_fts (FTS5)       glossary popovers
```

```
   cmd/blittermib       entry point, signal handling, orchestration
   cmd/blittermib-mcp   read-only MCP (Model Context Protocol) server over the index
   cmd/mib-migrate      one-shot tool: flat MIB collection → PEN-vendor layout
   cmd/mib-index        regenerate mibs/INDEX.yaml metadata catalog
   internal/compile     libsmi subprocess wrappers + XML → model
   internal/iana        embedded IANA Private Enterprise Number registry
   internal/model       normalised in-memory types
   internal/store       SQLite schema, FTS5, transactional reload
   internal/server      HTTP, routes, templ, JSON API, embedded assets
   internal/web         templ templates and the design system CSS
   internal/watch       fsnotify hot-reload with debounce + recover
   mibs/                curated corpus — vendors/, ietf/, iana/, experimental/, unsorted/
   prototype/           static HTML/CSS source-of-truth for the visuals
```

## MCP server

`cmd/blittermib-mcp` exposes the MIB archive to LLM agents over the
[Model Context Protocol](https://modelcontextprotocol.io) (stdio transport). It
opens the same SQLite index the web server reads — **read-only**, never writing
to or ingesting into the corpus — and advertises five tools: `search_mibs`,
`lookup_oid`, `lookup_symbol`, `decode_walk`, and `classify_notification`.

It is a locally-launched binary, not part of the Docker image or release
archives. Build it from source:

```
make build-mcp        # produces ./blittermib-mcp
```

Then point an MCP client at it. For Claude Desktop / Claude Code, add to the
client's MCP server config (adjust the paths):

```json
{
  "mcpServers": {
    "blittermib": {
      "command": "/path/to/blittermib-mcp",
      "args": ["--data", "/path/to/data"]
    }
  }
}
```

`--data` is the directory holding `blittermib.db` (default `./data`); the
server exits with an error if no database is found there.

### Network transport (HTTP)

The same five tools can also be served over the network by the **web server**
at `/mcp`, using the MCP Streamable HTTP transport (stateless, JSON responses).
This lets remote or shared clients reach one running blittermib instead of each
spawning a local stdio process. It is **off by default** and gated by two
environment variables:

| Variable | Effect |
| --- | --- |
| `BLITTERMIB_MCP_ENABLED` | `true` to register the `/mcp` route. |
| `BLITTERMIB_MCP_TOKEN` | Bearer token required on every `/mcp` request. |

The transport **fails closed**: if `BLITTERMIB_MCP_ENABLED` is true but the
token is empty (or whitespace-only), the route stays unregistered and a warning
is logged — it never comes up unauthenticated. Every request must carry
`Authorization: Bearer <token>`; the route sits behind the server's readiness
gate (503 while the corpus is still loading) and the existing `/healthz` +
`/readyz` probes cover it.

```
BLITTERMIB_MCP_ENABLED=true BLITTERMIB_MCP_TOKEN=s3cret blittermib
```

Point an HTTP MCP client at `http://<host>:<port>/mcp` with the bearer token.

> **Security:** the bearer token is *access control, not confidentiality* — it
> authenticates the caller but does not encrypt the connection. Terminate TLS at
> a reverse proxy (or keep the endpoint on a trusted network); a token sent over
> plaintext HTTP can be observed in transit. For the Helm deployment, supply the
> token as a Kubernetes secret (chart support tracked in
> [blittermib-chart](https://github.com/no42-org/blittermib-chart)).

## Documentation

- [docs/mcp-quickstart.md](docs/mcp-quickstart.md) — using the MCP server over stdio with Claude Code / Claude Desktop
- [mibs/README.md](mibs/README.md) — corpus directory layout
- [mibs/CONTRIBUTING.md](mibs/CONTRIBUTING.md) — adding a MIB: 4-step workflow, license-tag matrix, 4-tier CI expectations
- [prototype/](prototype/) — static HTML reference for the design system (open `prototype/index.html` directly)

## Build from source

```
make verify         gofmt-check + vet + race tests
make build          ./blittermib
make build-mcp      ./blittermib-mcp (read-only MCP server)
make generate       regenerate templ-generated files (after editing .templ)
make index          regenerate mibs/INDEX.yaml from the corpus
make verify-mibs    local MIB-corpus checks (lexical + naming + parse)
make refresh-pen    refresh the IANA Private Enterprise Number snapshot
make dist           cross-build release archives into dist/
make docker-build   build the production Docker image (TAG=...)
make hooks          install pre-commit git hooks
make check-tools    verify libsmi (smidump/smilint) is installed
```

## Project conventions

- **Conventional Commits** for every commit
- **AI-assisted, human-reviewed** — every commit carries an `Assisted-by` trailer; the human submitter is responsible for reviewing AI-generated code

## License

[MIT](LICENSE)
