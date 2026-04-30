# Prototype vs rendered HTML — divergences

`prototype/` is the static HTML/CSS source-of-truth for the design
system. It's hand-authored and doesn't talk to a real database. The
rendered pages are produced by [templ](https://templ.guide) templates
in `internal/web/` and captured byte-for-byte by the golden snapshots
under `internal/server/testdata/golden/`.

Some structural drift between the two is intentional. This document
catalogs what diverges and why, so a future contributor changing
either side can tell the difference between an accidental gap and a
deliberate decision.

## Page mapping

| prototype                  | rendered route        | golden                          | status   |
|----------------------------|-----------------------|---------------------------------|----------|
| `index.html`               | `/`                   | `landing.html`                  | aligned  |
| `empty.html`               | `/` (zero modules)    | `landing-empty.html`            | aligned  |
| `symbol.html`              | `/s/{Module}::{Name}` | `symbol-column.html`            | aligned* |
| `table.html`               | `/s/{Module}::{Name}` | `symbol-table.html`             | aligned* |
| `error.html`               | `/diagnostics/{module}` (NOT IMPLEMENTED) | — | divergent — see below |

\* "Aligned" means same CSS classes and structural intent. Real
rendered HTML has dynamic content (real OIDs, descriptions, counts)
where the prototype has static placeholders.

## Intentional divergences

### 1. The left rail is prototype-only

`prototype/symbol.html` and `prototype/table.html` show a persistent
sidebar (`<aside class="rail">`) with the per-module OID tree. The
rendered symbol pages don't have a rail — they're flat single-column
documents.

Why: the rail's per-module tree is a navigation affordance better
served by the dedicated `/tree` page (which has lazy-loading + keyboard
nav and scales to 100k+ nodes). Replicating it in every page would
duplicate the work and bloat the rendered HTML.

CSS classes only present in prototypes: `app`, `rail`, `rail-section`,
`rail-label`, `rail-tree`, `rail-tree-item`, `depth-1`, `depth-2`,
`depth-3`, `num`, `content`.

### 2. `class="page"` vs `class="content"` for the main wrapper

The prototype's symbol page uses `<main class="content">` inside a
`<div class="app">` grid (rail + content). The rendered template uses
`<main class="page">` directly because there's no rail.

Both class names exist in `styles.css` with sensible defaults; the
choice is driven by the surrounding layout, not branding.

### 3. Glossary popovers — affordances absent from rendered descriptions

`prototype/symbol.html` peppers its description prose with
`<a href="#" class="glossary">` markers on SMI vocabulary words
("Discontinuities", `ifCounterDiscontinuityTime`). The rendered
templates render the description verbatim — no automatic glossary
detection.

Why: the glossary dictionary lives in `glossary.js` (~30 terms). A
content layer that auto-decorates description text against this
dictionary would either (a) need a Go-side string-replace pass
(template complexity) or (b) need client-side scanning (JS bloat).
Both are deferred until the glossary's value is proven by manual
markup in a few high-traffic pages.

### 4. Copy-OID button

Prototype: `<span class="copy">copy</span>` next to every OID. The
button affords clipboard-copy via JS that doesn't ship yet.

Rendered pages: omitted. Every OID is rendered with its highlighted
dots (matches prototype) but no copy button.

Why: shipping a copy button without the JS would mislead users — it
looks clickable but does nothing. Adding the JS is small work but
hasn't been prioritised. Closeable any time someone wants to.

### 5. Source disclosure conditionally rendered

`prototype/symbol.html` always shows the "Show full SMI source"
disclosure block. Rendered pages only show it when the module's
`source_path` is recorded AND the source slice can be read. For
fixtures without a source path — including `landing-empty` and the
golden fixture — the disclosure is omitted.

Why: showing an empty disclosure would be worse than hiding it.

### 6. Per-module parse-failure page

`prototype/error.html` is a richly-styled per-module diagnostics
view with line-numbered errors, a "lenient mode" affordance, etc.
The rendered `/diagnostics` is a global view that lists every failed
module with their diagnostics inline.

Why: a per-module deep-link has not yet been wired up (would be
`/diagnostics/{module}`). The global view covers the spec scenario
"parse failures are surfaced gracefully" — the per-module deep page
is polish.

### 7. Theme toggle button

Prototype uses an inline JavaScript snippet for toggle. Rendered
pages serve `/static/theme.js` as a deferred script that wires up
any `[data-theme-toggle]` button. Same UX, different delivery path.

### 8. Demo nav links in prototype topbars

`prototype/index.html` topbar has `<a href="symbol.html">symbol</a>`
etc. — relative links pointing to the other prototype pages. These
are demo-only chrome and have no rendered equivalent.

## Class-name comparison summary

Run from the repo root to regenerate the comparison:

```sh
for proto in prototype/*.html; do
  base=$(basename "$proto" .html)
  case "$base" in
    index)  golden=landing.html ;;
    empty)  golden=landing-empty.html ;;
    symbol) golden=symbol-column.html ;;
    table)  golden=symbol-table.html ;;
    *) continue ;;
  esac
  echo "=== $proto vs golden/$golden ==="
  diff <(grep -oE 'class="[^"]+"' "$proto" | sort -u) \
       <(grep -oE 'class="[^"]+"' "internal/server/testdata/golden/$golden" | sort -u)
done
```

Every left-only class is intentional drift documented above; every
right-only class should be a `class="page"` (the layout-wrapper
divergence) or a real bug that needs filing.

## When to update this doc

Whenever you:

- Land a new templ template that has no prototype counterpart, or
- Change CSS class names used in rendered HTML, or
- Decide to close one of the divergences above (move it from this
  doc to a closed-gap entry below).

## Closed gaps

- **OID separator-dot highlighting** — landed in
  `feat(server): byte-for-byte prototype OID rendering`. Symbol pages
  now wrap each `.` in `<span class="dot">` so the design system's
  accent rule paints the separators in sienna, matching the prototype.
