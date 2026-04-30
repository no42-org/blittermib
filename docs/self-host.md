# Self-host guide

blittermib is designed for single-server self-hosting. This guide
covers Docker, bare-metal, reverse-proxy, backup, and troubleshooting.

## Choosing a deployment method

- **Docker (recommended)** — easiest, self-contained, atomic upgrades
  via `docker compose pull`. The image bundles libsmi.
- **Bare-metal binary** — useful for systemd-managed deployments,
  air-gapped systems, or hosts with unusual constraints.
- **From source** — for development or a custom build (e.g. with the
  full standard MIB bundle baked in).

## libsmi requirement

blittermib subprocesses libsmi's `smidump` and `smilint` tools to
parse MIBs. The Docker image includes them; bare-metal installs need
them on the host:

```
brew install libsmi                    # macOS
sudo apt install libsmi2-dev           # Debian / Ubuntu
sudo dnf install libsmi-devel          # Fedora / RHEL
```

Verify:

```
smidump -V
```

If the `make check-tools` target fails at startup, libsmi isn't
resolvable on `$PATH`.

## Docker

### Quickstart

```bash
git clone https://github.com/no42-org/blittermib.git
cd blittermib
mkdir mibs                              # drop your MIB files here
docker compose up -d
```

The shipped `compose.yml`:

- Builds the image locally (or pulls `ghcr.io/no42-org/blittermib:latest`
  if available)
- Mounts `./mibs/` (read-only) into `/var/lib/blittermib/mibs`
- Creates a named volume `blittermib-data` for the SQLite database and
  staged standard MIBs
- Exposes port 8080 on the host
- Sets `stop_grace_period: 35s` so graceful shutdown completes (the
  server's drain window is 30 s)
- Healthchecks against `/healthz` every 30 s

### Custom configuration

Override flags via `command:`:

```yaml
services:
  blittermib:
    command:
      - "-mibs"
      - "/var/lib/blittermib/mibs"
      - "-listen"
      - "0.0.0.0:8080"
      - "-v"
```

### Bind-mount UID caveat

The container runs as user `blittermib` (uid 1000). If you bind-mount
a host directory for `/var/lib/blittermib/data` instead of using the
default named volume, that uid needs write access:

```bash
# either match the uid:
sudo chown -R 1000:1000 /path/to/host/data

# or stick with the named volume in compose.yml (safest default).
```

### Updating

```bash
docker compose pull
docker compose up -d
```

The schema is idempotent — the SQLite database survives upgrades.

## Bare metal

### Linux + systemd

Place the binary at `/usr/local/bin/blittermib`. Install libsmi.
Create a service unit:

```ini
# /etc/systemd/system/blittermib.service
[Unit]
Description=blittermib SNMP MIB browser
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=blittermib
Group=blittermib
ExecStart=/usr/local/bin/blittermib \
    -mibs /var/lib/blittermib/mibs \
    -data /var/lib/blittermib/data \
    -listen 127.0.0.1:8080
Restart=on-failure
RestartSec=5

# Security hardening — adjust to taste.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/blittermib

[Install]
WantedBy=multi-user.target
```

Then:

```bash
sudo useradd -r -s /usr/sbin/nologin blittermib
sudo mkdir -p /var/lib/blittermib/{mibs,data}
sudo chown -R blittermib:blittermib /var/lib/blittermib
sudo systemctl daemon-reload
sudo systemctl enable --now blittermib
```

Logs go to `journalctl -u blittermib`.

### macOS launchd

A standard `launchd` plist under `~/Library/LaunchAgents/` works.
Set the `KeepAlive` and `RunAtLoad` keys; load with `launchctl load`.

## Reverse proxy

blittermib has no built-in TLS or authentication. Front it with a
reverse proxy if it's reachable beyond localhost.

### Caddy (simplest — auto TLS via Let's Encrypt)

```
mibs.example.com {
  reverse_proxy 127.0.0.1:8080
}
```

### nginx

```nginx
server {
  listen 443 ssl http2;
  server_name mibs.example.com;

  ssl_certificate     /etc/letsencrypt/live/mibs.example.com/fullchain.pem;
  ssl_certificate_key /etc/letsencrypt/live/mibs.example.com/privkey.pem;

  location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # Increase if you serve very large MIB source views.
    proxy_buffering on;
    proxy_buffer_size 16k;
  }
}
```

### Traefik (Docker labels)

```yaml
services:
  blittermib:
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.blittermib.rule=Host(`mibs.example.com`)"
      - "traefik.http.routers.blittermib.entrypoints=websecure"
      - "traefik.http.routers.blittermib.tls.certresolver=letsencrypt"
      - "traefik.http.services.blittermib.loadbalancer.server.port=8080"
```

### Authentication

blittermib doesn't authenticate on its own. If you need access control,
let the reverse proxy enforce it (basic auth, OAuth2 proxy, etc.).

## Hot reload

blittermib watches the `-mibs` directory via `fsnotify`. Drop, edit,
or rename a `.mib` / `.txt` / `.my` file and the server re-parses it
within ~250 ms (debounced; rapid edits coalesce into one reload).

The reload is transactional: the previous version of the affected
module is dropped and the new version inserted in a single SQLite
transaction. References INTO the reloaded module from other modules
survive because they're keyed by qualified `Module::Name` pair, not
by row IDs.

If a reload fails (parse error), the previous version stays loaded
and the failure shows on `/diagnostics`.

## Standard MIB bundle

The IETF/IANA standard MIB collection ships embedded inside the binary.
To populate it before building:

```bash
make fetch-standard-mibs    # downloads libsmi 0.5.0 + copies its MIBs
make build                  # next build embeds them
```

The bundle stages into `{data}/standard-mibs/` on every startup.
Files that already exist there are not overwritten — a user who edits
a staged standard MIB has their copy preserved across restarts.

User MIBs in `-mibs` take precedence on filename collision (loaded
last; `ReplaceModule` is per-module so the user's compile run wins).

## Backups

The whole state is in `{data}/blittermib.db`. SQLite's online backup
is consistent under WAL mode:

```bash
sqlite3 /var/lib/blittermib/data/blittermib.db \
  ".backup /backups/blittermib-$(date +%F).db"
```

Schedule via cron or systemd timer. The MIB source files live under
`-mibs` and are independent — back them up separately if they're not
already in version control.

## Monitoring

- `GET /healthz` — returns `{"status":"ok","version":"…"}` when the
  database is queryable; 503 otherwise. Use this for liveness probes.
- `GET /version` — plain-text build version.
- All requests log via `log/slog` at INFO (or DEBUG with `-v`),
  including method, path, status, byte count, and duration.

The Docker compose healthcheck uses `wget -qO- http://127.0.0.1:8080/healthz`.

## Cross-compile

`scripts/dist.sh` produces release archives for every supported
platform plus `SHA256SUMS`:

```bash
make dist
```

CGO is disabled — modernc.org/sqlite makes the SQLite driver pure Go,
so cross-compile works from any Go-enabled host without target SDKs.

Supported targets:

```
linux/amd64  linux/arm64  darwin/amd64  darwin/arm64  windows/amd64
```

## Troubleshooting

### `smidump not found` at startup

libsmi isn't on `$PATH`. See the [libsmi requirement](#libsmi-requirement)
section above for installation. The Docker image bundles libsmi so this
only affects bare-metal installs.

### `/diagnostics` shows parse failures

Some vendor MIBs are stricter than libsmi accepts at default severity.
Check the file/line of each error; common causes:

- Missing IMPORTS — the imported module isn't in `-mibs` or in the
  embedded bundle. Drop the missing module into `-mibs` and reload.
- Identifier not found — typo or unsupported SMI extension.
- Compliance issues — these are warnings, not errors; the module
  still loads.

### Empty search results

The FTS index is populated by every successful `ReplaceModule`. If
no MIBs have loaded successfully, the index is empty. Check
`/diagnostics` and the server logs for compile failures.

### Stale standard MIBs after upgrading libsmi

`mibsbundle.Stage` skips files that already exist in
`{data}/standard-mibs/`. After upgrading the embedded bundle, force
a re-extract:

```bash
# docker
docker compose exec blittermib rm -rf /var/lib/blittermib/data/standard-mibs
docker compose restart

# bare-metal
sudo rm -rf /var/lib/blittermib/data/standard-mibs
sudo systemctl restart blittermib
```

### Hot reload doesn't fire

The watcher only watches the top level of `-mibs`. Subdirectories are
ignored. Move MIB files up to the top level.

The watcher filters by extension (`.mib`, `.txt`, `.my`, or no
extension); files with other extensions don't trigger a reload.

### Container exits with timeout during shutdown

If `docker compose down` SIGKILLs the container before the drain
completes, increase `stop_grace_period` in `compose.yml` (default
shipped is 35 s, which exceeds the server's 30 s drain).

### Resetting state

To start from a clean slate:

```bash
# docker
docker compose down -v        # -v drops the named volume

# bare-metal
sudo systemctl stop blittermib
sudo rm -rf /var/lib/blittermib/data
sudo systemctl start blittermib
```

The server will re-stage the standard MIBs and re-index everything in
`-mibs` on next start.
