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

The published image ships the curated corpus (~322 standard IETF/IANA
MIBs) baked in, so you can run it without cloning anything:

```bash
docker run --rm -p 8080:8080 ghcr.io/no42-org/blittermib:latest
```

To layer your own MIBs on top of the baked-in corpus, bind-mount a
host directory at `/var/lib/blittermib/mibs/upload` — the watcher
picks them up alongside the standard corpus:

```bash
mkdir -p ./my-mibs
# drop your .mib / .txt / .my files into ./my-mibs
docker run --rm -p 8080:8080 \
    -v "$PWD/my-mibs:/var/lib/blittermib/mibs/upload:ro" \
    ghcr.io/no42-org/blittermib:latest
```

Or with `compose.yml` (uses a named data volume for the SQLite DB and
auto-restart on failure):

```bash
git clone https://github.com/no42-org/blittermib.git
cd blittermib
mkdir -p mibs/upload
# drop your MIBs into mibs/upload/ — they'll be layered on top of
# the corpus that ships in the image.
docker compose up
```

Open <http://localhost:8080>.

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

## Configuration

```
Flags:
  -mibs PATH      MIB corpus directory              (./mibs)
  -data PATH      directory for SQLite + state      (./data)
  -listen ADDR    HTTP listen address               (:8080)
  -v              verbose logging                   (DEBUG level)
  -version        print version and exit
```

Environment variables:

```
  BLITTERMIB_UPLOAD_ENABLED=true
       Expose POST /api/v1/upload (drop zone on the landing page)
       and DELETE /api/v1/upload/{name}. Off by default. This is an
       UNAUTHENTICATED write surface — only enable on deployments
       you control end-to-end (private LAN, reverse proxy with auth,
       single-user dev box). Files land in mibs/upload/ and load
       through the same watcher pipeline as files copied with `cp`.
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
   /healthz, /version      ops endpoints

   When BLITTERMIB_UPLOAD_ENABLED=true (off by default):
   /upload                 management page: drop zone + file list
   /api/v1/upload          multi-file POST → mibs/upload/, sync compile
   /api/v1/upload/{name}   DELETE single file from mibs/upload/
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

## Documentation

- [docs/self-host.md](docs/self-host.md) — Docker, bare-metal, systemd,
  reverse proxy with TLS, backups, troubleshooting
- [mibs/README.md](mibs/README.md) — corpus directory layout
- [mibs/CONTRIBUTING.md](mibs/CONTRIBUTING.md) — adding a MIB:
  4-step workflow, license-tag matrix, 4-tier CI expectations
- [prototype/](prototype/) — static HTML reference for the design system
  (open `prototype/index.html` directly)
- `openspec/changes/` and `openspec/specs/` — proposals, design notes,
  requirement specs, and task lists for landed + in-flight features

## Build from source

```
make verify         gofmt-check + vet + race tests
make build          ./blittermib
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
- **Spec-driven** via OpenSpec — see `openspec/changes/`
- **AI-assisted, human-reviewed** — every commit carries an
  `Assisted-by` trailer; the human submitter is responsible for
  reviewing AI-generated code

## License

[MIT](LICENSE)
