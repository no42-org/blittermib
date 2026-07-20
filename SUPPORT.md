# Support

## Where to ask

| You want to… | Go here |
|---|---|
| Report a bug | [Open a bug report](https://github.com/no42-org/blittermib/issues/new?template=bug.yml) |
| Request a feature | [Open a feature request](https://github.com/no42-org/blittermib/issues/new?template=enhancement.yml) |
| Ask "how do I…" | [Open an issue](https://github.com/no42-org/blittermib/issues/new) labelled `question` |
| Report a vulnerability | **Not** an issue — see [SECURITY.md](SECURITY.md) |
| Add or fix a MIB in the corpus | [`mibs/CONTRIBUTING.md`](mibs/CONTRIBUTING.md) |
| Contribute code | [CONTRIBUTING.md](CONTRIBUTING.md) |

Issues are the only support channel. There's no chat, no forum, and no
mailing list — keeping it in one place means answers stay searchable for
the next person with the same question.

## Before you ask

Most questions are already answered:

- [README](README.md) — features, quickstart, configuration, MCP server
- [RELEASING.md](RELEASING.md) — versioning and release artifacts
- [docs/mcp-quickstart.md](docs/mcp-quickstart.md) — wiring the MCP
  server into a client
- [Existing issues](https://github.com/no42-org/blittermib/issues?q=is%3Aissue) —
  search closed ones too

## What makes a question answerable

Include the version (`blittermib -version`), how you're running it
(Docker, binary, Helm), and the actual output rather than a paraphrase of
it. For anything involving a specific MIB, name the module — the corpus
is large and behaviour is often module-specific.

## Response expectations

This is a volunteer-maintained project. Issues are read, but replies are
best-effort and may take a while. A well-scoped issue with a
reproduction gets answered much faster than "it doesn't work".
