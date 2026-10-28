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

Needle consists of one Server and multiple Agents. Agents make outbound-only connections to collect CPU, memory, network traffic, and TCP Ping metrics. The Server stores them in SQLite and serves a read-only web dashboard.

## Features

- CPU, memory, disk, load, uptime, network rates, and monthly upload + download usage
- CPU model, operating system/version, and architecture in Node Detail
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

Deploy one Server and an Agent on each monitored machine. Agents make outbound connections; they need no inbound port. The Docker image contains the Server only.

## Deployment & Operations

Upgrade **Server first, then Agents**, and back up the Server database. The commands below use the default paths. Installers download the latest stable release; use the installer packaged with a release when deliberately installing an older version. Configuration examples follow **Common Paths**.

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

The container runs as UID/GID 1000. Its database directory must belong to that user, with mode `700`. The host port defaults to `127.0.0.1:8008`. Put an HTTPS reverse proxy or Tunnel in front: use `http://127.0.0.1:8008` from the host, or `http://needle-server:8008` from the same Docker network. Agents use the public HTTPS URL.

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

The supplied Compose file rotates this container's logs at `10m` per file, retaining three files (about 30 MB). For an existing deployment, add the `logging` block shown under **Configuration** to the local Compose file, then run `docker compose up -d --force-recreate needle-server`. A binary/image upgrade alone does not edit your Compose file. Rotation applies to the recreated container; export any old container logs you need before recreation. Database/history retention is independent of log rotation.

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

The query runs as the database owner, reads the live database including committed WAL, and needs no Server restart. No output means no matching binding. For a hostname containing a single quote, double that quote in SQL (`O''Brien`). Keep the complete Token private.

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

The helper uses the same UID/GID as the Server and a read-only SQLite connection. The data mount stays writable for SQLite's shared-memory coordination; no database records are changed. The helper container is removed after the query.


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

Upgrade retains `.env`, tokens, data, and supported custom DB / TLS settings. It stops the service and saves a consistent SQLite backup plus original files in `/opt/needle/recovery/migration.*`. Unsupported paths or service overrides stop migration with an explanation. If migration has begun, a failure retains the new database for recovery instead of overwriting it with an older copy. See the [migration and recovery record](docs/p0-p1-validation.md).

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
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh
sudo bash /tmp/needle-agent.sh install
```

Enter the Agent hostname, Server HTTPS URL, a unique Token (Enter generates one), reporting intervals, and TCP Ping mode. New installations set `server_managed: true`; set the display name, region, billing, renewal date, and monthly allowance in the Server `/admin` page after the first report. Run the printed Docker or binary `allow-token` command **on the Server**. The first successful report binds the Token to the hostname.

Root installs and manages the service; the Agent runs as the non-login `needle-agent` user with no capabilities. It cannot modify its binary, configuration, or systemd unit, execute Server commands, or upgrade itself.

| Location | Owner | Mode |
| --- | --- | --- |
| `/opt/needle-agent/` | `root:needle-agent` | `750` |
| `/opt/needle-agent/bin/` and `bin/needle-agent` | `root:root` | `755` |
| `/opt/needle-agent/agent.yaml` | `root:needle-agent` | `640` |
| `/opt/needle-agent/agent.yaml.bak` (if present) | `root:root` | `600` |
| `/etc/systemd/system/needle-agent.service` | `root:root` | `644` |

### Status, logs, and upgrades

Upgrade the Server first. **Quick upgrade — download the latest installer, keep configuration, and restart the Agent:**

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh && sudo bash /tmp/needle-agent.sh upgrade --keep-config && sudo systemctl restart needle-agent
```

Run on the Agent machine. This upgrades to the latest stable release without configuration prompts, preserves the complete YAML and Token, and restarts the service even if it was stopped. Boot autostart settings are unchanged. Each step runs only if the previous one succeeds; no new Token registration is needed.

Other status, log, and upgrade commands (download the installer first if `/tmp` was cleared):

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

`--reconfigure` changes probes and intervals only; region, billing, renewal dates, and traffic allowance are managed in `/admin`. The three flags are mutually exclusive. Non-interactive upgrades keep configuration; reconfiguration requires a terminal. Reinstalling with an existing configuration enters the upgrade flow. Kept configurations do not gain new fields automatically; omitted optional fields use their defaults.

Upgrade migrates older root-run installations to the dedicated user, validates configuration as that user, and preserves running / autostart states. A new installation starts immediately and at boot. Failed deployment restores the old files; failed rollback retains originals under `/opt/needle-agent/recovery/` and prints the recovery path.

For foreground troubleshooting, stop the service to avoid duplicate reports:

```sh
sudo systemctl stop needle-agent
sudo runuser -u needle-agent -- /opt/needle-agent/bin/needle-agent /opt/needle-agent/agent.yaml
# Press Ctrl+C, then resume the service
sudo systemctl start needle-agent
```

### Uninstall

Back up `agent.yaml` first if you intend to reuse its Token; Linux uninstall removes the entire local installation, including configuration, plus the service account and group.

```sh
# Local uninstall; retain the Server credential and node history
sudo bash /tmp/needle-agent.sh uninstall
# Revoke this credential on the Server, then uninstall locally; retain node history
sudo bash /tmp/needle-agent.sh uninstall --unregister
```

`--unregister` requires a Server that confirms history-preserving revocation. Failure retains the local installation. Only the administrator's Server-side `delete-agent` command deletes the node and its history. Add `--yes` to skip uninstall confirmation.

</details>

<details>
<summary><strong>4. Agent — OpenWrt / ImmortalWrt</strong></summary>

### Requirements and installation

Requires root SSH access, procd, BusyBox-compatible `sh`, HTTPS-capable wget or curl, CA certificates, and `sha256sum`. Supports `x86_64` / amd64 and `aarch64` / arm64; no ARM 32-bit or MIPS. GitHub API and Releases must be reachable.

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh
sh /tmp/needle-agent-openwrt.sh install
```

Choose the actual WAN interface (`pppoe-wan`, `eth1`, etc.). Avoid counting both LAN and WAN or a PPPoE interface and its underlying device. Enter the Agent hostname, Server URL, Token, intervals, WAN interface, and probes; allow the printed Token **on the Server**. New installations set `server_managed: true`; complete region, billing, renewal date, and allowance in the Server `/admin` page.

The router installer uses procd and runs the Agent as root; the Linux systemd service-account setup does not apply. It does not change routing, firewall, DNS, DHCP, or package settings. It opens no listening port and writes no local metric history. The service uses nice level 10, `GOMAXPROCS=1`, a 32 MiB soft Go memory target (not an RSS cap), and a 128-file descriptor limit.

### Status, logs, and upgrades

Upgrade the Server first. **Quick upgrade — download the latest installer, keep configuration, and restart the Agent:**

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh && sh /tmp/needle-agent-openwrt.sh upgrade --keep-config && /etc/init.d/needle-agent restart
```

Run as root on the router. This upgrades to the latest stable release without configuration prompts, preserves the complete YAML and Token, and restarts the service even if it was stopped. Boot autostart settings are unchanged. Each step runs only if the previous one succeeds; no new Token registration is needed.

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

Reselection changes probes and intervals only; use `/admin` for region, billing, renewal dates, and traffic allowance. Plain `upgrade` offers an interactive choice, defaulting to keeping configuration. `--keep-config` preserves YAML unchanged; `--reconfigure` changes probes and intervals; `--configure-intervals` changes only intervals. These flags are mutually exclusive and preserve Token and identity. Existing running / autostart states are preserved; a first installation starts immediately and at boot.

The installer checks checksums, binary execution, configuration, and space before replacing files. It requires 64 MiB available memory, 32 MiB free in `/tmp`, and a 4 MiB installation-space reserve after staging. Startup failure restores the old files and service state; a failed recovery retains its snapshot and prints the location. A running process alone does not prove successful reporting: check logs and the Server dashboard.

For a busy router, use `interval: 30` or longer, consider `realtime_interval: 0`, and enable only the interfaces and probes you need.

### Uninstall and firmware upgrades

```sh
# Remove binary/service/version; keep local configuration and Server data
sh /tmp/needle-agent-openwrt.sh uninstall
# Also delete local configuration and Needle's firmware-backup entry
sh /tmp/needle-agent-openwrt.sh uninstall --purge
# Revoke the Server credential and remove all local Needle files; keep Server history
sh /tmp/needle-agent-openwrt.sh uninstall --unregister --purge
```

Add `--yes` to skip confirmation. Revocation requires a compatible Server; failure retains local files and restores the previous running state. `--purge` removes Needle's configuration, backup, and staging files, retaining unrelated files and backup entries. Delete the Server node/history separately with `delete-agent` if required.

Firmware upgrades with settings retention preserve `/etc/needle/` through `/etc/sysupgrade.conf`. Reinstall the binary and service afterward; retained configuration is reused.

</details>

<details>
<summary><strong>Dashboard — Administrator and node settings</strong></summary>

The dashboard and its monitoring APIs remain public. `/admin` adds authenticated editing of display name, note, region, position, billing currency/amount/period, renewal date, and monthly upload + download allowance. An Agent's stable hostname, Token, collectors, and local YAML are not changed by this page.

Billing accepts `USD`, `CNY`, `EUR`, `GBP`, `CAD`, `HKD`, `JPY`, `AUD`, or another three-letter currency code, without exchange conversion. Enter the amount for the entire payment period: 0 means Free, empty means Not set. Periods are monthly, quarterly, half-yearly, yearly, every 2 or 3 years, or permanent. Use the actual next renewal / top-up date, including for transferred nodes. Monthly traffic is an independent upload + download allowance in decimal GB (1000 GB = 1 TB); 0 displays ♾️ and does not block traffic.

Initialize one administrator **on the Server host**, after starting the Server. Use its actual database path:

```sh
# Docker: run from the Server Compose directory
cd ~/needle
docker compose exec needle-server needle-server -db /data/needle.db admin-init

# Linux binary / systemd
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db admin-init
```

Run only the command matching your deployment. The interactive setup asks for a username (default `admin`), the **browser-facing origin** (e.g. `https://needle.example.com`, without `/admin` or any other path), and a password of at least 15 characters, entered twice without echo. HTTPS is required; loopback HTTP is allowed for local testing. No password is accepted in command arguments, environment variables, or piped input. No public registration or initialization endpoint exists; editing stays disabled until CLI initialization succeeds.

The configured origin is used for request validation and secure cookies behind HTTPS reverse proxies; forwarded headers are not trusted to select it. Keep the public origin stable and forward browser `Origin` headers unchanged. Open **Manage** in the dashboard header, sign in, and select **Edit** beside a node. Sessions last eight hours, end on sign-out, and are lost when the Server restarts. Passwords are stored as salted Argon2id hashes in the existing database.

Reset a forgotten password, change the username, or update the public origin interactively. Existing sessions are invalidated without restarting the Server:

```sh
# Docker, from ~/needle
docker compose exec needle-server needle-server -db /data/needle.db admin-reset

# Linux binary
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db admin-reset
```

Upgrade compatibility and ownership:

- Existing values populate the editor. Saving in `/admin`, or receiving the first report from the current Server-managed Agent, transfers an existing static profile to the Server without overwriting its saved values. Later reports cannot overwrite it. Before upgrading an older Agent, remove the retired static YAML fields listed in the Agent configuration sections; keep its Token and identity.
- New Agents installed with `server_managed: true` use Server-managed profiles from their first report. Missing billing and renewal settings show **Not set**, not Free or permanent. Monthly usage starts with reset day 1 and an unrestricted allowance; choose a finite allowance or ♾️ in the editor.
- A display name is a label. CLI commands and Token bindings continue to use the original Agent hostname or stable numeric ID.
- An optional note accepts up to 40 characters of single-line plain text. It appears beside uptime below the card's name, truncates to fit, and is hidden when empty. Clear it in the editor to remove it. Notes are stored on the Server; no Agent upgrade or YAML change is needed.
- On takeover, the current monthly reset day is pinned independently of the renewal date. For a previously unconfigured node it defaults to day 1. The editor shows this day read-only. Changing price, payment period, renewal date, or allowance does not clear usage or move the monthly boundary. Stored historical billing and traffic records retain their existing boundaries during migration.
- The saved renewal anchor still projects future reminders by the payment period; it does not confirm payment. The editor shows both the saved date and projected next renewal. Short months keep the original reset day (29–31) for subsequent months.
- Saves validate every field, use a database transaction, reject stale forms, refresh the public dashboard, and retain the latest 200 profile-change records in SQLite. A failed save does not partially change ordering or billing. Existing history and counter accounting remain intact.

Administrator cookies and Agent Tokens are separate credentials. The management API accepts only authenticated, same-origin JSON writes with CSRF protection. It provides profile editing only; Token registration, node deletion, and Agent service/configuration management remain terminal operations. Database backups contain the password hash as well as Agent credentials and should remain private.

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
| Logs | `journalctl -u needle-agent` |

The configuration contains the Token; root manages the file and the service user can only read it. Historical data lives on the Server; the Agent retry queue is memory-only.

Linux Server and Agent logs use the existing system journal and its retention policy; Needle creates no separate log files and does not change global journald settings. Use `sudo journalctl -u needle-agent --since "1 hour ago" --no-pager` (or `-u needle-server`) for recent logs.

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
| Logs | `logread -e needle-agent` |

The configuration contains the Token. Normal uninstall retains it; `--purge` removes it. Recovery snapshots are temporary; a failed recovery prints the retained path.

The Agent sends output to the system logger. With OpenWrt's default memory ring buffer, old entries are overwritten and logs do not survive reboot. Needle does not enable persistent flash logging or change the router's global log buffer. Use `logread -e needle-agent` or `logread -f -e needle-agent` to view or follow entries.

</details>

## Configuration

<details>
<summary><strong>Realtime updates and historical sampling</strong></summary>

```yaml
realtime_interval: 5 # Extra live-only reports, seconds; 0 disables
interval: 30        # Full history, TCP Ping, and monthly traffic accounting
```

Fresh installs use `5 / 30`. Old YAML without `realtime_interval` uses `0 / interval`; `--keep-config` does not enable it. Enabled realtime must be an integer from 1 through `interval`. Edit YAML and restart, or use `upgrade --configure-intervals`.

Realtime updates memory only; full reports save history and update billing / traffic. The dashboard normally uses SSE and falls back to 30-second polling when unavailable. Raw history remains seven days and hourly history 90 days, regardless of realtime frequency.

Full reports retry only after confirming the Server's persistent-deduplication capability. Default buffering is 64 reports including the in-flight report, independently capped at 8 MiB; retries expire 1800 seconds after sampling. The queue is memory-only, so restarting the Agent loses unsent reports. Increasing capacity does not guarantee recovery of an entire outage. See [validation details](docs/p0-p1-validation.md) for protocol and retry behavior.

</details>

<details>
<summary><strong>Traffic counter continuity and migration</strong></summary>

Full reports include kernel boot identity and the counted interface scope. The Server uses counter deltas within a stable scope, preserves high watermarks on counter decreases, and rebases when interfaces change. A confirmed newer boot can add its counters without erasing already accumulated usage.

The first strong-identity report after migration retains existing usage and establishes a new baseline. Ambiguous transitions can undercount; they never justify adding an entire existing interface counter as new usage. Needle cannot recover traffic from before monitoring, missing pre-reboot counters, or an unobserved monthly boundary. Changing the allowance does not reset usage. No automatic historical correction is performed; see the recovery reference below.

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

Update the host proxy / Tunnel origin to `http://127.0.0.1:8088`. Compose `.env` supplies interpolation values; it is separate from the binary Server's environment file. Recreate with `up -d` after changing Compose settings; `restart` alone does not apply them. Keep the Server timezone stable because monthly boundaries use it.

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

The binary does not read `.env` itself; systemd loads it. The supplied unit uses `-l` and `-db`; putting TLS filenames in `.env` does not enable HTTPS. Prefer the default service behind an HTTPS proxy / Tunnel. Native TLS requires explicitly configuring both flags in the service or a manual invocation:

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

Example; replace hostname, URL, Token, and interfaces for your machine. Configure billing and other static node settings in `/admin`. See also [agent.yaml.example](agent.yaml.example).

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

Configure region, billing currency/amount/period, renewal date, and traffic allowance in `/admin`. Amounts cover the entire payment period; 0 means Free and an empty amount means Not set. Renewal dates project reminders by the payment period. Monthly traffic uses the independent pinned reset day, including for annual and permanent nodes; changing billing or the allowance does not clear usage. An allowance of 0 displays ♾️.

The former YAML fields `region`, `billing_currency`, `billing_amount`, `billing_period`, `expires_at`, and `monthly_traffic_limit_gb` are no longer accepted. Save their values in `/admin` and remove them from YAML before upgrading. Replace `insecure` with `tls_skip_verify` if present.

| Installer mode | Targets |
| --- | --- |
| `1) IPv4 only` (default) | CMv4, CUv4, CTv4 |
| `2) IPv6 only` | CMv6, CUv6, CTv6 |
| `3) Dual stack` | All six presets |
| `4) Custom` | Selected presets and / or custom TCP targets |
| `5) Disabled` | No probes; system monitoring continues |

There is no connectivity auto-detection. Choose according to actual outbound IPv4 / IPv6 availability. To disable probes manually, replace the whole list with `tcpping: []`. Disabled targets disappear from detail charts while old history expires normally; cards retain `TCP Ping · Off`, and detail range controls remain available.

`--keep-config` preserves the file unchanged. `--reconfigure` asks for probes and intervals; `--configure-intervals` changes only the two intervals. Token, hostname, URL, and interfaces are preserved; changed settings are backed up to `agent.yaml.bak`.

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

Settings apply after restart; there is no hot reload or remote configuration. Check logs and the dashboard after the next full report. Keep Tokens and configuration files private.

</details>

<details>
<summary><strong>Agent — OpenWrt / ImmortalWrt</strong></summary>

Edit the configuration as root (`nano` if installed, otherwise `vi`):

```sh
nano /etc/needle/agent.yaml
```

Example; replace hostname, URL, Token, and interfaces for your machine. Configure billing and other static node settings in `/admin`. See also [agent.yaml.example](agent.yaml.example).

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

Configure region, billing currency/amount/period, renewal date, and traffic allowance in `/admin`. Amounts cover the entire payment period; 0 means Free and an empty amount means Not set. Renewal dates project reminders by the payment period. Monthly traffic uses the independent pinned reset day, including for annual and permanent nodes; changing billing or the allowance does not clear usage. An allowance of 0 displays ♾️.

The former YAML fields `region`, `billing_currency`, `billing_amount`, `billing_period`, `expires_at`, and `monthly_traffic_limit_gb` are no longer accepted. Save their values in `/admin` and remove them from YAML before upgrading. Replace `insecure` with `tls_skip_verify` if present.

The installer recommends WAN's `l3_device` when available. Confirm it exists; multi-WAN can use multiple distinct WAN interfaces. Do not list both PPPoE and its underlying device, or LAN and WAN for the same traffic. `realtime_interval: 0` and disabled TCP Ping are optional low-overhead choices.

| Installer mode | Targets |
| --- | --- |
| `1) IPv4 only` (default) | CMv4, CUv4, CTv4 |
| `2) IPv6 only` | CMv6, CUv6, CTv6 |
| `3) Dual stack` | All six presets |
| `4) Custom` | Selected presets and / or custom TCP targets |
| `5) Disabled` | No probes; system monitoring continues |

There is no connectivity auto-detection. Choose according to actual outbound IPv4 / IPv6 availability. To disable probes manually, replace the whole list with `tcpping: []`. Disabled targets disappear from detail charts while old history expires normally; cards retain `TCP Ping · Off`, and detail range controls remain available.

`--keep-config` preserves the file unchanged. `--reconfigure` asks for probes and intervals; `--configure-intervals` changes only the two intervals. Token, hostname, URL, and interfaces are preserved; changed settings are backed up to `agent.yaml.bak`.

Validate before restarting; this check does not send probes or reports:

```sh
/usr/bin/needle-agent -check-config /etc/needle/agent.yaml &&
/etc/init.d/needle-agent restart
logread -e needle-agent

# Read the full Token locally
grep '^token:' /etc/needle/agent.yaml
```

Settings apply after restart; there is no hot reload or remote configuration. Check logs and the dashboard after the next full report. Keep Tokens and configuration files private.

</details>

## Reference

<details>
<summary><strong>Node cards and live probe results</strong></summary>

Cards show system usage, live upload / download rates, monthly totals, allowance, configured billing cost, and next scheduled renewal. Permanent dates and unmetered allowances display ♾️. Probe columns keep v4 left and v6 right; disabled probes retain a muted `TCP Ping · Off` area.

Click the **calendar / up-arrow** button first in the header controls (renewal sort, view, theme) to sort cards or list rows by the next renewal date, soonest first; click again to restore the saved manual order. A stronger icon color indicates renewal sorting is active. Permanent nodes and nodes without a valid date appear last. Ties keep manual order, offline nodes are included, and region filters still apply. The current browser remembers the mode across reloads and live updates. Sorting uses the Server's projected next renewal date; it does not change stored node positions, payment dates, or monthly traffic boundaries. This sorting feature requires only a Server update.

Node Detail shows the CPU model, OS/version, and architecture in a muted line below its title, for example `Intel N150 · ImmortalWrt 24.10.4 · x86_64`. Long text wraps so the full information is readable directly. These details appear only in Node Detail, without hover popups; cards retain their existing height and core/load line. Missing items are omitted without placeholders.

Upgrade the Server first, then Agents to populate these fields; use `upgrade --keep-config` after removing retired YAML keys as described in the upgrade checklist. No collector-specific YAML settings or Token re-registration are needed. The Agent reads system information once at startup and attaches it to full reports, not realtime requests or history rows. Restart the Agent after an OS upgrade to refresh it. Model names reflect what the host or hypervisor exposes. Metadata read failures are logged without disabling live CPU collection, and omitted/empty fields from older Agents preserve saved values.

`4C` means four logical CPUs; `Load 0.14` is the one-minute system load average, not CPU percentage. Live TCP loss reflects the latest attempt (`0%` or `100%`); detail statistics summarize the selected history window. Click detail targets to toggle chart lines; this does not change Agent configuration.

</details>

<details>
<summary><strong>Upgrade checklist</strong></summary>

For current releases, use this checklist:

1. Back up the database and upgrade the Server first. Review the [migration record](docs/p0-p1-validation.md) when upgrading an older installation.
2. Confirm the static settings in `/admin`, then remove the retired region/billing/date/allowance fields from Agent YAML. Replace `insecure` with `tls_skip_verify` if present. Configuration validation rejects these old keys with an actionable error.
3. Download the matching Agent installer and run `upgrade --keep-config` on Linux or OpenWrt. Token, hostname, interfaces, and probes are retained; no re-registration is needed. Use `--configure-intervals` / `--reconfigure` only to change local monitoring settings. Missing `realtime_interval` remains disabled; retry settings have defaults.
4. Validate edited YAML, restart, and confirm reports in logs and the dashboard. Upgrade does not require re-entering an unchanged renewal date.

Use compatible binaries and installers from the same release. After database migration, avoid downgrading to a version that does not understand the current accounting and history schema.

</details>

<details>
<summary><strong>Dashboard refresh troubleshooting</strong></summary>

The dashboard uses SSE, with 30-second polling while SSE is unavailable. A failed refresh retains the last successful data. Retry requests a new render; ordinary requests time out after 15 seconds.

If failures recur, check the failing endpoint in browser developer tools, Server logs, and `/api/health`; then check proxy / Tunnel connectivity. The proxy must permit the long-lived `/api/events` stream. A healthy internal endpoint does not prove public access is working. History panels have independent Retry controls.

Runtime messages use `INFO`, `WARN`, or `ERROR` with `module` and `event` fields. Startup includes version and source commit. Healthy samples/reports are silent. Continuous failures log immediately, then at most once a minute per tracked operation; `count` is the outage's total failures and `suppressed` counts repeats omitted since the previous message. The same operation's next success emits one `event=recovered` entry. History/realtime collectors are tracked separately; TCP Ping recovery means its permission issue cleared, not that a target became reachable. Rejected credentials are aggregated across hostnames and do not emit recovery on another Agent's successful request.

Tracking is bounded in memory and resets when the process restarts. Minute summaries are emitted on the next failed attempt, without a background timer. Levels are plain-text labels, not a promise of matching system-journal priority filters. Token headers, full configuration, and upstream HTTP response bodies are not logged; HTTP errors include the status instead. Error text is quoted and length-limited. Existing `journalctl`, `logread`, and `docker compose logs` commands remain the way to inspect logs.

</details>

<details>
<summary><strong>Traffic counter regressions and recovery</strong></summary>

Current accounting prevents a counter decrease from being charged as an entire new counter. Existing inflated totals are not automatically rewritten. Modern Agents provide boot / interface identities; legacy Agents use uptime and high watermarks. Missing counters are unavailable, not valid zeros.

`repair-traffic` is a **legacy-only** tool for supported inflation in retained raw history. Strong-identity accumulators refuse legacy repair. Preview first; apply only after confirming the faulty deployment time and the proposed changes. Missing history cannot be reconstructed from hourly maxima. See the [validation record](docs/p0-p1-validation.md).

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

Rollups and cleanup commit together; late samples and repeated maintenance do not duplicate counts. Historical gaps remain gaps. Deleting a node removes its history, rollups, traffic records, and migration evidence. Maintenance and retention details are in the [validation record](docs/p0-p1-validation.md).

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
