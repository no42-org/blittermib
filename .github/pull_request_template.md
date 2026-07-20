<!--
Adding or correcting a MIB in the corpus? Use the corpus template instead:
?template=add-mib.md  (see mibs/CONTRIBUTING.md)
-->

Closes #

## What changed

<!-- One or two sentences. The "why" matters more than the "what" — the
     diff already shows the what. -->

## How it was verified

<!-- The command you ran and what it showed. `make verify` alone is fine
     for small changes; anything user-visible deserves a note on how you
     confirmed the behaviour. -->

```
make verify
```

## Checklist

- [ ] Linked to an issue above with a closing keyword
- [ ] Commits use Conventional Commits and are signed off (`git commit -s`)
- [ ] AI-assisted commits carry an `Assisted-by:` trailer
- [ ] `make verify` and `make lint` pass locally
- [ ] `make generate` / `make prepare-assets` re-run and committed, if `.templ` or `prototype/styles.css` changed
- [ ] Docs updated (README / RELEASING / CONTRIBUTING) if behaviour or process changed
