# Contributing to blittermib

Thanks for helping out. This document covers code contributions.
Adding or correcting a MIB in the bundled corpus has its own,
different workflow — see [`mibs/CONTRIBUTING.md`](mibs/CONTRIBUTING.md).

## Start from an issue

Open an issue before opening a pull request. It's where the "should we
do this, and how" conversation happens, and it keeps a drive-by PR from
being rejected after the work is already done. Reference it from the PR
with a closing keyword (`Closes #123`) so merging resolves it.

## Development loop

```bash
make hooks      # install the pre-commit git hooks (once)
make generate   # regenerate templ output — required after editing .templ files
make verify     # gofmt-check + vet + race tests — must pass before a PR
make lint       # golangci-lint
```

A single test:

```bash
go test ./internal/store -run TestReloadIsTransactional -race
```

`make verify` needs libsmi (`smidump`/`smilint`) on `PATH` — `make
check-tools` tells you whether it's there. Version 0.5.0 or newer; the
Ubuntu-packaged 0.4.8 fails on a slice of the corpus.

If you touch the visuals, edit `prototype/styles.css` — it is the source
of truth — then run `make prepare-assets`. `internal/server/assets/styles.css`
is generated and CI fails if it drifts.

## Commits

**Conventional Commits** — `<type>[scope]: <description>`, where type is
one of `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`,
`chore`, `ci`, `build`, `revert`. Breaking changes append `!` or add a
`BREAKING CHANGE:` footer. The type drives the version bump at release
time (see [RELEASING.md](RELEASING.md)).

Every commit carries two footer trailers, in this order:

```
Assisted-by: ClaudeCode:claude-opus-4-8
Signed-off-by: Jane Developer <jane@example.com>
```

### Developer Certificate of Origin (required)

Every commit must be signed off. Create the trailer with `git commit -s`,
which appends a `Signed-off-by` line using your git identity:

```bash
git commit -s -m "fix(store): keep FK cascades alive across reconnects"
```

Signing off certifies the [Developer Certificate of
Origin](https://developercertificate.org/) — that you wrote the change or
otherwise have the right to submit it under the project's MIT license.
The `Signed-off-by` identity must be a **human**; never an AI agent, and
never a pseudonym you don't stand behind.

### AI assistance

AI-assisted contributions are welcome and used heavily in this project.
The rules:

- Commits produced with an AI agent's help carry an `Assisted-by:
  <Agent>:<model>` trailer — e.g. `Assisted-by: ClaudeCode:claude-opus-4-8`.
  This is disclosure, not a co-author credit.
- The **human who signs off remains fully responsible** for the change:
  for reviewing it, for its correctness, and for its license compliance.
  "The model wrote it" is not a defense for code you signed off on.
- AI working directories (`_bmad/`, `openspec/`, `.claude/`, …) are
  gitignored and never committed.

## Pull requests

- One logical change per PR; keep the diff reviewable.
- CI must be green — the same `make` targets you ran locally.
- Fill in the PR template, including the `Closes #<issue>` line.
- `main` is squash-merged, so your PR becomes exactly one commit. Write
  the PR title as the commit message you want.

## Reporting security issues

Do not open a public issue. See [SECURITY.md](SECURITY.md).
