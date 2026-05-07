## Adding a MIB to the corpus

Checklist:

- [ ] Filename matches `MODULE-IDENTITY` name (e.g. `CISCO-RTTMON-MIB`,
      no extension).
- [ ] File lives in the correct directory:
  - `vendors/{PEN}-{slug}/` for vendor MIBs (PEN derived from
    `.1.3.6.1.4.1.{PEN}.*` of the `MODULE-IDENTITY` OID; slug from
    `internal/iana/pen.go::Slug`).
  - `ietf/{group}/` for IETF MIBs (group from `mibs/_groups.yaml`,
    fall back to `other/`).
  - `iana/` for IANA registry MIBs (`.1.3.6.1.6.*`).
  - `experimental/` for `.1.3.6.1.3.*`.
- [ ] `make index` re-run; the resulting `mibs/INDEX.yaml` diff is
      committed alongside the MIB.
- [ ] License tag is correct. If the auto-detector mis-tagged it,
      add an entry to `mibs/_overrides.yaml`. The full set of
      license tags lives in `mibs/LICENSES/`.
- [ ] CI tiers pass: lexical (Tier 1), naming + structure (Tier 2),
      parser (Tier 3), diff-parse (Tier 4). Tier 4 catches
      namespace collisions / redefined-symbol regressions that
      single-file parsing wouldn't surface.

## What's in this PR

<!--
One or two sentences: vendor or RFC, what the MIB describes, why
we want it in the corpus. Link to upstream source if non-IETF.
-->
