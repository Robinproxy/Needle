# Needle

<p align="center">
  <img src="https://raw.githubusercontent.com/Robinproxy/Needle/main/avatar.png" width="96" height="96" alt="Needle">
</p>

<p align="center">
  A lightweight, single-binary traffic and system monitor for personal servers
</p>

<p align="center">
  <a href="https://github.com/Robinproxy/Needle/releases">Releases</a> ·
  <a href="https://github.com/Robinproxy/Needle/pkgs/container/needle">Container</a>
</p>

<p align="center">
  <a href="https://github.com/Robinproxy/Needle/actions/workflows/docker.yml"><img src="https://github.com/Robinproxy/Needle/actions/workflows/docker.yml/badge.svg?branch=main" alt="Docker build"></a>
</p>

Server + Agents, SQLite, public dashboard, optional administrator editing.

## Features

- System metrics, TCP Ping, monthly traffic, and cost totals.
- Public dashboard; private management of billing, renewal, allowance, and node order.
- Docker / Linux Server; Linux systemd and OpenWrt / ImmortalWrt Agents.

## Architecture

```text
Agent A ─┐
Agent B ─┼── HTTPS ──> Needle Server ──> SQLite
Agent C ─┘                   │
                             └── Web Dashboard
```

Install an Agent on each monitored machine; no inbound Agent ports are needed. The Docker image contains only the Server.

## Deployment & Operations

Upgrade **Server first, then Agents**. Commands use default paths; omit `sudo` in a root shell. Replace uppercase placeholders. Installers fetch the latest stable release; use the packaged installer for older releases.

<details>
<summary><strong>1. Server — Docker Compose</strong></summary>

Linux with Docker Engine and Compose. Run commands from `~/needle`.

### Install

```sh
mkdir -p ~/needle
sudo install -d -o 1000 -g 1000 -m 700 ~/needle/data
cd ~/needle
curl -fsSLO https://raw.githubusercontent.com/Robinproxy/Needle/main/docker-compose.yml
docker compose up -d
```

Use an HTTPS proxy / Tunnel to `127.0.0.1:8008`. Database owner: UID/GID `1000`, directory mode `700`.

### Status, logs, and upgrade

```sh
docker compose ps
docker compose logs --tail=100 needle-server
docker compose logs -f --tail=100 needle-server
docker compose logs --since=1h --tail=100 needle-server
curl -fsS http://127.0.0.1:8008/api/health
docker compose exec needle-server needle-server -version

# Pull, stop, back up the complete SQLite directory, and upgrade
docker compose pull needle-server &&
docker compose stop needle-server &&
sudo cp -a data "data.backup.$(date +%Y%m%d-%H%M%S)" &&
docker compose up -d needle-server

# Service control
docker compose restart needle-server
docker compose stop needle-server
docker compose start needle-server
```

### Agents and tokens

```sh
docker compose exec needle-server needle-server -db /data/needle.db allow-token YOUR_TOKEN
docker compose exec needle-server needle-server -db /data/needle.db list-agents
docker compose exec needle-server needle-server -db /data/needle.db list-tokens

# Move a node to position 1, using its hostname or stable ID
docker compose exec needle-server needle-server -db /data/needle.db move-agent HOSTNAME_OR_ID 1

# Revoke a token; retain the node, history, billing, and traffic
docker compose exec needle-server needle-server -db /data/needle.db revoke-token YOUR_TOKEN

# Delete the node, its bound token, and all associated data
docker compose exec needle-server needle-server -db /data/needle.db delete-agent HOSTNAME_OR_ID
```

`move-agent`: hostname or stable ID + position. `revoke-token` keeps data; `delete-agent` deletes the node, bound token, history, and traffic.

**Full Token query** — `list-tokens` is masked. Requires host `sqlite3` (`sudo apt-get install sqlite3` on Debian / Ubuntu). Replace `HOSTNAME` with the node's hostname; double any single quotes inside it.

```sh
cd ~/needle
sudo -u "#$(sudo stat -c '%u' data/needle.db)" \
  sqlite3 -readonly data/needle.db \
  "SELECT hostname, token FROM agent_tokens WHERE hostname = 'HOSTNAME';"
```

**Without host sqlite3 / when deployed under `/root`:**

```sh
cd ~/needle
docker run --rm -i --user 1000:1000 \
  --mount "type=bind,src=$(pwd)/data,dst=/data" \
  python:3.12-alpine python - HOSTNAME <<'PYTHON'
import sqlite3, sys
with sqlite3.connect("file:/data/needle.db?mode=ro", uri=True) as db:
    rows = db.execute("SELECT hostname, token FROM agent_tokens WHERE hostname = ?", (sys.argv[1],)).fetchall()
    for hostname, token in rows:
        print(f"{hostname}: {token}")
    if not rows:
        print("No matching hostname")
PYTHON
```

### Backup, restore, and removal

Stop the Server and copy the whole data directory, including WAL / SHM files. Replace the backup name when restoring.

```sh
cd ~/needle
docker compose stop needle-server &&
sudo cp -a data "data.backup.$(date +%Y%m%d-%H%M%S)" &&
docker compose start needle-server
```

```sh
cd ~/needle
docker compose down &&
sudo mv data "data.before-restore.$(date +%Y%m%d-%H%M%S)" &&
sudo cp -a data.backup.YYYYMMDD-HHMMSS data &&
docker compose up -d needle-server
```

`docker compose down` keeps data and configuration. Log rotation: `10m` × `3`; apply Compose edits with `docker compose up -d`.

</details>

<details>
<summary><strong>2. Server — Linux Binary / systemd</strong></summary>

Linux amd64 / arm64; root, Bash, systemd, Python 3, account tools, and util-linux.

### Install

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh install
```

Service user: `needle-server`. Default listener: `127.0.0.1:8008`; use an HTTPS proxy / Tunnel.

### Status, logs, and upgrade

```sh
sudo systemctl status needle-server --no-pager
sudo journalctl -u needle-server -n 100 --no-pager
sudo journalctl -u needle-server -f
curl -fsS http://127.0.0.1:8008/api/health
/opt/needle/bin/needle-server -version

# Download the current installer before upgrading
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh upgrade
sudo bash /tmp/needle-server.sh status

sudo systemctl restart needle-server
sudo systemctl stop needle-server
sudo systemctl start needle-server
```

Upgrade keeps `.env`, Tokens, and data. Recovery files: `/opt/needle/recovery/migration.*`.

### Agents and tokens

```sh
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token YOUR_TOKEN
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-tokens
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db move-agent HOSTNAME_OR_ID 1
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db revoke-token YOUR_TOKEN
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent HOSTNAME_OR_ID
```

`move-agent`: hostname or stable ID + position. `revoke-token` keeps data; `delete-agent` deletes the node and associated data.

**Full Token query** — install host `sqlite3`; replace `HOSTNAME` with the node's hostname and double any single quotes inside it.

```sh
sudo -u "#$(sudo stat -c '%u' /opt/needle/data/needle.db)" \
  sqlite3 -readonly /opt/needle/data/needle.db \
  "SELECT hostname, token FROM agent_tokens WHERE hostname = 'HOSTNAME';"
```

### Backup, restore, and removal

Stop the Server and copy the whole data directory. `cp -a` preserves ownership; replace the backup name when restoring.

```sh
sudo systemctl stop needle-server &&
sudo cp -a /opt/needle/data "/opt/needle/data.backup.$(date +%Y%m%d-%H%M%S)" &&
sudo systemctl start needle-server
```

```sh
sudo systemctl stop needle-server &&
sudo mv /opt/needle/data "/opt/needle/data.before-restore.$(date +%Y%m%d-%H%M%S)" &&
sudo cp -a /opt/needle/data.backup.YYYYMMDD-HHMMSS /opt/needle/data &&
sudo systemctl start needle-server
```

Re-download the installer if `/tmp` was cleared. Choose one uninstall command:

```sh
# Remove service and binary; retain .env, database, and service account
sudo bash /tmp/needle-server.sh uninstall
# Also delete the default installation, configuration, and database
sudo bash /tmp/needle-server.sh uninstall --purge
```

`--purge` refuses custom data paths. See [recovery details](docs/p0-p1-validation.md).

</details>

<details>
<summary><strong>3. Agent — Linux Binary / systemd</strong></summary>

Linux amd64 / arm64; root, Bash, systemd, `useradd` / `groupadd` / `userdel` / `groupdel` / `getent`, `runuser`, and `flock`.

### Install

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh && sudo bash /tmp/needle-agent.sh install
```

Enter hostname, final Server URL, Token (empty = generate), intervals, and probes. Run the printed registration command on the Server. Billing / renewal / allowance: `/admin`.

### Upgrade — keep configuration and restart

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh && sudo bash /tmp/needle-agent.sh upgrade --keep-config && sudo systemctl restart needle-agent
```

Keeps YAML, Token, and boot settings. The final restart also starts a previously stopped service.

### Status, logs, and upgrade modes

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh
sudo bash /tmp/needle-agent.sh status
sudo systemctl status needle-agent --no-pager
sudo journalctl -u needle-agent -n 100 --no-pager
sudo journalctl -u needle-agent -f
sudo /opt/needle-agent/bin/needle-agent -version

# Choose ONE upgrade mode
sudo bash /tmp/needle-agent.sh upgrade --keep-config
sudo bash /tmp/needle-agent.sh upgrade --reconfigure
sudo bash /tmp/needle-agent.sh upgrade --configure-intervals

sudo systemctl restart needle-agent
sudo systemctl stop needle-agent
sudo systemctl start needle-agent
sudo systemctl enable needle-agent
sudo systemctl disable needle-agent
```

| Upgrade mode | Behavior |
| --- | --- |
| `upgrade` | Interactive choice; keeping configuration is the default |
| `--keep-config` | Preserve the YAML unchanged; no configuration prompts |
| `--reconfigure` | Reselect TCP Ping and intervals; preserve Token and identity |
| `--configure-intervals` | Change only realtime and full-report intervals |

Choose one mode; reconfiguration keeps Token and identity and backs up YAML to `agent.yaml.bak`. Failed deployment rolls back or prints its recovery path.

### Foreground troubleshooting

```sh
sudo systemctl stop needle-agent
sudo runuser -u needle-agent -- /opt/needle-agent/bin/needle-agent /opt/needle-agent/agent.yaml
# Press Ctrl+C, then resume the service
sudo systemctl start needle-agent
```

### Uninstall

**Delete all local Agent files and Token without confirmation:**

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh && sudo bash /tmp/needle-agent.sh uninstall --yes && rm -f /tmp/needle-agent.sh
```

Local uninstall keeps Server data. Choose `--unregister` to revoke the Server credential:

```sh
# Local uninstall; retain the Server credential and node history
sudo bash /tmp/needle-agent.sh uninstall
# Revoke this credential on the Server, then uninstall locally; retain node history
sudo bash /tmp/needle-agent.sh uninstall --unregister
```

Use Server `delete-agent` to remove the node and history. The empty installer lock remains until reboot; journal retention is managed by systemd.

</details>

<details>
<summary><strong>4. Agent — OpenWrt / ImmortalWrt</strong></summary>

Run as root. Requires procd, BusyBox `sh`, HTTPS wget / curl, CA certificates, and `sha256sum`. Supports x86_64 / amd64 and aarch64 / arm64.

### Install

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh && sh /tmp/needle-agent-openwrt.sh install
```

Enter hostname, final Server URL, Token (empty = generate), intervals, WAN interface, and probes. Run the printed registration command on the Server. Static settings: `/admin`.

Agent runs as root under procd. Defaults: nice `10`, `GOMAXPROCS=1`, soft Go memory target `32 MiB`, open-file limit `128`.

### Upgrade — keep configuration and restart

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh && sh /tmp/needle-agent-openwrt.sh upgrade --keep-config && /etc/init.d/needle-agent restart
```

Keeps YAML, Token, and boot settings. The final restart also starts a previously stopped service.

### Status, logs, and upgrade modes

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh
sh /tmp/needle-agent-openwrt.sh status
logread -e needle-agent
/usr/bin/needle-agent -version

# Choose ONE upgrade mode
sh /tmp/needle-agent-openwrt.sh upgrade --keep-config
sh /tmp/needle-agent-openwrt.sh upgrade --reconfigure
sh /tmp/needle-agent-openwrt.sh upgrade --configure-intervals
# Optionally append --version vX.Y.Z to select a compatible published release

/etc/init.d/needle-agent restart
/etc/init.d/needle-agent stop
/etc/init.d/needle-agent start
/etc/init.d/needle-agent enable
/etc/init.d/needle-agent disable
```

Reconfiguration keeps Token / identity and backs up YAML to `agent.yaml.bak`. Installer minimums: `64 MiB` available RAM, `32 MiB` free `/tmp`, `4 MiB` installation reserve.

### Uninstall and firmware upgrades

**Delete all local Agent files and Token without confirmation:**

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh && sh /tmp/needle-agent-openwrt.sh uninstall --purge --yes && rm -f /tmp/needle-agent-openwrt.sh
```

Other uninstall modes — choose one:

```sh
# Remove binary/service/version; keep local configuration and Server data
sh /tmp/needle-agent-openwrt.sh uninstall
# Also delete local configuration and Needle's firmware-backup entry
sh /tmp/needle-agent-openwrt.sh uninstall --purge
# Revoke the Server credential and remove all local Needle files; keep Server history
sh /tmp/needle-agent-openwrt.sh uninstall --unregister --purge
```

`--unregister` revokes the credential and keeps Server history; failure retains the local installation. System logs follow router retention.

Keeping firmware settings preserves `/etc/needle/`; reinstall the binary and service afterward. For lower load: `interval: 30` or longer, `realtime_interval: 0`, and `tcpping: []`.

</details>

<details>
<summary><strong>Dashboard — Administrator and node settings</strong></summary>

Public dashboard: `/`. Management: `/admin` (or **Manage**).

### Initialize administrator — run on the Server

```sh
# Docker: run from the Server Compose directory
cd ~/needle
docker compose exec needle-server needle-server -db /data/needle.db admin-init

# Linux binary / systemd
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db admin-init
```

Enter username (default `admin`), HTTPS origin without a path, and a password of at least 15 characters twice. Password input is hidden. Sessions last up to eight hours and end on Server restart.

### Reset username, password, or origin

```sh
# Docker, from ~/needle
docker compose exec needle-server needle-server -db /data/needle.db admin-reset

# Linux binary
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db admin-reset
```

Use the actual database path. Forward the browser `Origin` unchanged through the proxy.

### Node settings — Manage → Edit

| Setting | Value / rule |
| --- | --- |
| Display name | Label only; hostname / Token binding stays the same |
| Region | Country code, e.g. `US`, `CN`, `JP` |
| Note | Optional, single line, up to 40 characters |
| Provider website | Optional HTTP / HTTPS URL, up to 2048 bytes; no embedded credentials |
| Position | Starts at 1; moving a node shifts the others |
| Billing currency | Any three-letter code; common: USD, CNY, EUR, GBP, CAD, HKD, JPY, AUD |
| Amount | Price for the whole payment period; `0` = Free, empty = Not set |
| Payment period | 1 / 3 / 6 / 12 / 24 / 36 months, or permanent |
| Renewal date | Actual next payment / top-up date, `YYYY-MM-DD`; reminders advance by period |
| Monthly traffic allowance | Upload + download, decimal GB; `1000 GB = 1 TB`, `0` = ♾️ |
| Monthly reset day | Read-only, pinned independently of renewal; defaults to 1 for new nodes |
| Cost display | Total currency: CNY / USD / EUR / GBP / CAD / HKD / JPY / AUD; Hidden hides only the top total |

Billing / allowance edits do not clear usage. Short months keep the original reset day for later months. New nodes start with billing / renewal unset. Total costs use cached ECB rates; card prices and public billing data remain visible when the total is hidden.

</details>

## Common Paths

<details>
<summary><strong>Server — Docker</strong></summary>

| Item | Path |
| --- | --- |
| Compose file | `~/needle/docker-compose.yml` |
| Optional Compose environment | `~/needle/.env` |
| Host database directory | `~/needle/data/` (UID/GID 1000, mode `700`) |
| Host database and live sidecars | `~/needle/data/needle.db`, `needle.db-wal`, `needle.db-shm` |
| Container database | `/data/needle.db` |
| Manual backup directory | `~/needle/data.backup.YYYYMMDD-HHMMSS/` |
| Logs | `docker compose logs needle-server` |

Host paths assume `~/needle`; back up the whole data directory with the Server stopped.

</details>

<details>
<summary><strong>Server — Binary</strong></summary>

| Item | Default path |
| --- | --- |
| Executable | `/opt/needle/bin/needle-server` |
| Environment | `/opt/needle/.env` |
| Database directory | `/opt/needle/data/` (`needle-server` owned, mode `700`) |
| Database and live sidecars | `/opt/needle/data/needle.db`, `needle.db-wal`, `needle.db-shm` |
| systemd unit | `/etc/systemd/system/needle-server.service` |
| Automatic upgrade backup / originals | `/opt/needle/recovery/migration.*/` |
| Manual backup directory | `/opt/needle/data.backup.YYYYMMDD-HHMMSS/` |
| Logs | `journalctl -u needle-server` |

Custom DB path: inspect `sudo systemctl cat needle-server`.

</details>

<details>
<summary><strong>Agent — Binary</strong></summary>

| Item | Default path |
| --- | --- |
| Executable | `/opt/needle-agent/bin/needle-agent` |
| Configuration | `/opt/needle-agent/agent.yaml` |
| Backup before settings reselection | `/opt/needle-agent/agent.yaml.bak` |
| systemd unit | `/etc/systemd/system/needle-agent.service` |
| Failed rollback originals, if retained | `/opt/needle-agent/recovery/` |
| Downloaded installer | `/tmp/needle-agent.sh` |
| Installer lock (empty; retained until reboot) | `/run/needle-agent-install.lock` |
| Logs | `journalctl -u needle-agent` |

Root owns the installation; service user: `needle-agent`. Logs use system journal retention.

### Permissions

| Location | Owner | Mode |
| --- | --- | --- |
| `/opt/needle-agent/` | `root:needle-agent` | `750` |
| `/opt/needle-agent/bin/` and `bin/needle-agent` | `root:root` | `755` |
| `/opt/needle-agent/agent.yaml` | `root:needle-agent` | `640` |
| `/opt/needle-agent/agent.yaml.bak` (if present) | `root:root` | `600` |
| `/etc/systemd/system/needle-agent.service` | `root:root` | `644` |

</details>

<details>
<summary><strong>Agent — OpenWrt / ImmortalWrt</strong></summary>

| Item | Default path |
| --- | --- |
| Executable | `/usr/bin/needle-agent` |
| Configuration | `/etc/needle/agent.yaml` |
| Backup before settings reselection | `/etc/needle/agent.yaml.bak` |
| Installed version | `/etc/needle/version` |
| procd service | `/etc/init.d/needle-agent` |
| Firmware backup list | `/etc/sysupgrade.conf` (Needle entry: `/etc/needle/`) |
| Downloaded installer | `/tmp/needle-agent-openwrt.sh` (temporary; cleared on reboot) |
| Installer lock (removed on exit) | `/tmp/needle-agent-installer.lock/` |
| Staging / failed recovery snapshot | `/tmp/needle-agent.XXXXXX/` (actual path printed on failure) |
| Logs | `logread -e needle-agent` |

Normal uninstall keeps YAML; `--purge` removes it. Router logs clear on reboot.

`/etc/needle/` must be owned by root with no group / other write access (new installs: `700`). YAML and version files use `600`. Unsafe directories are rejected before operations.

</details>

## Configuration

<details>
<summary><strong>Realtime updates and historical sampling</strong></summary>

```yaml
realtime_interval: 5 # Extra live-only reports, seconds; 0 disables
interval: 30        # Full history, TCP Ping, and monthly traffic accounting
```

| Setting | Rule |
| --- | --- |
| New install | Realtime / history: `5 / 30` seconds |
| Omitted realtime interval | `0` (off), including after `--keep-config` |
| History retry defaults | 64 reports including in-flight; 8 MiB total; max age 1800 seconds |
| Full-report overload | 16 concurrent; `429` / `503` include `Retry-After` |
| Browser transport | SSE; fallback polling every 30 seconds |

Retries are memory-only and lost on restart. Change intervals with `upgrade --configure-intervals` or edit YAML and restart. [Protocol details](docs/p0-p1-validation.md).

</details>

<details>
<summary><strong>Traffic counter continuity and migration</strong></summary>

Monthly usage accumulates upload + download deltas. Counter decreases / interface changes rebase safely; confirmed new boots retain prior usage. Allowance edits do not reset counters. Missing historical traffic cannot be reconstructed. [Accounting and migration details](docs/p0-p1-validation.md).

</details>

<details>
<summary><strong>Server — Docker Compose settings</strong></summary>

File: `~/needle/docker-compose.yml`.

```yaml
services:
  needle-server:
    image: ghcr.io/robinproxy/needle:latest
    ports:
      - "127.0.0.1:${NEEDLE_PORT:-8008}:8008"
    environment:
      NEEDLE_LISTEN: ":8008"
    volumes:
      - ./data:/data
    restart: unless-stopped
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8008/api/health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

| Setting | Meaning / default |
| --- | --- |
| `image` | `latest`: tested current main; use `vX.Y.Z` to pin a release |
| `ports` | Loopback only; host port → container port |
| `NEEDLE_PORT` | Host port, default `8008`; set in `~/needle/.env` |
| `NEEDLE_LISTEN` | Container listener, `:8008`; align ports and health check |
| `volumes` | `./data:/data`; persistent SQLite storage |
| `restart` | `unless-stopped` |
| `logging.driver` | `json-file` |
| `logging.options.max-size` | `"10m"` per file |
| `logging.options.max-file` | `"3"` files |
| `healthcheck` | Internal `/api/health` check |

### Change host port to 8088

```sh
cd ~/needle
nano .env
# Add or edit: NEEDLE_PORT=8088
docker compose config
docker compose up -d needle-server
curl -fsS http://127.0.0.1:8088/api/health
```

Point the proxy / Tunnel to `127.0.0.1:8088`. Apply Compose edits with `up -d`; use `up -d --force-recreate needle-server` to force recreation.

</details>

<details>
<summary><strong>Server — Binary environment and flags</strong></summary>

### Edit and apply

```sh
sudo nano /opt/needle/.env
```

```dotenv
NEEDLE_LISTEN=127.0.0.1:8008
```

```sh
sudo systemctl restart needle-server
sudo systemctl status needle-server --no-pager
sudo journalctl -u needle-server -n 50 --no-pager
```

| Flag / variable | Meaning / default |
| --- | --- |
| `-l` / `NEEDLE_LISTEN` | Listen address; `-l` overrides the variable; default `127.0.0.1:8008` |
| `-db` | SQLite path; binary default `./data/needle.db`, installer uses `/opt/needle/data/needle.db` |
| `-cert` and `-key` | Native HTTPS certificate and key; supply both, readable by the service user |
| `-y` | Skip `delete-agent` / `revoke-token` confirmation; place before the subcommand |
| `-version` | Print version and source commit without opening the database |

Systemd loads `.env`; the binary does not. For native TLS, set both flags in the service command:

```sh
./needle-server -l 127.0.0.1:8443 -db ./data/needle.db -cert /path/to/fullchain.pem -key /path/to/privkey.pem
```

After unit edits: `sudo systemctl daemon-reload` then restart. Use one Server process per database. Keep its timezone stable for billing / traffic dates.

</details>

<details>
<summary><strong>Agent — Linux Binary / systemd</strong></summary>

### Edit

```sh
sudo nano /opt/needle-agent/agent.yaml
```

```yaml
hostname: "example-node"
server: "https://needle.example.com"
token: "replace-with-unique-agent-token"
server_managed: true
realtime_interval: 5
interval: 30
# interfaces: ["eth0"]
tls_skip_verify: false
allow_plain_http: false
tcpping:
  - name: CMv4
    target: sh-cm-v4.ip.zstaticcdn.com:80
  - name: CUv4
    target: sh-cu-v4.ip.zstaticcdn.com:80
  - name: CTv4
    target: sh-ct-v4.ip.zstaticcdn.com:80
```

| Field | Default / rule |
| --- | --- |
| `hostname` | Stable identity; empty = system hostname |
| `server` | Final HTTPS URL; redirects must keep scheme / host / effective port / method |
| `token` | Required, unique; 1–256 characters, no whitespace or controls |
| `server_managed` | Default `true`; `false` is rejected; static settings belong in `/admin` |
| `realtime_interval` | Seconds: `0` = off; otherwise 1–`interval`. New install `5`; omitted `0` |
| `interval` | History / TCP Ping / traffic interval; default `30` seconds, max `86400`; non-positive uses `30` |
| `history_queue_capacity` | Optional, 1–1024; omitted / `0` = `64`; separate 8 MiB cap |
| `history_retry_max_age_seconds` | Optional, 1–86400 seconds; omitted / `0` = `1800` from sample time |
| `interfaces` | NIC whitelist; omitted / empty auto-selects physical interfaces, excluding loopback / virtual / VPN |
| `tls_skip_verify` | Default `false`; `true` skips certificate verification |
| `allow_plain_http` | Default `false`; remote HTTP requires `true`; loopback allowed. No HTTPS downgrade |
| `tcpping` | `[]` / omitted = off. Max 32; unique normalized names, 1–64 UTF-8 bytes, no controls; `host:port`, ports 1–65535; IPv6: `[address]:port` |

### Installer TCP Ping modes

| Installer mode | Targets |
| --- | --- |
| `1) IPv4 only` (default) | CMv4, CUv4, CTv4 |
| `2) IPv6 only` | CMv6, CUv6, CTv6 |
| `3) Dual stack` | All six presets |
| `4) Custom` | Selected presets and / or custom TCP targets |
| `5) Disabled` | No probes; system monitoring continues |

`tcpping: []` disables probes; old history expires normally. Full template: [agent.yaml.example](agent.yaml.example).

### Validate and restart

```sh
sudo /opt/needle-agent/bin/needle-agent -check-config /opt/needle-agent/agent.yaml &&
sudo systemctl restart needle-agent
sudo systemctl status needle-agent --no-pager
sudo journalctl -u needle-agent -n 50 --no-pager

# Read the full Token locally
sudo grep '^token:' /opt/needle-agent/agent.yaml
```

File owner / mode: `root:needle-agent`, `640`. Check service-user access with `sudo runuser -u needle-agent -- /opt/needle-agent/bin/needle-agent -check-config /opt/needle-agent/agent.yaml`.

Remove retired YAML keys: `region`, `billing_currency`, `billing_amount`, `billing_period`, `expires_at`, `monthly_traffic_limit_gb`. Set them in `/admin`; replace `insecure` with `tls_skip_verify`.

</details>

<details>
<summary><strong>Agent — OpenWrt / ImmortalWrt</strong></summary>

### Edit

```sh
nano /etc/needle/agent.yaml
```

```yaml
hostname: "example-node"
server: "https://needle.example.com"
token: "replace-with-unique-agent-token"
server_managed: true
realtime_interval: 0
interval: 30
interfaces: ["pppoe-wan"]
tls_skip_verify: false
allow_plain_http: false
tcpping: []
```

| Field | Default / rule |
| --- | --- |
| `hostname` | Stable identity; empty = system hostname |
| `server` | Final HTTPS URL; redirects must keep scheme / host / effective port / method |
| `token` | Required, unique; 1–256 characters, no whitespace or controls |
| `server_managed` | Default `true`; `false` is rejected; static settings belong in `/admin` |
| `realtime_interval` | Seconds: `0` = off; otherwise 1–`interval`. New install `5`; omitted `0` |
| `interval` | History / TCP Ping / traffic interval; default `30` seconds, max `86400`; non-positive uses `30` |
| `history_queue_capacity` | Optional, 1–1024; omitted / `0` = `64`; separate 8 MiB cap |
| `history_retry_max_age_seconds` | Optional, 1–86400 seconds; omitted / `0` = `1800` from sample time |
| `interfaces` | Actual WAN interface, e.g. `["pppoe-wan"]` / `["eth1"]`; never count both LAN and WAN or PPPoE and its physical device |
| `tls_skip_verify` | Default `false`; `true` skips certificate verification |
| `allow_plain_http` | Default `false`; remote HTTP requires `true`; loopback allowed. No HTTPS downgrade |
| `tcpping` | `[]` / omitted = off. Max 32; unique normalized names, 1–64 UTF-8 bytes, no controls; `host:port`, ports 1–65535; IPv6: `[address]:port` |

### Installer TCP Ping modes

| Installer mode | Targets |
| --- | --- |
| `1) IPv4 only` (default) | CMv4, CUv4, CTv4 |
| `2) IPv6 only` | CMv6, CUv6, CTv6 |
| `3) Dual stack` | All six presets |
| `4) Custom` | Selected presets and / or custom TCP targets |
| `5) Disabled` | No probes; system monitoring continues |

`tcpping: []` disables probes; old history expires normally. Full template: [agent.yaml.example](agent.yaml.example).

### Validate and restart

```sh
/usr/bin/needle-agent -check-config /etc/needle/agent.yaml &&
/etc/init.d/needle-agent restart
logread -e needle-agent

# Read the full Token locally
grep '^token:' /etc/needle/agent.yaml
```

Select the real WAN device (`l3_device` on OpenWrt). Use `vi` if `nano` is unavailable.

Remove retired YAML keys: `region`, `billing_currency`, `billing_amount`, `billing_period`, `expires_at`, `monthly_traffic_limit_gb`. Set them in `/admin`; replace `insecure` with `tls_skip_verify`.

</details>

## Reference

<details>
<summary><strong>Node cards and live probe results</strong></summary>

CPU core count and system information reflect the monitored host. `Load`: one-minute system load. `Seen … ago`: report overdue. ♾️: permanent expiry / unlimited traffic. TCP Ping statistics use the selected history window.

</details>

<details>
<summary><strong>Upgrade checklist</strong></summary>

1. Back up SQLite; upgrade Server first.
2. Check static settings in `/admin`; remove retired Agent YAML fields.
3. Upgrade Agents with `--keep-config`; reselect only when changing probes / intervals.
4. Check logs and refresh the dashboard. Do not downgrade an upgraded database to an incompatible release.

</details>

<details>
<summary><strong>Dashboard refresh troubleshooting</strong></summary>

| Symptom / log | Check |
| --- | --- |
| Refresh / live connection fails | `/api/health`, `/api/info`, reverse proxy; allow long-lived `/api/events` |
| `Updates paused` | Click Retry; inspect Server logs |
| History `422` | Shorten the time window; response point limit reached |
| History `503` / `504` | Busy / timed out; retry or shorten the window |
| Agent `3xx` | Set `server` to the final URL; cross-origin / method-changing redirects are rejected |
| `exchange_rates` | Actual ECB synchronization; recovery requires a successful update |
| `exchange_rates_cache` | Local cache/database read |
| `count` / `suppressed` | Total failures / omitted repeats; repeated errors summarized at most once per minute |

Log commands are in each deployment section. [Validation and recovery details](docs/p0-p1-validation.md).

</details>

<details>
<summary><strong>Traffic counter regressions and recovery</strong></summary>

Legacy repair only. Replace the timestamp with the confirmed faulty deployment time in RFC3339 format, and `HOSTNAME_OR_ID` with the affected node. Preview before applying.

### Server binary

```sh
sudo systemctl stop needle-server
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db repair-traffic all
# Run only after reviewing the preview
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db repair-traffic --since CONFIRMED_DEPLOYMENT_RFC3339 --apply HOSTNAME_OR_ID
sudo systemctl start needle-server
```

### Server Docker

```sh
docker compose stop needle-server
docker compose run --rm --no-deps needle-server -db /data/needle.db repair-traffic all
# Run only after reviewing the preview
docker compose run --rm --no-deps needle-server -db /data/needle.db repair-traffic --since CONFIRMED_DEPLOYMENT_RFC3339 --apply HOSTNAME_OR_ID
docker compose start needle-server
```

Strong-identity counters refuse legacy repair. Applying creates `needle.db.traffic-backup-TIMESTAMP.db` (mode `600`). [Repair details](docs/p0-p1-validation.md).

</details>

<details>
<summary><strong>History views and retention</strong></summary>

| View / data | Resolution / retention |
| --- | --- |
| `1d` | Raw samples for the last 24 hours |
| `7d` | About 720 time buckets with daily drill-down |
| Date view | Recent calendar day, raw while available |
| `30d` | Hourly trends |
| Raw history | Seven days, retaining complete boundary hours (up to one extra hour) |
| Hourly history | 90 days |
| Monthly traffic records | Old cycle starts beyond 62 days are cleaned, except protected migration records |

Normal daily views stay raw; dense results aggregate without truncating. Query limits: `4096` metric rows / `32768` probe rows, `4` concurrent requests, `10s` timeout. Explicit windows: ≤8 days within the 90-day archive; relative range: ≤30 days. Deleting a node removes its history and traffic.

</details>

<details>
<summary><strong>Build from source and upgrade scope</strong></summary>

Requires the Go version in [go.mod](go.mod); Server also needs a C compiler for SQLite.

```sh
git clone https://github.com/Robinproxy/Needle.git
cd Needle
go test ./...
go build -o needle-server ./cmd/server
CGO_ENABLED=0 go build -o needle-agent ./cmd/agent
```

Server supports Linux amd64 / arm64; Agents are static. Dashboard / API fixes: upgrade Server. Collection / transport / config fixes: upgrade Agent. [Release checks](docs/release-preparation.md) · [Load tests](docs/load-testing.md).

</details>

## Credits

- Thanks to [akiasprin](https://github.com/akiasprin) for open-sourcing [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2), which inspired parts of Needle's dashboard design.
- Thanks to [zstaticcdn](https://lf3-ips.zstaticcdn.com/) for providing TCP Ping endpoints.
- Thanks to [OpenCode](https://opencode.ai/), [DeepSeek](https://deepseek.com/), [Grok](https://x.ai/), [GLM](https://zhipuai.cn/), and [OpenAI Codex](https://openai.com/codex/) for development and code-review assistance.
- Thanks to every open-source author, contributor, and Needle user who has shared feedback.

## License

[MIT](https://opensource.org/license/mit)
