# MCP server quickstart

blittermib serves the same five **read-only** MIB tools over the
[Model Context Protocol](https://modelcontextprotocol.io) through two transports:

- **stdio** — the `blittermib-mcp` binary, which a client launches as a
  subprocess. Best for desktop clients on your own machine (Claude Desktop /
  Claude Code). Covered by sections **1–6** below.
- **network (HTTP)** — the web server serves the same tools at `/mcp` for
  remote or shared access, behind a bearer token. See
  [§7 Network transport](#7-network-transport-http).

Either way the tools are **read-only** — they never ingest or mutate the MIB
corpus. (Opening the database applies any pending schema migrations and a
one-time notification-relationship backfill, exactly as the web server does; the
MIB source files are never touched.) The five tools are `search_mibs`,
`lookup_oid`, `lookup_symbol`, `decode_walk`, and `classify_notification`.

> **How stdio works.** A stdio MCP server is not a long-running network
> service. The MCP **client** (Claude Code, Claude Desktop, the MCP Inspector,
> …) launches the binary as a subprocess and talks to it over stdin/stdout for
> the duration of one session. There is no port to expose and no daemon to keep
> running. You point the client at the binary; the client owns its lifecycle.

---

## 1. Prerequisites

- The `blittermib-mcp` binary — build it below, or download a prebuilt one from
  a release (Linux: bundled in `blittermib-*-linux-*.tar.gz`; macOS/Windows: the
  standalone `blittermib-mcp-*` archive).
- A populated `blittermib.db`. The server **exits with an error if no database
  is found** at `<data-dir>/blittermib.db`.

## 2. Build

```sh
make build-mcp        # produces ./blittermib-mcp
# or:
go build -o blittermib-mcp ./cmd/blittermib-mcp
```

`blittermib-mcp --version` prints the build version (`dev` for local builds; the
release tag for binaries from a release or the Docker image).

Prebuilt binaries ship in every release, so you can skip this step. On Linux,
`blittermib-mcp` is bundled in the `blittermib-*-linux-*.tar.gz` archive. On
macOS and Windows (where Claude Desktop usually runs) it ships as a standalone
`blittermib-mcp-<os>-<arch>` archive — `.tar.gz` for macOS, `.zip` (with
`blittermib-mcp.exe`) for Windows.

## 3. Get a database

`blittermib.db` is produced by running the web app (or the ingest pipeline)
against a corpus. The simplest path:

```sh
make run              # ./blittermib -mibs ./mibs -data ./data
```

This ingests `./mibs` and writes `./data/blittermib.db`. You can stop the web
server afterwards — the MCP server only needs the database file. From here on,
`--data ./data` points the MCP server at it.

## 4. Smoke-test it (optional)

You don't need a client to confirm the binary works. Drive the stdio handshake
directly — send `initialize`, the `initialized` notification, then `tools/list`.
The trailing `sleep` matters: the server shuts down as soon as stdin reaches
EOF, so without it the process can exit before flushing its replies.

```sh
{ printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'; sleep 1; } \
  | ./blittermib-mcp --data ./data
```

You should see two JSON-RPC response lines on stdout — the `initialize` result
and a `tools/list` result whose `tools` array names the five tools. (A real
client keeps the stdin stream open for the whole session, so it never hits this
EOF race.) For an interactive UI, the
[MCP Inspector](https://github.com/modelcontextprotocol/inspector) works too:

```sh
npx @modelcontextprotocol/inspector ./blittermib-mcp --data ./data
```

## 5. Configure a client

### Claude Code (CLI)

Register the server with the `claude mcp add` command. The `--` separates
Claude's own flags from the server command and its arguments:

```sh
claude mcp add blittermib -- /absolute/path/to/blittermib-mcp --data /absolute/path/to/data
```

Then, inside Claude Code, `/mcp` lists the connected servers and their tools.

### Claude Desktop

Edit the MCP server config (create the file if it doesn't exist):

- **macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows:** `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "blittermib": {
      "command": "/absolute/path/to/blittermib-mcp",
      "args": ["--data", "/absolute/path/to/data"]
    }
  }
}
```

Restart Claude Desktop. The blittermib tools appear in the tools menu.

> **Use absolute paths.** The client launches the binary with its own working
> directory, not your project root, so a relative `--data ./data` will not
> resolve. Point `command` and `--data` at absolute paths.

## 6. Running against a Docker deployment

If blittermib runs in a container, the cleanest topology is to ship the
`blittermib-mcp` binary **in the same image** and have the client `exec` it —
one image, one database volume, no cross-container DB sharing. With the binary
present at `/usr/local/bin/blittermib-mcp` and the data directory at
`/var/lib/blittermib/data`, the client command becomes:

```json
{
  "mcpServers": {
    "blittermib": {
      "command": "docker",
      "args": ["exec", "-i", "-u", "1000", "blittermib",
               "/usr/local/bin/blittermib-mcp",
               "--data", "/var/lib/blittermib/data"]
    }
  }
}
```

`-i` keeps stdin open for the stdio stream; `blittermib` is the running
container's name. **`-u 1000` matters**: `docker exec` bypasses the entrypoint
and would otherwise run as root, and opening the database in WAL mode creates
`*.db-wal`/`*.db-shm` files — running as uid 1000 (the user the web server runs
as, which owns the data volume) keeps those files owned by the right user so the
web server can still write them. The image ships `blittermib-mcp` at
`/usr/local/bin/blittermib-mcp` alongside the web binary, so no extra setup is
needed — the running container already has everything.

The same SQLite file is safe to share between the web server and an MCP session:
WAL mode + `busy_timeout` coordinate the readers and the brief writes each
process makes when it opens the database (migrations/backfill). This holds only
on a **local** filesystem — never an NFS/networked volume, where SQLite's file
locking is unreliable.

## 7. Network transport (HTTP)

When you want remote or shared access — several people or agents reaching one
running blittermib instead of each spawning a local binary — enable the
**network transport**. The web server then serves the same five tools at `/mcp`
over the MCP **Streamable HTTP** transport (stateless, JSON responses). No
separate binary, no per-client database path: the server already holds the open
index.

It is **off by default** and protected by a bearer token. Enable it with two
environment variables:

| Variable | Effect |
|----------|--------|
| `BLITTERMIB_MCP_ENABLED` | `true` registers the `/mcp` route |
| `BLITTERMIB_MCP_TOKEN`   | bearer token required on every `/mcp` request |

```sh
BLITTERMIB_MCP_ENABLED=true BLITTERMIB_MCP_TOKEN=s3cret \
  ./blittermib -mibs ./mibs -data ./data
```

It **fails closed**: if `BLITTERMIB_MCP_ENABLED` is true but the token is empty
or whitespace-only, the route stays unregistered (404) and the server logs a
warning — it never comes up unauthenticated. `/mcp` also sits behind the
readiness gate, returning 503 while the corpus is still loading.

> **The token authenticates; it does not encrypt.** A bearer token sent over
> plain HTTP can be read in transit. Terminate TLS at a reverse proxy, or keep
> the endpoint on a trusted network. For Kubernetes, supply the token as a
> Secret (see the [chart repo](https://github.com/no42-org/blittermib-chart)).

### Smoke-test it

A single `initialize` call confirms auth and transport. Streamable HTTP requires
an `application/json` content type and an `Accept` that lists both
`application/json` and `text/event-stream`:

```sh
TOKEN=s3cret
curl -sS http://localhost:8080/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}'
```

A JSON `initialize` result means you're in. Quick negative checks: a missing or
wrong token returns **401**, and a `GET`/`DELETE` to `/mcp` returns **405** (the
stateless JSON server accepts only `POST`).

### Connect a client

**Claude Code (CLI)** — add the server with the HTTP transport and an auth
header:

```sh
claude mcp add --transport http blittermib http://localhost:8080/mcp \
  --header "Authorization: Bearer s3cret"
```

`/mcp` inside Claude Code then lists the blittermib tools, exactly as for the
stdio binary.

**MCP Inspector** — choose the *Streamable HTTP* transport, enter the `/mcp`
URL, and add an `Authorization: Bearer <token>` header.

**Any HTTP MCP client** — point it at `http://<host>:<port>/mcp` and send the
bearer header on every request.

### In Docker

Publish the port and pass the env vars; nothing else changes (the web server and
`/mcp` share one process and one database):

```sh
docker run --rm -p 8080:8080 \
    -e BLITTERMIB_MCP_ENABLED=true \
    -e BLITTERMIB_MCP_TOKEN=s3cret \
    -v blittermib-data:/var/lib/blittermib/data \
    ghcr.io/no42-org/blittermib:latest
```

Unlike the stdio-in-Docker recipe (§6), there is no `docker exec` and no uid
juggling — the running web server owns the database and answers `/mcp` itself.

## 8. The tools

| Tool | Input | What it does |
|------|-------|--------------|
| `search_mibs` | `query`, optional `limit` (default 20) | Full-text search over symbol name, OID, and description; returns ranked hits |
| `lookup_oid` | `oid` | Resolves a numeric OID to its symbol; falls back to the nearest known prefix when no module owns it exactly |
| `lookup_symbol` | `module`, `name` | Full detail for a symbol (kind, syntax, access, status, OID, index columns, enum values, description) |
| `decode_walk` | `text` | Decodes pasted snmpwalk/snmpbulkwalk output; unresolved OIDs carry enterprise (PEN) and nearest-module-root hints |
| `classify_notification` | `module`, `name` | Inferred notification relationship (raise / clear / orphan) with confidence and evidence |

Example prompts once connected:

- "Use blittermib to decode this snmpwalk output: …"
- "What symbol is OID `1.3.6.1.2.1.2.2.1.10`?"
- "Is `linkDown` in IF-MIB a raise or a clear trap?"

## 9. Troubleshooting

Stdio transport:

- **`database not found at <path>`** — the `--data` directory has no
  `blittermib.db`. Point `--data` at the directory holding the database (see
  step 3), using an absolute path.
- **Client shows no tools / fails to connect** — confirm the binary path is
  absolute and executable, and that `--data` resolves from the client's working
  directory. Re-run the step 4 smoke test to isolate the binary from the client.

Network transport:

- **`/mcp` returns 404** — the transport is off. Set `BLITTERMIB_MCP_ENABLED=true`
  **and** a non-empty `BLITTERMIB_MCP_TOKEN`; a missing or blank token fails
  closed (look for the warning in the server log).
- **`/mcp` returns 401** — missing or wrong bearer token. Send
  `Authorization: Bearer <token>` matching `BLITTERMIB_MCP_TOKEN`.
- **`/mcp` returns 503** — the corpus is still loading. Retry once `/readyz`
  returns 200.
- **`/mcp` returns 405** — you sent a `GET` or `DELETE`; the stateless JSON
  transport accepts only `POST`.

Both transports:

- **Stale results** — the MCP server reads whatever `blittermib.db` currently
  holds; it does not watch for changes within a session. Re-ingest with the web
  app/ingest pipeline, then start a new session.
