# Security Policy

## Supported versions

blittermib is pre-1.0 and ships from a single line of development. Only
the **latest release** receives security fixes; there are no maintained
back-branches. Fixes land on `main` and go out in the next tag.

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it privately through GitHub's private vulnerability reporting:

> https://github.com/no42-org/blittermib/security/advisories/new

That opens a draft advisory visible only to you and the maintainers. If
you can't use it for some reason, email <ronny@no42.org>.

Please include what you'd want to receive: affected version or commit,
the impact, and the smallest reproduction you can manage.

## What to expect

- **Acknowledgement** within 7 days.
- An assessment (severity, whether it reproduces, likely fix shape)
  within 14 days.
- A fix released and an advisory published once one is available. You'll
  be credited in the advisory unless you'd rather not be.

This is a volunteer-maintained project — there is no bug bounty, and
timelines are best-effort rather than contractual.

## Scope notes

blittermib is a self-hosted MIB browser. Things that are in scope:

- Anything reachable over its HTTP surface (the web UI, `/api/v1/*`, the
  walk decoder, the MCP HTTP transport) — injection, path traversal,
  SSRF, authentication bypass on a protected deployment.
- Parsing of untrusted input: MIB files fed to the import pipeline, and
  `snmpwalk` captures pasted into `/walk`.
- Supply-chain issues in the release pipeline — unsigned or
  mis-attributed artifacts, tampering with published images.

Out of scope: findings that require the operator to have already
deliberately exposed an unauthenticated instance to the public internet
in a way the README warns against, and reports from automated scanners
without a demonstrated impact.

## Verifying what you run

Releases are cosign-signed keyless and carry an SBOM and build
provenance. The verification commands are in
[RELEASING.md](RELEASING.md) and the README's *Verifying releases*
section — running them is the cheapest supply-chain check available to
you.
