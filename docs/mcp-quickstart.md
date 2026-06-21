# MCP server quickstart (stdio)

`blittermib-mcp` exposes the MIB archive to LLM agents over the
[Model Context Protocol](https://modelcontextprotocol.io) using the **stdio**
transport. It opens the same SQLite index the web server reads. Its tools are
**read-only** — they never ingest or mutate the MIB corpus. (Opening the
database applies any pending schema migrations and a one-time
notification-relationship backfill, exactly as the web server does; the MIB
source files are never touched.) It advertises five tools: `search_mibs`,
`lookup_oid`, `lookup_symbol`, `decode_walk`, and `classify_notification`.

> **How stdio works.** A stdio MCP server is not a long-running network
> service. The MCP **client** (Claude Code, Claude Desktop, the MCP Inspector,
> …) launches the binary as a subprocess and talks to it over stdin/stdout for
> the duration of one session. There is no port to expose and no daemon to keep
> running. You point the client at the binary; the client owns its lifecycle.

---

## 1. Prerequisites

- The `blittermib-mcp` binary (build below, or `go build`).
- A populated `blittermib.db`. The server **exits with an error if no database
  is found** at `<data-dir>/blittermib.db`.

## 2. Build

```sh
make build-mcp        # produces ./blittermib-mcp
# or:
go build -o blittermib-mcp ./cmd/blittermib-mcp
```

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

## 7. The tools

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

## 8. Troubleshooting

- **`database not found at <path>`** — the `--data` directory has no
  `blittermib.db`. Point `--data` at the directory holding the database (see
  step 3), using an absolute path.
- **Client shows no tools / fails to connect** — confirm the binary path is
  absolute and executable, and that `--data` resolves from the client's working
  directory. Re-run the step 4 smoke test to isolate the binary from the client.
- **Stale results** — the MCP server reads whatever `blittermib.db` currently
  holds; it does not watch for changes within a session. Re-ingest with the web
  app/ingest pipeline, then start a new session.
