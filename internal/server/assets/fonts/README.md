# fonts/

Self-hosted woff2 files for Inter + JetBrains Mono, served at
`/static/fonts/` and embedded into the binary at build time.

Both families are SIL OFL 1.1 licensed.

Populate via:

```
make fetch-fonts
```

Files this directory should contain after `make fetch-fonts`:

```
Inter-400.woff2          # Sans regular
Inter-500.woff2          # Sans medium
Inter-600.woff2          # Sans semibold
JetBrainsMono-400.woff2  # Mono regular
JetBrainsMono-500.woff2  # Mono medium
```

Source CDN (Fontsource via jsdelivr):

- `https://cdn.jsdelivr.net/fontsource/fonts/inter@latest/latin-{400,500,600}-normal.woff2`
- `https://cdn.jsdelivr.net/fontsource/fonts/jetbrains-mono@latest/latin-{400,500}-normal.woff2`

The `@font-face` declarations in `../styles.css` reference these
exact filenames. If a file is missing, the browser falls through the
font stack to the next family (system mono / sans).
