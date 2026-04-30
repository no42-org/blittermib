# fonts/

Self-hosted woff2 files for Geist Sans + Geist Mono, served at
`/static/fonts/` and embedded into the binary at build time.

Both families are SIL OFL 1.1 licensed.

Populate via:

```
make fetch-fonts
```

Files this directory should contain after `make fetch-fonts`:

```
Geist-400.woff2          # Sans regular
Geist-500.woff2          # Sans medium
Geist-600.woff2          # Sans semibold
GeistMono-400.woff2      # Mono regular
GeistMono-500.woff2      # Mono medium
```

The `@font-face` declarations in `prototype/styles.css` reference
these exact filenames. If a file is missing, the browser falls
through the font stack to the next family (system mono / sans).
