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

One Server, multiple Agents, SQLite storage, and a public dashboard with optional administrator editing.

## Features

- CPU, memory, disk, load, uptime, network rates, and monthly upload + download usage
- CPU model, operating system/version, architecture, and virtualization in Node Detail
- Configurable TCP Ping targets for IPv4 / IPv6, with latency, jitter, and loss history
- Billing costs, renewal reminders, monthly traffic allowances, and ♾️ options
- Public dashboard with an optional single-administrator node settings page
- Cards and list views, region filters, themes, persistent node ordering, and live SSE updates
- SQLite storage, embedded dashboard, and unique hostname-bound Agent tokens
- Docker or Linux binary Server; Linux systemd and OpenWrt / ImmortalWrt Agents

## Architecture

```text
Agent A ─┐
Agent B ─┼── HTTPS ──> Needle Server ──> SQLite
Agent C ─┘                   │
                             └── Web Dashboard
```

Install an Agent on each monitored machine; no inbound Agent ports are needed. The Docker image contains only the Server.

## Deployment & Operations

Back up the database and upgrade **Server first, then Agents**. Commands use default paths; omit `sudo` in a root shell. Installers fetch the latest stable release; for an older release, use its packaged installer. Paths and configuration fields are listed below.

<details>
<summary><strong>1. Server — Docker Compose</strong></summary>

### Install

Requires Docker Engine and Docker Compose on Linux.

```sh
mkdir -p ~/needle
sudo install -d -o 1000 -g 1000 -m 700 ~/needle/data
cd ~/needle
curl -fsSLO https://raw.githubusercontent.com/Robinproxy/Needle/main/docker-compose.yml
docker compose up -d
```

The container uses UID/GID 1000 and a mode-`700` database directory. Put HTTPS in front of `http://127.0.0.1:8008` (host proxy) or `http://needle-server:8008` (same Docker network). Agents use the public HTTPS URL.

### Status, logs, and upgrades

Run from `~/needle`. Adjust the health-check port if you changed `NEEDLE_PORT`.

```sh
docker compose ps
docker compose logs --tail=100 needle-server
docker compose logs -f --tail=100 needle-server
docker compose logs --since=1h --tail=100 needle-server
curl -fsS http://127.0.0.1:8008/api/health
docker compose exec needle-server needle-server -version

# Pull, stop, back up the complete SQLite directory, and upgrade
# Each step runs only if the preceding step succeeded
docker compose pull needle-server &&
docker compose stop needle-server &&
sudo cp -a data "data.backup.$(date +%Y%m%d-%H%M%S)" &&
docker compose up -d needle-server

# Service control
docker compose restart needle-server
docker compose stop needle-server
docker compose start needle-server
```

Upgrade retains the mounted database, registered tokens, and node settings. It does not upgrade remote Agents.

Compose rotates logs at `10m` × 3 files (about 30 MB). For an existing deployment, add the **Configuration** logging block and run `docker compose up -d --force-recreate needle-server`; image upgrades do not edit Compose. Export needed logs before recreation. Log rotation does not affect database retention.

### Agents and tokens

Run on the Server, from `~/needle`; replace the uppercase placeholders:

```sh
docker compose exec needle-server needle-server -db /data/needle.db allow-token YOUR_TOKEN
docker compose exec needle-server needle-server -db /data/needle.db list-agents
docker compose exec needle-server needle-server -db /data/needle.db list-tokens

# Move a node to position 1, using its hostname or stable ID
docker compose exec needle-server needle-server -db /data/needle.db move-agent Router 1

# Revoke a token; retain the node, history, billing, and traffic
docker compose exec needle-server needle-server -db /data/needle.db revoke-token YOUR_TOKEN

# Delete the node, its bound token, and all associated data
docker compose exec needle-server needle-server -db /data/needle.db delete-agent HOSTNAME_OR_ID
```

`POS` is the display position; `ID` is the stable node identifier. Moving a node shifts the others and applies on the next dashboard update. A deleted or revoked token must be allowed again before its Agent can reconnect.

**Find a VPS's full Token on the Server:** `list-tokens` masks tokens and has no full-output flag. Install `sqlite3` on the **Linux host** if needed (Debian / Ubuntu: `sudo apt-get install sqlite3`); the Server container does not include it. Replace `Malibu` with the exact hostname shown by `list-tokens`:

```sh
cd ~/needle
sudo -u "#$(sudo stat -c '%u' data/needle.db)" \
  sqlite3 -readonly data/needle.db \
  "SELECT hostname, token FROM agent_tokens WHERE hostname = 'Malibu';"
```

Read-only, as the database owner, including committed WAL; no restart needed. No output means no match. Escape hostname quotes as `O''Brien`. Keep Tokens private.

If the host directory is under `/root` or another parent the database owner cannot traverse, use this temporary helper container instead of changing directory permissions. It needs no host `sqlite3`; Docker downloads `python:3.12-alpine` on first use. Replace the final `Malibu` argument with the hostname:

```sh
cd ~/needle
docker run --rm -i --user 1000:1000 \
  --mount "type=bind,src=$(pwd)/data,dst=/data" \
  python:3.12-alpine python - Malibu <<'PYTHON'
import sqlite3, sys
with sqlite3.connect("file:/data/needle.db?mode=ro", uri=True) as db:
    rows = db.execute("SELECT hostname, token FROM agent_tokens WHERE hostname = ?", (sys.argv[1],)).fetchall()
    for hostname, token in rows:
        print(f"{hostname}: {token}")
    if not rows:
        print("No matching hostname")
PYTHON
```

The helper uses the Server UID/GID and read-only SQLite; its writable mount permits WAL coordination. It changes no records and removes itself after the query.


### Backup, restore, and removal

Stop the Server before copying SQLite files; include the entire directory, not only `needle.db`:

```sh
cd ~/needle
docker compose stop needle-server &&
sudo cp -a data "data.backup.$(date +%Y%m%d-%H%M%S)" &&
docker compose start needle-server
```

Restore with the actual backup name. Keep the current data as a fallback:

```sh
cd ~/needle
docker compose down &&
sudo mv data "data.before-restore.$(date +%Y%m%d-%H%M%S)" &&
sudo cp -a data.backup.YYYYMMDD-HHMMSS data &&
docker compose up -d needle-server
```

`docker compose down` removes the containers and network; it retains `~/needle/data`, `.env`, and the Compose file. Backups preserve their original ownership and permissions.

</details>

<details>
<summary><strong>2. Server — Linux Binary / systemd</strong></summary>

### Install

Requires root access, Linux amd64 / arm64, Bash, systemd, Python 3, and standard account / util-linux tools.

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh install
```

The installer creates the non-login `needle-server` user. Root owns the binary, environment, and unit; the service can write only its database directory (`700`, database / WAL / SHM files `600`). New installs listen on `127.0.0.1:8008`; use an HTTPS reverse proxy or Tunnel.

### Status, logs, and upgrades

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

Upgrade preserves `.env`, tokens, data, and supported DB / TLS settings. It stops the service and backs up SQLite and original files to `/opt/needle/recovery/migration.*`. Unsupported paths / overrides stop migration. After migration starts, failures retain the new database for recovery; see the [recovery record](docs/p0-p1-validation.md).

### Agents and tokens

Run on the Server, using the actual database path if customized:

```sh
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token YOUR_TOKEN
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-tokens
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db move-agent Router 1
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db revoke-token YOUR_TOKEN
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent HOSTNAME_OR_ID
```

`move-agent` accepts a hostname or stable `ID`, followed by a one-based position. `revoke-token` retains node data; `delete-agent` removes the node, history, traffic, and bound token. Root CLI commands automatically open SQLite as the database owner.

**Find a VPS's full Token:** install host `sqlite3` if needed (Debian / Ubuntu: `sudo apt-get install sqlite3`). Replace `Malibu` with the exact hostname:

```sh
sudo -u "#$(sudo stat -c '%u' /opt/needle/data/needle.db)" \
  sqlite3 -readonly /opt/needle/data/needle.db \
  "SELECT hostname, token FROM agent_tokens WHERE hostname = 'Malibu';"
```

`list-tokens` shows masked tokens only. This read-only query needs no restart; no output means no matching binding. Double any single quote inside the SQL hostname (`O''Brien`). Keep the complete Token private.

### Backup, restore, and removal

```sh
sudo systemctl stop needle-server &&
sudo cp -a /opt/needle/data "/opt/needle/data.backup.$(date +%Y%m%d-%H%M%S)" &&
sudo systemctl start needle-server
```

Restore a backup while the service is stopped; replace the example backup name:

```sh
sudo systemctl stop needle-server &&
sudo mv /opt/needle/data "/opt/needle/data.before-restore.$(date +%Y%m%d-%H%M%S)" &&
sudo cp -a /opt/needle/data.backup.YYYYMMDD-HHMMSS /opt/needle/data &&
sudo systemctl start needle-server
```

Download the installer again if `/tmp` was cleared:

```sh
# Remove service and binary; retain .env, database, and service account
sudo bash /tmp/needle-server.sh uninstall
# Also delete the default installation, configuration, and database
sudo bash /tmp/needle-server.sh uninstall --purge
```

`--purge` refuses custom data directories. An installer-created account is removed only if it has no remaining owned files or running processes. Use `cp -a` to preserve database ownership and permissions.

</details>

<details>
<summary><strong>3. Agent — Linux Binary / systemd</strong></summary>

### Install and connect

Requires root access, Linux amd64 / arm64, Bash, systemd, account tools (`useradd`, `groupadd`, `userdel`, `groupdel`, `getent`), and util-linux (`runuser`, `flock`).

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh && sudo bash /tmp/needle-agent.sh install
```

This one-line install prompts for hostname, Server HTTPS URL, Token (Enter generates one), intervals, and TCP Ping mode. Run the printed `allow-token` command **on the Server**; the first report binds the Token to the hostname. New installs use `server_managed: true`; set display name, region, billing, renewal date, and allowance in `/admin`.

Root manages installation and systemd. The service runs as non-login `needle-agent`, without capabilities, remote command execution, or self-upgrade; its binary, YAML, and unit are read-only to it.

| Location | Owner | Mode |
| --- | --- | --- |
| `/opt/needle-agent/` | `root:needle-agent` | `750` |
| `/opt/needle-agent/bin/` and `bin/needle-agent` | `root:root` | `755` |
| `/opt/needle-agent/agent.yaml` | `root:needle-agent` | `640` |
| `/opt/needle-agent/agent.yaml.bak` (if present) | `root:root` | `600` |
| `/etc/systemd/system/needle-agent.service` | `root:root` | `644` |

### Status, logs, and upgrades

**One-line upgrade — keep configuration and restart** (upgrade the Server first):

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh && sudo bash /tmp/needle-agent.sh upgrade --keep-config && sudo systemctl restart needle-agent
```

Preserves YAML, Token, and boot settings; no prompts or re-registration. The final restart starts even a previously stopped service. `&&` stops the command on failure.

Status, logs, service controls, and alternative upgrade modes:

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

Choose one mode. Non-interactive upgrades keep YAML; reconfiguration requires a terminal and preserves Token, hostname, URL, and interfaces. Static settings stay in `/admin`. Reinstalling an existing Agent uses the upgrade flow. Kept YAML gains no new fields; omitted optional fields use defaults.

Upgrade migrates root-run services to the dedicated user and validates YAML as that user. Plain upgrades preserve running / boot states; fresh installs start immediately and at boot. Failures restore old files, or retain originals under `/opt/needle-agent/recovery/` if rollback fails.

For foreground troubleshooting, stop the service to avoid duplicate reports:

```sh
sudo systemctl stop needle-agent
sudo runuser -u needle-agent -- /opt/needle-agent/bin/needle-agent /opt/needle-agent/agent.yaml
# Press Ctrl+C, then resume the service
sudo systemctl start needle-agent
```

### Uninstall

**One-line complete local uninstall — deletes configuration and Token without confirmation:**

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh && sudo bash /tmp/needle-agent.sh uninstall --yes && rm -f /tmp/needle-agent.sh
```

Stops/disables the service; removes its unit, `/opt/needle-agent/` (binary, YAML, backups, recovery files), dedicated user/group, and downloaded installer. Back up YAML first to reuse the Token. System journal history and the empty `/run/needle-agent-install.lock` remain under system retention / reboot cleanup.

Other modes (download the installer first if needed):

```sh
# Local uninstall; retain the Server credential and node history
sudo bash /tmp/needle-agent.sh uninstall
# Revoke this credential on the Server, then uninstall locally; retain node history
sudo bash /tmp/needle-agent.sh uninstall --unregister
```

Local uninstall retains Server tokens and history. `--unregister` revokes the credential while retaining history; an incompatible / unreachable Server leaves the local installation intact. Use Server-side `delete-agent HOSTNAME_OR_ID` to remove the node, token, and history. Add `--yes` to skip confirmation.

</details>

<details>
<summary><strong>4. Agent — OpenWrt / ImmortalWrt</strong></summary>

### Requirements and installation

Requires root SSH access, procd, BusyBox-compatible `sh`, HTTPS-capable wget or curl, CA certificates, and `sha256sum`. Supports `x86_64` / amd64 and `aarch64` / arm64; no ARM 32-bit or MIPS. GitHub API and Releases must be reachable.

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh && sh /tmp/needle-agent-openwrt.sh install
```

Run this one-line install as root. Enter hostname, Server URL, Token, intervals, actual WAN interface (`pppoe-wan`, `eth1`, etc.), and probes; run the printed `allow-token` command **on the Server**. New installs use `server_managed: true`; configure static settings in `/admin`. Do not count both LAN/WAN or PPPoE/its underlying device.

The router installer uses procd and runs the Agent as root; the Linux systemd service-account setup does not apply. It does not change routing, firewall, DNS, DHCP, or package settings. It opens no listening port and writes no local metric history. The service uses nice level 10, `GOMAXPROCS=1`, a 32 MiB soft Go memory target (not an RSS cap), and a 128-file descriptor limit.

### Status, logs, and upgrades

**One-line upgrade — keep configuration and restart** (upgrade the Server first):

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh && sh /tmp/needle-agent-openwrt.sh upgrade --keep-config && /etc/init.d/needle-agent restart
```

Run as root. Preserves YAML, Token, and boot settings; no prompts or re-registration. The final restart starts even a previously stopped service. `&&` stops the command on failure.

Other status, log, and upgrade commands (re-download the installer after `/tmp` is cleared):

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

Plain `upgrade` offers a choice, defaulting to keeping YAML. Choose one flag: `--keep-config`, `--reconfigure` (probes + intervals), or `--configure-intervals`. Token and identity are preserved; static settings stay in `/admin`. Plain upgrades preserve running / boot states; fresh installs start immediately and at boot.

Before replacing files, the installer checks checksums, execution, YAML, and space: 64 MiB available RAM, 32 MiB free in `/tmp`, and a 4 MiB installation reserve. Startup failure rolls back; failed rollback retains its snapshot and prints the path. Confirm reporting in logs and the dashboard.

For a busy router, use `interval: 30` or longer, consider `realtime_interval: 0`, and enable only the interfaces and probes you need.

### Uninstall and firmware upgrades

**One-line complete local uninstall — deletes configuration and Token without confirmation:**

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh && sh /tmp/needle-agent-openwrt.sh uninstall --purge --yes && rm -f /tmp/needle-agent-openwrt.sh
```

Stops/disables the service; removes binary, init script, boot links, version, YAML/backups/staging files, Needle's `/etc/sysupgrade.conf` entry, and the downloaded installer. Removes `/etc/needle/` when empty; unrelated files and backup entries are retained. Router network settings and system log retention are unchanged.

Other modes (re-download the installer if `/tmp` was cleared):

```sh
# Remove binary/service/version; keep local configuration and Server data
sh /tmp/needle-agent-openwrt.sh uninstall
# Also delete local configuration and Needle's firmware-backup entry
sh /tmp/needle-agent-openwrt.sh uninstall --purge
# Revoke the Server credential and remove all local Needle files; keep Server history
sh /tmp/needle-agent-openwrt.sh uninstall --unregister --purge
```

Local uninstall retains Server tokens and history. `--unregister` revokes only the credential; failure retains local files and restores the prior running state. Add `--yes` to skip confirmation. Use Server-side `delete-agent HOSTNAME_OR_ID` to remove the node, token, and history.

Firmware upgrades with settings retention preserve `/etc/needle/` through `/etc/sysupgrade.conf`. Reinstall the binary and service afterward; retained configuration is reused.

</details>

<details>
<summary><strong>Dashboard — Administrator and node settings</strong></summary>

Dashboard / monitoring APIs are public. `/admin` edits display name, note, region, position, billing currency/amount/period, renewal date, and monthly allowance. Agent identity, Token, and local collection settings remain on the Agent.

Currencies: `USD`, `CNY`, `EUR`, `GBP`, `CAD`, `HKD`, `JPY`, `AUD`, or another three-letter code; no conversion. Amount covers the whole period: 0 = Free, empty = Not set. Periods: 1, 3, 6, 12, 24, 36 months or permanent. Enter the actual next renewal / top-up date. Monthly upload + download uses decimal GB (1000 GB = 1 TB); allowance 0 = ♾️. Allowances are display-only limits.

Initialize one administrator **on the Server host**, after starting the Server. Use its actual database path:

```sh
# Docker: run from the Server Compose directory
cd ~/needle
docker compose exec needle-server needle-server -db /data/needle.db admin-init

# Linux binary / systemd
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db admin-init
```

Run the matching command. Enter username (default `admin`), browser origin (`https://needle.example.com`, no path), and a password of at least 15 characters twice; hidden input displays nothing. HTTPS is required except for loopback testing. Passwords must be entered interactively, not through arguments, environment, or pipes. Only CLI setup enables editing; there is no public registration.

Keep the configured origin stable and forward browser `Origin` unchanged; forwarded headers cannot override it. Open **Manage**, sign in, then **Edit** a node. Sessions expire after eight hours, sign-out, or restart. SQLite stores salted Argon2id password hashes.

Reset password, username, or origin interactively; this invalidates sessions without a restart:

```sh
# Docker, from ~/needle
docker compose exec needle-server needle-server -db /data/needle.db admin-reset

# Linux binary
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db admin-reset
```

Upgrade compatibility and ownership:

- Saving or receiving a current Agent's first report transfers static settings to Server ownership, preserving saved values. Later Agent reports cannot overwrite them. Remove retired YAML fields before upgrading older Agents.
- New `server_managed: true` Agents start with billing / renewal **Not set**, reset day 1, and unlimited allowance (♾️). Complete settings in the editor.
- Display name is a label; CLI and Token bindings use the original hostname or stable numeric ID.
- Note: up to 40 single-line characters, shown beside card uptime; truncated to fit, hidden when empty. Edit / clear it on the Server.
- Monthly reset day is pinned on takeover (default 1), read-only, and independent of renewal. Editing price, period, date, or allowance never clears usage or moves cycle boundaries. Short months retain the original day (29–31) for later months.
- Renewal dates project future reminders by payment period; they do not confirm payment. The editor shows the saved anchor and projected next date.
- Saves are validated and transactional, reject stale forms, refresh the dashboard, and retain the latest 200 change records. Migration preserves existing history and accounting boundaries.

Admin sessions and Agent Tokens are separate. Editing requires same-origin authenticated JSON with CSRF protection. Token registration, node deletion, and Agent management remain CLI operations. Database backups contain credentials and password hashes; keep them private.

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

Host paths assume the deployment directory above. WAL / SHM files may appear only while SQLite needs them; back up the whole directory with the Server stopped.

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

Custom database paths remain in the service's `ExecStart`; inspect with `sudo systemctl cat needle-server`.

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

Root manages Token-bearing YAML; the Agent can only read it. History lives on the Server; Agent retries are memory-only.

Linux logs use system journal retention, with no separate files or global journald changes. Recent logs: `sudo journalctl -u needle-agent --since "1 hour ago" --no-pager` (or `-u needle-server`).

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

The configuration contains the Token. Normal uninstall retains it; `--purge` removes it. Recovery snapshots are temporary; a failed recovery prints the retained path.

Logs use OpenWrt's system logger; its default RAM ring buffer overwrites old entries and clears on reboot. No flash logging / global buffer changes. View with `logread -e needle-agent`; follow with `logread -f -e needle-agent`.

</details>

## Configuration

<details>
<summary><strong>Realtime updates and historical sampling</strong></summary>

```yaml
realtime_interval: 5 # Extra live-only reports, seconds; 0 disables
interval: 30        # Full history, TCP Ping, and monthly traffic accounting
```

Fresh installs use `5 / 30`; omitted `realtime_interval` stays 0, including after `--keep-config`. Enabled realtime must be an integer from 1 to `interval`. Edit and restart, or use `upgrade --configure-intervals`.

Realtime updates memory only; full reports save history and traffic accounting. SSE falls back to 30-second polling. Retention stays seven days raw / 90 days hourly.

Retries require Server persistent deduplication. Defaults: 64 reports including in-flight, an independent 8 MiB cap, and 1800-second sample age. The memory-only queue is lost on restart and may not cover a full outage. See [protocol details](docs/p0-p1-validation.md).

</details>

<details>
<summary><strong>Traffic counter continuity and migration</strong></summary>

Full reports carry boot / interface identities. Stable-scope counter deltas accumulate usage; decreases retain high watermarks, interface changes rebase, and confirmed newer boots add new counters without clearing prior usage.

Migration preserves usage and establishes an identity baseline. Ambiguous transitions may undercount, but never add an entire old counter. Pre-monitoring traffic, missing pre-reboot samples, and unobserved monthly boundaries cannot be reconstructed. Allowance edits do not reset usage; historical repair is manual.

</details>

<details>
<summary><strong>Server — Docker Compose settings</strong></summary>

Edit `~/needle/docker-compose.yml`; the supplied configuration is:

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
| `image` | `latest` follows published builds; use a published version tag to pin a release |
| `ports` | Host loopback only; left port is the host, right port is the container |
| `NEEDLE_PORT` | Optional Compose substitution, default `8008`; set in `~/needle/.env` |
| `NEEDLE_LISTEN` | Container listener, `:8008`; keep aligned with the mapping and health check |
| `volumes` | `./data` on the host maps to `/data`; database persists across recreation |
| `restart` | `unless-stopped`: restart unless explicitly stopped |
| `logging.driver` | `json-file`; logs remain available through `docker compose logs` |
| `logging.options.max-size` | Rotate after `10m` per file; use a quoted string |
| `logging.options.max-file` | Keep `"3"` files, including the current file; oldest files are removed on rotation |
| `healthcheck` | Internal `/api/health` check; does not verify a public proxy or Agent connectivity |

For host port 8088:

```sh
cd ~/needle
nano .env
# Add or edit: NEEDLE_PORT=8088
docker compose config
docker compose up -d needle-server
curl -fsS http://127.0.0.1:8088/api/health
```

Point the host proxy / Tunnel to `http://127.0.0.1:8088`. Compose `.env` supplies substitutions; it is separate from binary configuration. Apply changes with `up -d`, not `restart`. Keep the Server timezone stable for monthly boundaries.

</details>

<details>
<summary><strong>Server — Binary environment and flags</strong></summary>

Edit the installer-managed environment:

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

Systemd loads `.env`; the binary does not. The unit uses `-l` / `-db`; TLS filenames in `.env` do not enable HTTPS. Prefer an HTTPS proxy / Tunnel, or explicitly supply both TLS flags:

```sh
./needle-server -l 127.0.0.1:8443 -db ./data/needle.db -cert /path/to/fullchain.pem -key /path/to/privkey.pem
```

After an intentional unit edit, run `sudo systemctl daemon-reload` and restart. Do not start a second Server against the live database. Keep the Server timezone stable; billing and traffic reset dates use that timezone.

</details>

<details>
<summary><strong>Agent — Linux Binary / systemd</strong></summary>

Edit the configuration as root (`nano` if installed, otherwise `vi`):

```sh
sudo nano /opt/needle-agent/agent.yaml
```

Replace hostname, URL, Token, and interfaces; static settings belong in `/admin`. See [agent.yaml.example](agent.yaml.example).

```yaml
hostname: "my-vps"
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

| Field | Meaning / default |
| --- | --- |
| `hostname` | Stable Agent identity; empty / omitted uses the system hostname. Token binds to it; edit the display name in `/admin` |
| `server` | Server URL; use the public HTTPS address, e.g. `https://needle.example.com` |
| `token` | Unique allowed credential; required, 1–256 characters, no whitespace or control characters |
| `server_managed` | Boolean; defaults to `true`. Static settings belong to `/admin`; `false` is rejected |
| `realtime_interval` | Integer seconds: `0` disables extra live reports; otherwise 1–`interval`. New installs use 5; omitted = 0 |
| `interval` | Full report / history / TCP Ping / traffic accounting interval in seconds; default 30, maximum 86400; legacy non-positive values use 30 |
| `history_queue_capacity` | Optional integer, 1–1024; omitted / 0 = 64, including the in-flight report; separate 8 MiB queue limit |
| `history_retry_max_age_seconds` | Optional integer, 1–86400; omitted / 0 = 1800, counted from original sample time |
| `interfaces` | Exact NIC whitelist, e.g. `["eth0"]`; omitted / empty counts automatically selected physical interfaces, excluding loopback and common virtual / VPN devices |
| `tls_skip_verify` | Default `false`; `true` skips HTTPS certificate verification |
| `allow_plain_http` | Default `false`; must be `true` for remote HTTP. Loopback HTTP works without it |
| `tcpping` | Named TCP targets; `[]` / omitted disables probes. Maximum 32; unique names of 1–64 characters, `host:port`, port 1–65535; IPv6 literal format `[2001:db8::1]:443` |

In `/admin`, configure region, billing currency/amount/period, renewal date, and allowance. Amount: whole period, 0 = Free, empty = Not set. Allowance: 0 = ♾️. Traffic resets monthly on its pinned day, independent of payment period / renewal; editing these settings does not clear usage.

The former YAML fields `region`, `billing_currency`, `billing_amount`, `billing_period`, `expires_at`, and `monthly_traffic_limit_gb` are no longer accepted. Save their values in `/admin` and remove them from YAML before upgrading. Replace `insecure` with `tls_skip_verify` if present.

| Installer mode | Targets |
| --- | --- |
| `1) IPv4 only` (default) | CMv4, CUv4, CTv4 |
| `2) IPv6 only` | CMv6, CUv6, CTv6 |
| `3) Dual stack` | All six presets |
| `4) Custom` | Selected presets and / or custom TCP targets |
| `5) Disabled` | No probes; system monitoring continues |

Choose based on outbound IPv4 / IPv6 availability; no auto-detection. `tcpping: []` disables probes and hides their detail chart; cards keep `TCP Ping · Off`, range controls remain, and old history expires normally.

Upgrade flags preserve Token / identity: `--keep-config` keeps YAML; `--reconfigure` changes probes + intervals; `--configure-intervals` changes intervals only. Reselection backs up YAML to `agent.yaml.bak`.

Validate before restarting; this check does not send probes or reports:

```sh
sudo /opt/needle-agent/bin/needle-agent -check-config /opt/needle-agent/agent.yaml &&
sudo systemctl restart needle-agent
sudo systemctl status needle-agent --no-pager
sudo journalctl -u needle-agent -n 50 --no-pager

# Read the full Token locally
sudo grep '^token:' /opt/needle-agent/agent.yaml
```

To confirm the service user can read the file, run `sudo runuser -u needle-agent -- /opt/needle-agent/bin/needle-agent -check-config /opt/needle-agent/agent.yaml`. Preserve `root:needle-agent` ownership and mode `640` when replacing the file.

Restart to apply changes, then confirm the next full report in logs / dashboard. No hot reload or remote configuration. Keep YAML / Tokens private.

</details>

<details>
<summary><strong>Agent — OpenWrt / ImmortalWrt</strong></summary>

Edit the configuration as root (`nano` if installed, otherwise `vi`):

```sh
nano /etc/needle/agent.yaml
```

Replace hostname, URL, Token, and interfaces; static settings belong in `/admin`. See [agent.yaml.example](agent.yaml.example).

```yaml
hostname: "home-router"
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

| Field | Meaning / default |
| --- | --- |
| `hostname` | Stable Agent identity; empty / omitted uses the system hostname. Token binds to it; edit the display name in `/admin` |
| `server` | Server URL; use the public HTTPS address, e.g. `https://needle.example.com` |
| `token` | Unique allowed credential; required, 1–256 characters, no whitespace or control characters |
| `server_managed` | Boolean; defaults to `true`. Static settings belong to `/admin`; `false` is rejected |
| `realtime_interval` | Integer seconds: `0` disables extra live reports; otherwise 1–`interval`. New installs use 5; omitted = 0 |
| `interval` | Full report / history / TCP Ping / traffic accounting interval in seconds; default 30, maximum 86400; legacy non-positive values use 30 |
| `history_queue_capacity` | Optional integer, 1–1024; omitted / 0 = 64, including the in-flight report; separate 8 MiB queue limit |
| `history_retry_max_age_seconds` | Optional integer, 1–86400; omitted / 0 = 1800, counted from original sample time |
| `interfaces` | Exact NIC whitelist; select the real WAN (`pppoe-wan`, `eth1`, etc.). Omitted / empty uses automatic filtering and can double-count router traffic |
| `tls_skip_verify` | Default `false`; `true` skips HTTPS certificate verification |
| `allow_plain_http` | Default `false`; must be `true` for remote HTTP. Loopback HTTP works without it |
| `tcpping` | Named TCP targets; `[]` / omitted disables probes. Maximum 32; unique names of 1–64 characters, `host:port`, port 1–65535; IPv6 literal format `[2001:db8::1]:443` |

In `/admin`, configure region, billing currency/amount/period, renewal date, and allowance. Amount: whole period, 0 = Free, empty = Not set. Allowance: 0 = ♾️. Traffic resets monthly on its pinned day, independent of payment period / renewal; editing these settings does not clear usage.

The former YAML fields `region`, `billing_currency`, `billing_amount`, `billing_period`, `expires_at`, and `monthly_traffic_limit_gb` are no longer accepted. Save their values in `/admin` and remove them from YAML before upgrading. Replace `insecure` with `tls_skip_verify` if present.

The installer recommends WAN's `l3_device` when available. Confirm it exists; multi-WAN can use multiple distinct WAN interfaces. Do not list both PPPoE and its underlying device, or LAN and WAN for the same traffic. `realtime_interval: 0` and disabled TCP Ping are optional low-overhead choices.

| Installer mode | Targets |
| --- | --- |
| `1) IPv4 only` (default) | CMv4, CUv4, CTv4 |
| `2) IPv6 only` | CMv6, CUv6, CTv6 |
| `3) Dual stack` | All six presets |
| `4) Custom` | Selected presets and / or custom TCP targets |
| `5) Disabled` | No probes; system monitoring continues |

Choose based on outbound IPv4 / IPv6 availability; no auto-detection. `tcpping: []` disables probes and hides their detail chart; cards keep `TCP Ping · Off`, range controls remain, and old history expires normally.

Upgrade flags preserve Token / identity: `--keep-config` keeps YAML; `--reconfigure` changes probes + intervals; `--configure-intervals` changes intervals only. Reselection backs up YAML to `agent.yaml.bak`.

Validate before restarting; this check does not send probes or reports:

```sh
/usr/bin/needle-agent -check-config /etc/needle/agent.yaml &&
/etc/init.d/needle-agent restart
logread -e needle-agent

# Read the full Token locally
grep '^token:' /etc/needle/agent.yaml
```

Restart to apply changes, then confirm the next full report in logs / dashboard. No hot reload or remote configuration. Keep YAML / Tokens private.

</details>

## Reference

<details>
<summary><strong>Node cards and live probe results</strong></summary>

Cards show system usage, live upload / download rates, monthly totals, allowance, configured billing cost, and next scheduled renewal. Permanent dates and unmetered allowances display ♾️. Probe columns keep v4 left and v6 right; disabled probes retain a muted `TCP Ping · Off` area.

The header's **calendar / up-arrow** toggles next-renewal sorting (active = stronger color) and saved manual order. Soonest dates come first; permanent / undated nodes last. Ties retain manual order; offline nodes and region filters still apply. The browser remembers the mode. Sorting changes no saved positions, billing, or traffic boundaries and needs only a Server update.

Node Detail adds a wrapping system line: `AMD EPYC 7B13 · Debian 12 · x86_64 · KVM`. CPU model, OS/version, architecture, and virtualization appear here only, without hover popups or missing-value placeholders.

Upgrade Server then Agents to populate system information; no new YAML / Token registration. It is read at startup and sent with full reports, not realtime or history rows. Restart after OS / environment changes. Hardware names depend on what the host exposes. Read failures are logged without stopping CPU monitoring; omitted / empty metadata preserves saved values.

Linux virtualization uses read-only container markers, DMI, and the existing library; no commands or privileges added. Examples: `KVM`, `QEMU`, `VMware`, `Hyper-V`, `LXC`, `Docker`; host roles add `Host`. Container evidence takes priority over an outer VM. Unknown is omitted, not labeled bare metal. Docker merely installed on a host does not mean the Agent is containerized.

`4C` means four logical CPUs; `Load 0.14` is the one-minute system load average, not CPU percentage. Live TCP loss reflects the latest attempt (`0%` or `100%`); detail statistics summarize the selected history window. Click detail targets to toggle chart lines; this does not change Agent configuration.

</details>

<details>
<summary><strong>Upgrade checklist</strong></summary>

1. Back up SQLite and upgrade Server first; see the [migration record](docs/p0-p1-validation.md) for older installations.
2. Confirm static settings in `/admin`; remove retired YAML keys and replace `insecure` with `tls_skip_verify`.
3. Run the Agent's one-line `upgrade --keep-config` above. Token, hostname, interfaces, and probes stay unchanged. Missing `realtime_interval` stays disabled; retry defaults apply. Use reselection flags only to change collection settings.
4. Validate edited YAML, restart, and confirm reports. Unchanged renewal dates need no re-entry.

Use compatible binaries and installers from the same release. After database migration, avoid downgrading to a version that does not understand the current accounting and history schema.

</details>

<details>
<summary><strong>Dashboard refresh troubleshooting</strong></summary>

SSE falls back to 30-second polling; failed refreshes keep the last good data. Brief interruptions are silent; after 30 seconds of data outage, `Updates paused · Retry` appears beside filters and clears on recovery. First-load errors show a centered Retry. Ordinary requests time out after 15 seconds.

Wake / network return refreshes and reconnects immediately. Background timers stop; canceled older requests cannot overwrite resumed data. Retry keeps filters, details, and history selection; the NEEDLE logo reloads the page.

For repeated failures, check browser requests, Server logs, `/api/health`, and public proxy / Tunnel access. Allow long-lived `/api/events`; internal health alone does not prove public connectivity. History panels retry independently.

Logs use `INFO` / `WARN` / `ERROR`, `module`, and `event`; startup adds version / commit. Healthy reports are silent. Repeated failures log immediately, then at most once per minute per operation: `count` = total failures, `suppressed` = omitted repeats. Recovery emits `event=recovered`. History / realtime tracking is separate; TCP Ping recovery concerns permission errors, not reachability. Credential failures aggregate across hostnames and do not recover on another Agent's success.

Tracking is bounded and resets on restart; summaries appear on the next failure, without background timers. Levels are text labels, not journal priorities. Logs omit Tokens, full YAML, and HTTP response bodies; errors use status codes and bounded quoted text. Read via `journalctl`, `logread`, or `docker compose logs`.

</details>

<details>
<summary><strong>Traffic counter regressions and recovery</strong></summary>

Counter decreases never add a whole new counter. Modern Agents use boot / interface identities; legacy Agents use uptime and high watermarks. Missing counters are unavailable, not zero. Old inflated totals are not rewritten automatically.

`repair-traffic` repairs supported **legacy-only** inflation in retained raw history; strong-identity accumulators refuse it. Preview before applying, using the confirmed faulty deployment time. Hourly maxima cannot reconstruct missing raw samples. See the [validation record](docs/p0-p1-validation.md).

For a binary deployment (replace timestamp and node ID with confirmed values):

```sh
sudo systemctl stop needle-server
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db repair-traffic all
# Run only after reviewing the preview
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db repair-traffic --since CONFIRMED_DEPLOYMENT_RFC3339 --apply HOSTNAME_OR_ID
sudo systemctl start needle-server
```

For Docker, from `~/needle`:

```sh
docker compose stop needle-server
docker compose run --rm --no-deps needle-server -db /data/needle.db repair-traffic all
# Run only after reviewing the preview
docker compose run --rm --no-deps needle-server -db /data/needle.db repair-traffic --since CONFIRMED_DEPLOYMENT_RFC3339 --apply HOSTNAME_OR_ID
docker compose start needle-server
```

Use a timestamp such as `2026-10-18T03:17:38Z` only if it matches your actual faulty deployment. Applying creates a mode-600 `needle.db.traffic-backup-TIMESTAMP.db`; it retains Tokens, metric history, and Agent configuration.

</details>

<details>
<summary><strong>History views and retention</strong></summary>

| View / data | Resolution / retention |
| --- | --- |
| `1d` | Raw samples for the last 24 hours |
| `7d` | 15-minute overview with daily drill-down |
| Date view | Recent calendar day, raw while available |
| `30d` | Hourly trends |
| Raw history | Seven days, retaining complete boundary hours (up to one extra hour) |
| Hourly history | 90 days |
| Monthly traffic records | Old cycle starts beyond 62 days are cleaned, except protected migration records |

Rollups / cleanup commit together; late samples and repeated maintenance do not duplicate counts. Gaps stay gaps. Node deletion removes history, rollups, traffic, and migration evidence. See [retention details](docs/p0-p1-validation.md).

</details>

<details>
<summary><strong>Build from source and upgrade scope</strong></summary>

Use the Go version in [go.mod](go.mod) and a C compiler for the SQLite Server:

```sh
git clone https://github.com/Robinproxy/Needle.git
cd Needle
go test ./...
go build -o needle-server ./cmd/server
CGO_ENABLED=0 go build -o needle-agent ./cmd/agent
```

Release packages target Linux amd64 / arm64; Agents are static builds. Dashboard / API changes generally require a Server upgrade; collector, transport, and local configuration changes require Agent upgrades. See [installer scripts](scripts/), [load tests](docs/load-testing.md), and [release checks](docs/release-preparation.md).

</details>

## Credits

- Thanks to [akiasprin](https://github.com/akiasprin) for open-sourcing [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2), which inspired parts of Needle's dashboard design.
- Thanks to [zstaticcdn](https://lf3-ips.zstaticcdn.com/) for providing TCP Ping endpoints.
- Thanks to [OpenCode](https://opencode.ai/), [DeepSeek](https://deepseek.com/), [Grok](https://x.ai/), [GLM](https://zhipuai.cn/), and [OpenAI Codex](https://openai.com/codex/) for development and code-review assistance.
- Thanks to every open-source author, contributor, and Needle user who has shared feedback.

## License

[MIT](https://opensource.org/license/mit)
