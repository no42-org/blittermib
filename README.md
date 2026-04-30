# blittermib

**Pixelperfect MIB browser** — browse SNMP MIBs, beautifully.

A self-hostable, browser-based reference tool for SNMP MIB files. Drop
a directory of MIBs and get a typographically-disciplined web UI that
lets you search, navigate, and understand them — without sending
anything to a third party.

```
                  blittermib.
       Browse SNMP MIBs, beautifully.
            [ Search...    ⌘K ]
        287 modules · 85,432 symbols
```

## Features

- **Search-first navigation** — `⌘K` palette over symbol names, OIDs,
  modules, and full-text descriptions
- **Semantic table rendering** — SMIv2 tables show their columns inline,
  with `INDEX` columns flagged
- **Cross-references** — every symbol page lists what indexes,
  augments, groups, or notifications reference it
- **Hot reload** — drop a MIB into the watched directory and it appears
  in seconds (`fsnotify` + 250 ms debounce + transactional ingest)
- **Self-hosted** — single binary, no telemetry, no phone-home
- **Standard MIBs bundled** — IETF/IANA core MIBs ship inside the binary
  so the tool is useful before you supply anything yourself
- **Diagnostics surface** — parse failures show file, line, severity,
  and code; failed MIBs never block successful ones
- **Two interactive islands** — virtualised `⌘K` palette over the
  search API + inline glossary popovers with `localStorage` dismissal

## Quickstart

### Docker

```bash
git clone https://github.com/no42-org/blittermib.git
cd blittermib
mkdir mibs && cp /path/to/your/mibs/* mibs/
docker compose up
```

Open <http://localhost:8080>.

### Bare metal

Requires Go 1.26+ and libsmi (`smidump`, `smilint`):

```bash
brew install libsmi                # macOS
sudo apt install libsmi2-dev       # Debian / Ubuntu
sudo dnf install libsmi-devel      # Fedora / RHEL

git clone https://github.com/no42-org/blittermib.git
cd blittermib
make build
./blittermib -mibs ./mibs
```

## Configuration

```
Flags:
  -mibs PATH      directory of user MIB files       (./mibs)
  -data PATH      directory for SQLite + state      (./data)
  -listen ADDR    HTTP listen address               (:8080)
  -v              verbose logging                   (DEBUG level)
  -version        print version and exit
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
   internal/compile     libsmi subprocess wrappers + XML → model
   internal/model       normalised in-memory types
   internal/store       SQLite schema, FTS5, transactional reload
   internal/server      HTTP, routes, templ, JSON API, embedded assets
   internal/web         templ templates and the design system CSS
   internal/watch       fsnotify hot-reload with debounce + recover
   internal/mibsbundle  embedded standard IETF/IANA MIBs
   prototype/           static HTML/CSS source-of-truth for the visuals
```

## Documentation

- [docs/self-host.md](docs/self-host.md) — Docker, bare-metal, systemd,
  reverse proxy with TLS, backups, troubleshooting
- [prototype/](prototype/) — static HTML reference for the design system
  (open `prototype/index.html` directly)
- [openspec/changes/add-mib-browser/](openspec/changes/add-mib-browser/)
  — change proposal, design notes, requirement spec, task list

## Build from source

```
make verify         gofmt-check + vet + race tests
make build          ./blittermib
make generate       regenerate templ-generated files (after editing .templ)
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

## Sister project

blittermib shares its visual system with
[blitsbom](https://blitsbom.eu) — a self-hostable, browser-only viewer
for CycloneDX SBOM files by the same author. Topbar layout, footer,
typography rules, and the family tagline pattern (`Pixelperfect …`)
are intentionally consistent.

## License

To be decided — placeholder.
