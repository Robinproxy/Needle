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

- CPU usage and logical core count, memory, disk, load, uptime, network rates, and monthly traffic usage
- Explicit IPv4-only, IPv6-only, dual-stack, custom, or disabled TCP Ping; selectable detail chart lines
- `1d` raw history, a downsampled `7d` overview with daily drill-down, and `30d` hourly trends
- Seven-day raw retention and 90-day hourly retention
- Monthly upload + download usage with an optional allowance; accumulation survives Agent reboots and counter resets
- Monthly, quarterly, semiannual, annual, and permanent billing periods; permanent nodes display ♾️
- Node cards with CPU/MEM and DSK/TRF grids, separate live rates and monthly totals, and IPv4/IPv6 probe columns
- Card and list layouts, region filters, themes, anomaly markers, and automatic refresh
- Unique Agent tokens bound to a hostname on the first successful report
- SQLite storage and embedded frontend assets, with no frontend build step
- Docker or binary Server deployment; systemd and OpenWrt / ImmortalWrt Agent installers

## Architecture

```text
Agent A ─┐
Agent B ─┼── HTTPS ──> Needle Server ──> SQLite
Agent C ─┘                   │
                             └── Web Dashboard
```

Agents collect metrics and make outbound connections to the Server; no inbound Agent port is required. The Server stores reports and serves a read-only dashboard. The published Docker image contains the Server; Agents run separately on the machines being monitored.

## Deployment & Operations

Deploy one Server, then install an Agent on each monitored machine. Use the matching section below for installation, upgrades, logs, and removal. Configuration examples are collected under **Configuration** after **Common Paths**.

<details>
<summary><strong>1. Server — Docker Compose</strong></summary>

### Install

Requires Docker Engine and Docker Compose.

```sh
mkdir -p ~/needle/data
cd ~/needle
curl -fsSLO https://raw.githubusercontent.com/Robinproxy/Needle/main/docker-compose.yml
docker compose up -d
```

The default host binding is `127.0.0.1:8008`; data persists in `~/needle/data`. Put an HTTPS reverse proxy or Cloudflare Tunnel in front of the Server. If cloudflared runs on the host, use `http://127.0.0.1:8008` as its origin; if it shares the container network, use `http://needle-server:8008`. Agents connect to the public HTTPS hostname.

### Status, logs, and upgrades

Run these commands from `~/needle`:

```sh
docker compose ps
docker compose logs -f --tail=100 needle-server
curl -fsS http://127.0.0.1:8008/api/health

# Upgrade the Server; database and Agent configurations are retained
docker compose pull
docker compose up -d

# Service control
docker compose restart needle-server
docker compose stop needle-server
docker compose start needle-server
```

### Agents and tokens

After installing an Agent, allow its unique token on the Server:

```sh
docker compose exec needle-server needle-server -db /data/needle.db allow-token YOUR_TOKEN
docker compose exec needle-server needle-server -db /data/needle.db list-agents
docker compose exec needle-server needle-server -db /data/needle.db list-tokens

# Move a node to position 1 (use its hostname or ID, not the POS column)
docker compose exec needle-server needle-server -db /data/needle.db move-agent Router 1

# Revoke a credential and delete its bound node and history
docker compose exec needle-server needle-server -db /data/needle.db revoke-token YOUR_TOKEN
# Delete a node, its history, and its bound token by ID or hostname
docker compose exec needle-server needle-server -db /data/needle.db delete-agent HOSTNAME_OR_ID
```

Deleting a registered node also removes its bound token. Allow the token again if you want that Agent to reconnect.

`list-agents` shows the current position in `POS` and the stable node ID in `ID`. `move-agent HOSTNAME_OR_ID POSITION` shifts the other nodes automatically. Cards and list rows share this order across browsers, including offline nodes; new nodes appear last. Changes take effect on the next dashboard refresh without restarting the Server or changing Agent configuration, tokens, or history.

### Backup, restore, and removal

Stop the Server and copy the entire data directory for a consistent SQLite backup:

```sh
docker compose stop needle-server
cp -a data "data.backup.$(date +%Y%m%d-%H%M%S)"
docker compose start needle-server
```

To restore, replace the placeholder backup name with your actual backup:

```sh
docker compose down
mv data "data.before-restore.$(date +%Y%m%d-%H%M%S)"
cp -a data.backup.YYYYMMDD-HHMMSS data
docker compose up -d
```

`docker compose down` removes the containers and network while retaining the bind-mounted `data` directory.

</details>

<details>
<summary><strong>2. Server — Linux Binary / systemd</strong></summary>

### Install

The installer supports Linux amd64 and arm64 with Bash and systemd. It downloads a release binary and creates the service:

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh install
```

For a local reverse proxy or Tunnel, choose `127.0.0.1:8008` when asked for the listen address. Configure the public HTTPS hostname before connecting Agents.

### Status, logs, and upgrades

Download the script again if it is no longer present in `/tmp`.

```sh
sudo bash /tmp/needle-server.sh status
sudo systemctl status needle-server --no-pager
sudo journalctl -u needle-server -f
curl -fsS http://127.0.0.1:8008/api/health

# Upgrade, preserving .env and data/
sudo bash /tmp/needle-server.sh upgrade

sudo systemctl restart needle-server
sudo systemctl stop needle-server
sudo systemctl start needle-server
```

### Agents and tokens

Run on the Server machine:

```sh
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token YOUR_TOKEN
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-tokens
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db move-agent Router 1
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db revoke-token YOUR_TOKEN
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent HOSTNAME_OR_ID
```

Revoking a token deletes its bound node data. Deleting a node also removes its bound token; allow the token again before reconnecting the Agent.

`list-agents` shows `POS` (current position) separately from `ID` (stable node ID). Use `move-agent HOSTNAME_OR_ID POSITION` to move a node to a one-based position; other nodes shift automatically. Cards and list rows use the same persistent order, offline nodes keep their positions, and new nodes appear last. The next dashboard refresh applies the change without a Server restart or Agent changes.

### Backup, restore, and removal

```sh
sudo systemctl stop needle-server
sudo cp -a /opt/needle/data "/opt/needle/data.backup.$(date +%Y%m%d-%H%M%S)"
sudo systemctl start needle-server
```

To restore, stop the service, move the current data directory aside, copy a backup back to `/opt/needle/data`, then start the service. Preserve ownership and permissions with `cp -a`.

```sh
# Remove service and binary; keep .env and data/
sudo bash /tmp/needle-server.sh uninstall
# Also delete configuration and database
sudo bash /tmp/needle-server.sh uninstall --purge
```

</details>

<details>
<summary><strong>3. Agent — Linux Binary / systemd</strong></summary>

### Install and connect

Run on each monitored Linux amd64 or arm64 machine using Bash and systemd:

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh
sudo bash /tmp/needle-agent.sh install
```

Run the installer and service-management commands as root. The installer requires the standard Linux account tools (`useradd`, `groupadd`, `userdel`, `groupdel`, `getent`) and util-linux (`runuser`, `flock`). It creates a dedicated `needle-agent` system account with no home directory or interactive login. The service runs as that user, with no Linux capabilities, a read-only system filesystem, protected home directories, and private temporary/device namespaces. No writable application-state directory is needed. The Agent only collects and reports data; it does not execute Server commands or update itself.

| Location | Owner | Mode |
| --- | --- | --- |
| `/opt/needle-agent/` | `root:needle-agent` | `750` |
| `/opt/needle-agent/bin/` | `root:root` | `755` |
| `/opt/needle-agent/bin/needle-agent` | `root:root` | `755` |
| `/opt/needle-agent/agent.yaml` | `root:needle-agent` | `640` |
| `/opt/needle-agent/agent.yaml.bak` (when present) | `root:root` | `600` |
| `/etc/systemd/system/needle-agent.service` | `root:root` | `644` |

The Agent can read its configuration but cannot modify it, its executable, the installation directory, or its service file. Edit configuration with `sudo`, then restart the service as root. Basic Linux collectors and TCP probes do not need elevated privileges; collection errors are logged and unavailable metric groups are reported as missing rather than healthy zero values. Swap totals are included in the memory report; this does not add a new dashboard chart.

Enter the Server HTTPS URL and node details. Use a unique token per Agent; leaving the token empty generates one. Select `5) Permanent` for no expiry, or choose a finite billing period. Choose the TCP Ping mode and enter the monthly upload + download allowance in decimal GB (`1000` = 1 TB, `0` or Enter = no limit). Allow the printed token **on the Server** using the command in the matching Server section above. The first successful report binds the token to the hostname.

### Status, logs, and upgrades

Download the current installer before upgrading to get the latest configuration prompts. Upgrade the Server to v0.7.0+ first, then upgrade Agents for core-count and monthly-allowance reporting. Existing tokens and hostnames are retained.

```sh
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh
sudo bash /tmp/needle-agent.sh status
sudo systemctl status needle-agent --no-pager
sudo journalctl -u needle-agent -n 100 --no-pager
sudo journalctl -u needle-agent -f

# Interactive upgrade: keep existing config (default) or reconfigure probes, allowance, and optional billing
sudo bash /tmp/needle-agent.sh upgrade
# Skip the configuration question
sudo bash /tmp/needle-agent.sh upgrade --keep-config
# Reselect probes, allowance, and optional billing (requires a terminal)
sudo bash /tmp/needle-agent.sh upgrade --reconfigure

sudo systemctl restart needle-agent
sudo systemctl stop needle-agent
sudo systemctl start needle-agent
```

Running `install` again with an existing Linux Agent configuration uses the upgrade flow, preserving the token and hostname. Edit `/opt/needle-agent/agent.yaml` as described under **Configuration**, then restart the service. For foreground troubleshooting, stop the service first:

```sh
sudo systemctl stop needle-agent
sudo runuser -u needle-agent -- /opt/needle-agent/bin/needle-agent /opt/needle-agent/agent.yaml
# Press Ctrl+C, then start the service again
sudo systemctl start needle-agent
```

Upgrading an older root-run installation automatically creates the service account and migrates permissions. Configuration is validated before replacement and checked again as the service user. Existing running and autostart states are preserved; a new installation starts immediately and at boot. A failed deployment restores the previous executable, configuration, permissions, and service. If rollback itself fails, root-only originals are retained in `/opt/needle-agent/recovery/` for manual recovery. An existing account with conflicting permissions or a systemd drop-in that changes the required user, group, capabilities, or sandbox settings is rejected rather than silently reused. Systems must support the listed systemd isolation settings.

### Uninstall

```sh
# Remove local installation; retain Server history
sudo bash /tmp/needle-agent.sh uninstall
# Request removal of the Server node before local uninstall
sudo bash /tmp/needle-agent.sh uninstall --unregister
```

Successful Server unregistration removes node history and the bound token. Allow a token on the Server before reconnecting a reinstalled Agent.

Linux uninstall also removes the dedicated service account and group; it does not remove a home directory because none is created. These systemd/account changes do not apply to the separate OpenWrt / ImmortalWrt procd installer.

</details>

<details>
<summary><strong>4. Agent — OpenWrt / ImmortalWrt</strong></summary>

### Requirements and installation

Use root SSH access on OpenWrt / ImmortalWrt with procd. The installer uses BusyBox-compatible `sh`; Bash, systemd, and Docker are not required. Supported architectures are `x86_64` (amd64) and `aarch64` (arm64). ARM 32-bit and MIPS are not supported.

You need curl or HTTPS-capable wget, CA certificates, sha256sum, and access to GitHub API and Releases. The selected release must contain `needle-agent-linux-ARCH.tar.gz` and its `.sha256` file. These standalone packages are available in v0.6.5 and later. Static builds and installer checks pass. Agent reporting and procd lifecycle tests also pass in an isolated OpenWrt 24.10.4 ARM64 userspace with a Linux virtual-machine kernel. This does not replace validation on the target router, especially x86_64 hardware, WAN/PPPoE interfaces, and firmware reboot behavior.

Download first, then run from an interactive SSH terminal:

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh
sh /tmp/needle-agent-openwrt.sh install
```

Enter the Server HTTPS URL and node details. The default reporting interval is 30 seconds and the default billing period is `forever` (Server v0.6.4+ required). Choose the TCP Ping mode and the monthly upload + download allowance in decimal GB (`1000` = 1 TB, `0` or Enter = no limit). Allow the printed token **on the Server** using the corresponding Server section above.

Confirm the WAN interface suggested by the installer. PPPoE commonly uses `pppoe-wan`; other WANs may use an interface such as `eth1`. The selected interface must exist. Avoid counting both a PPPoE interface and its underlying device, or both LAN and WAN for the same traffic. See the OpenWrt / ImmortalWrt Agent settings under **Configuration** for multi-WAN setups.

### Status, logs, and upgrades

A first installation starts the service and enables it at boot. Upgrades and reinstalls preserve its existing running and autostart states, including a deliberately stopped or disabled service. Download the current installer before each upgrade, including after `/tmp` was cleared on reboot. Upgrade the Server to v0.7.0+ first, then upgrade Agents for core-count and monthly-allowance reporting.

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh
sh /tmp/needle-agent-openwrt.sh status
logread -e needle-agent

# Interactive upgrade: keep existing config (default) or reconfigure probes, allowance, and optional billing
sh /tmp/needle-agent-openwrt.sh upgrade
sh /tmp/needle-agent-openwrt.sh upgrade --keep-config
# Reselect TCP Ping, allowance, and optional billing settings; preserve token and identity
sh /tmp/needle-agent-openwrt.sh upgrade --reconfigure
# Pin this release if needed
sh /tmp/needle-agent-openwrt.sh upgrade --version v0.7.0

/etc/init.d/needle-agent restart
/etc/init.d/needle-agent stop
/etc/init.d/needle-agent start
/etc/init.d/needle-agent enable
/etc/init.d/needle-agent disable
```

Edit `/etc/needle/agent.yaml`, then restart. Before downloading, the installer requires at least 64 MiB of available memory and 32 MiB free in `/tmp` for downloads and recovery snapshots. Missing checksums, checksum mismatches, or a binary that cannot execute abort installation before replacing the running Agent. The installer also checks free installation space, keeping a 4 MiB reserve after staging the binary. Agents supporting `-check-config` validate the configuration offline before the old service is stopped; this check performs no probes or reports. To check an edited configuration manually with a supporting Agent:

```sh
/usr/bin/needle-agent -check-config /etc/needle/agent.yaml
```

Installation, upgrades, and uninstall operations are locked against concurrent execution. Upgrades wait for the old process to exit before replacing it. When starting a service, the installer checks that it stays running; failures restore the previous binary, configuration, configuration backup, service, version, and boot/running state. If recovery itself fails, the installer prints and retains the temporary recovery directory. A startup check does not verify successful reports to the Server. For `unauthorized` reports, check the token allowed on the Server.

The Agent reads system counters and sends outbound reports and the configured TCP probes. It does not change network, firewall, DHCP, DNS, or routing settings; install packages; add cron jobs; open a listening port; or write local metric history. Router CPU, memory, and a small amount of outbound traffic are still used. The procd service runs at nice level 10 with one Go execution core, a 32 MiB **soft Go memory target** (not an RSS cap), no core dumps, and a 128-file descriptor limit. Repeated immediate crashes stop respawning after five retries. Use the default 30-second interval or longer on a busy router, and select only the WAN interface and probes you need.

### Uninstall and firmware upgrades

```sh
# Keep local configuration and Server history
sh /tmp/needle-agent-openwrt.sh uninstall
# Also delete local configuration; retain Server history
sh /tmp/needle-agent-openwrt.sh uninstall --purge
# Delete the Server node and its bound token, then remove local files
sh /tmp/needle-agent-openwrt.sh uninstall --unregister --purge
```

Add `--yes` to skip uninstall confirmation. If unregistration fails or is interrupted, local files are retained and the script restores the prior running state. Uninstall waits for the Agent to stop before deleting files. `--unregister` deletes the Server node, history, and bound token; omit it to keep those records.

Ordinary uninstall removes the binary, init service, autostart links, and version file, while retaining `agent.yaml`, its `.bak` backup, and the firmware-backup entry. `--purge` additionally deletes those configuration files and Needle's staging/recovery files and removes its exact entry from `/etc/sysupgrade.conf`. Other backup entries and unrelated files in `/etc/needle/` are kept; the directory is removed only when empty. Successful operations remove their temporary downloads, snapshots, and installer lock. Unexpected symlinks or existing staging files make an upgrade stop before overwriting them.

The installer adds `/etc/needle/` to `/etc/sysupgrade.conf`. Firmware upgrades that keep settings can retain configuration; reinstall the Agent binary and service afterward. Reinstallation preserves existing configuration. `uninstall --purge` also removes the backup entry.

</details>

## Common Paths

<details>
<summary><strong>Server — Docker</strong></summary>

| Item | Path |
| --- | --- |
| Compose file | `~/needle/docker-compose.yml` |
| Optional Compose environment | `~/needle/.env` |
| Host database | `~/needle/data/needle.db` |
| Container database | `/data/needle.db` |

Host paths assume the deployment directory used above.

</details>

<details>
<summary><strong>Server — Binary</strong></summary>

| Item | Path |
| --- | --- |
| Executable | `/opt/needle/bin/needle-server` |
| Environment | `/opt/needle/.env` |
| Database | `/opt/needle/data/needle.db` |
| Service | `/etc/systemd/system/needle-server.service` |

Paths are the installer defaults.

</details>

<details>
<summary><strong>Agent — Binary</strong></summary>

| Item | Path |
| --- | --- |
| Executable | `/opt/needle-agent/bin/needle-agent` |
| Configuration | `/opt/needle-agent/agent.yaml` |
| Backup before settings reselection | `/opt/needle-agent/agent.yaml.bak` |
| Service | `/etc/systemd/system/needle-agent.service` |

Paths are the installer defaults.

</details>

<details>
<summary><strong>Agent — OpenWrt / ImmortalWrt</strong></summary>

| Item | Path |
| --- | --- |
| Executable | `/usr/bin/needle-agent` |
| Configuration | `/etc/needle/agent.yaml` |
| Backup before settings reselection | `/etc/needle/agent.yaml.bak` |
| Installed version | `/etc/needle/version` |
| Service | `/etc/init.d/needle-agent` |
| Firmware backup list | `/etc/sysupgrade.conf` |

Paths are the installer defaults.

</details>

## Configuration

<details>
<summary><strong>Server — Docker Compose settings</strong></summary>

Edit `~/needle/docker-compose.yml`. The repository provides this configuration:

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
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8008/api/health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

| Setting | Purpose |
| --- | --- |
| `image` | `latest` tracks the latest published image; use a published version tag to pin a release |
| `ports` | Publishes container port 8008 on the host loopback address |
| `NEEDLE_PORT` | Compose substitution for the host port; defaults to 8008 |
| `NEEDLE_LISTEN` | Listen address inside the container; keep `:8008` for the supplied port mapping and health check |
| `volumes` | Persists the SQLite database from `/data` into the host's `./data` directory |
| `restart` | Restarts the container unless explicitly stopped |

For example, put `NEEDLE_PORT=8088` in `~/needle/.env` to change the host port, then run `docker compose up -d` from `~/needle`. Point the host reverse proxy or Tunnel to `http://127.0.0.1:8088`. This optional Compose `.env` is separate from the binary Server's environment file.

</details>

<details>
<summary><strong>Server — Binary environment and flags</strong></summary>

The systemd installer writes `/opt/needle/.env`:

```dotenv
NEEDLE_LISTEN=127.0.0.1:8008
```

The service uses that address and `/opt/needle/data/needle.db`. Restart after editing:

```sh
sudo systemctl restart needle-server
sudo journalctl -u needle-server -n 50 --no-pager
```

For manual binary execution, the supported flags are:

| Flag / variable | Purpose |
| --- | --- |
| `-l` / `NEEDLE_LISTEN` | Listen address; `-l` takes precedence, default `:8008` |
| `-db` | Database path; default `./data/needle.db` |
| `-cert` and `-key` | Certificate and private key for native HTTPS; supply both |
| `-y` | Skip confirmation for `delete-agent` and `revoke-token` |

Example with native TLS:

```sh
./needle-server -l :8443 -db ./data/needle.db -cert /path/to/fullchain.pem -key /path/to/privkey.pem
```

The supplied systemd service uses `-l` and `-db`; it does not load TLS paths from `.env`. Use a reverse proxy/Tunnel with that service, or adapt its unit for native TLS. For manual runs, `.env` is not automatically read by the binary. Stop the existing service before running another Server against the same port.

</details>

<details>
<summary><strong>Agent — Linux Binary / systemd</strong></summary>

Edit `/opt/needle-agent/agent.yaml`. A full example is available in [agent.yaml.example](agent.yaml.example).

```yaml
hostname: ""
server: "https://needle.example.com"
token: "replace-with-unique-agent-token"
region: SG
billing_currency: "USD"
billing_amount: "" # Entire billing period; "0" = free, "" = not configured
billing_period: "forever"
expires_at: ""
monthly_traffic_limit_gb: 1000 # 1000 decimal GB = 1 TB; 0 = no limit
interval: 30
tls_skip_verify: false
allow_plain_http: false
tcpping:
  - name: CMv4
    target: sh-cm-v4.ip.zstaticcdn.com:80
```

| Field | Meaning |
| --- | --- |
| `hostname` | Node name; empty uses the system hostname. Tokens bind to this name after the first successful report |
| `server` | Server URL; use the public HTTPS address |
| `token` | Unique allowed token for this Agent; do not reuse across nodes |
| `region` | Region code shown in the dashboard |
| `billing_currency` | Three-letter currency code, e.g. `USD`, `CNY`, `EUR`, `GBP`, `CAD`, `HKD`, `JPY`, `AUD`; no exchange-rate conversion |
| `billing_amount` | Exact decimal amount for the entire billing period, not a monthly equivalent; quote it in YAML. `"0"` means free, `""` means not configured; up to 12 integer and 6 fractional digits |
| `billing_period` | `1m`, `3m`, `6m`, `12m`, `24m`, `36m`, or `forever`; empty leaves the billing period unset |
| `expires_at` | Actual next renewal / top-up date in `YYYY-MM-DD`, independent of when you bought or acquired the node; empty for `forever` |
| `monthly_traffic_limit_gb` | Monthly upload + download allowance in decimal GB; `1000` = 1 TB, `0` or omitted displays ♾️; display only, does not stop traffic; Server and Agent v0.7.0+ |
| `interval` | Report interval in seconds; defaults to 30 when missing or non-positive |
| `interfaces` | Exact NIC whitelist; omitted or empty uses the virtual-interface exclusion filter |
| `tls_skip_verify` | Disables TLS certificate verification; defaults to false |
| `allow_plain_http` | Allows remote plaintext HTTP; defaults to false. Loopback HTTP is permitted without it |
| `tcpping` | List of named TCP targets in `host:port` format |

The installer asks separately for currency, the amount for the entire billing period, the billing period, and the actual next renewal date. It does not infer the date from today or the original purchase date, so a transferred annual node can retain its existing renewal date. `forever` skips the date question; free service and unlimited monthly traffic are independent settings. Billing metadata is reported to the Server and retained in its database/API. The card footer shows the amount with its billing-period suffix and the next scheduled renewal date. Zero amounts display `Free`; missing amounts display `Not set`. Reports from Agents that omit billing cost preserve the previously saved cost. Upgrade the Server before using the new Agent billing fields and `24m`/`36m` periods.

Finite periods control the renewal countdown. Monthly traffic resets on the day of month in `expires_at`, regardless of whether the billing period is one, three, six, twelve, twenty-four, or thirty-six months. `forever` displays ♾️ in the due-date position, ignores any expiry date, and resets traffic on the first of each month in the Server timezone. Server v0.6.4+ is required for `forever`. Without an expiry date or `forever`, monthly traffic is unavailable. The configured `expires_at` date anchors the payment schedule. The countdown shows `Due today` throughout a renewal day; on the next day, the displayed date advances by the billing period until it is current or future. Every candidate retains the original day, clamped to the end of shorter months, so a day-31 schedule returns to day 31 after February. The stored anchor is unchanged, and monthly traffic still resets independently. This is a scheduled payment reminder, not confirmation of payment or service status. Update the configuration and restart the Agent only when the schedule changes.

Date-only settings retain their configured calendar date in the Server timezone, including the UTC-noon encoding sent by existing Agents and UTC-midnight date values. Renewal days count calendar days, including across daylight-saving changes; day 29–31 resets clamp to shorter months. When upgrading from the earlier UTC/local-date behavior, an existing active traffic cycle keeps its accumulated usage until the next corrected local reset. Renewing the date or changing the payment period preserves that transition when the monthly reset day stays the same. The original baseline remains available; restarting the Server does not repeat the transition. Keep the Server timezone stable.

`monthly_traffic_limit_gb` controls the traffic allowance independently of expiry. For example, a permanent node can still have a 1 TB monthly allowance. TRF displays combined monthly upload and download usage against this allowance, using decimal units: `1000 GB = 1 TB`. A zero or omitted allowance displays ♾️; exceeding a finite allowance highlights the percentage but does not block traffic. Changing the allowance does not reset accumulated usage.

Usage starts at the first observed counter baseline; Needle cannot recover traffic used before monitoring began or reconcile it with a provider's meter. Upgrade both Server and Agent to v0.7.0+ for allowance reporting and logical CPU counts. Core counts are collected automatically and do not require a configuration option.

For a Linux server, omit `interfaces` to use automatic filtering, or specify the actual NIC:

```yaml
interfaces: ["eth0"]
```

The Linux installer offers these TCP Ping choices starting in v0.7.0, with no automatic connectivity detection:

| Mode | Targets |
| --- | --- |
| `1) IPv4 only` (default) | CMv4, CUv4, CTv4 |
| `2) IPv6 only` | CMv6, CUv6, CTv6 |
| `3) Dual stack` | All six presets |
| `4) Custom` | Select individual presets and optionally add your own targets |
| `5) Disabled` | No TCP Ping; system metrics continue reporting |

Custom targets require unique names and `host:port` addresses; bracket literal IPv6 addresses, for example `[2001:db8::1]:443`. Up to 32 targets are supported. Choose according to the node's usable outbound connectivity, including NAT or WARP routes.

The `tcpping` list remains the source of truth. During an interactive upgrade, choose **Keep existing configuration** (default) or **Reconfigure TCP Ping, monthly allowance, and optional billing settings**. Reselection updates `tcpping` and optionally `monthly_traffic_limit_gb` (Enter preserves the existing allowance). The final billing question defaults to **No**, preserving existing currency, amount, period, and renewal date. Choosing **Yes** replaces all four billing fields through separate prompts; the token, hostname, Server URL, network interfaces, and other settings are preserved. The previous file is saved as `agent.yaml.bak` with mode 600.

Use `upgrade --keep-config` to suppress the choice, or `upgrade --reconfigure` to go directly to the five TCP Ping options, followed by the monthly allowance and optional billing prompts. Non-interactive upgrades keep the original configuration; explicit reconfiguration requires a terminal. `--keep-config` and `--reconfigure` cannot be combined. You can also edit the list and restart the Agent manually; there is no runtime auto-detection or automatic removal of unreachable targets. To disable probes:

```yaml
tcpping: []
```

The dashboard's live card/list summaries follow the latest report. Disabling all probes hides the card/list probe results and the TCP Ping chart, Show/Hide controls, and route statistics in Node Detail. Every node keeps the `TCP Ping` title and `1d / 7d / 30d` controls directly below the four system charts; there is no enabled-target count. Cards retain a three-row probe area with the same divider and a centered, muted `TCP Ping · Off` label, keeping a consistent minimum height and aligned edges within each grid row; custom targets can expand cards naturally. System charts retain their history-range controls and weekly date drill-down. Re-enabling probes restores their summaries and chart automatically. Detail charts and statistics show only currently enabled targets. Previously enabled targets are hidden, and their stored data expires under the existing history retention rules. Chart Show/Hide controls affect display only; they do not change the Agent's configuration.

Restart after editing:

```sh
sudo systemctl restart needle-agent
sudo journalctl -u needle-agent -n 50 --no-pager
```

Keep the database, environment files, configurations, and tokens out of source control. Use HTTPS for public access; Tunnel or reverse-proxy protection does not replace the Server's token checks.

</details>

<details>
<summary><strong>Agent — OpenWrt / ImmortalWrt</strong></summary>

Edit `/etc/needle/agent.yaml`. A full example of the YAML format is available in [agent.yaml.example](agent.yaml.example); select the actual WAN device for your router.

```yaml
hostname: "home-router"
server: "https://needle.example.com"
token: "replace-with-unique-agent-token"
region: CN
billing_currency: "USD"
billing_amount: "" # Entire billing period; "0" = free, "" = not configured
billing_period: "forever"
expires_at: ""
monthly_traffic_limit_gb: 1000 # 1000 decimal GB = 1 TB; 0 = no limit
interval: 30
interfaces: ["pppoe-wan"]
tls_skip_verify: false
allow_plain_http: false
tcpping:
  - name: CMv4
    target: sh-cm-v4.ip.zstaticcdn.com:80
```

| Field | Meaning |
| --- | --- |
| `hostname` | Node name; empty uses the system hostname. Tokens bind to this name after the first successful report |
| `server` | Server URL; use the public HTTPS address |
| `token` | Unique allowed token for this Agent; do not reuse across nodes |
| `region` | Region code shown in the dashboard |
| `billing_currency` | Three-letter currency code, e.g. `USD`, `CNY`, `EUR`, `GBP`, `CAD`, `HKD`, `JPY`, `AUD`; no exchange-rate conversion |
| `billing_amount` | Exact decimal amount for the entire billing period, not a monthly equivalent; quote it in YAML. `"0"` means free, `""` means not configured; up to 12 integer and 6 fractional digits |
| `billing_period` | `1m`, `3m`, `6m`, `12m`, `24m`, `36m`, or `forever`; empty leaves the billing period unset |
| `expires_at` | Actual next renewal / top-up date in `YYYY-MM-DD`, independent of when you bought or acquired the node; empty for `forever` |
| `monthly_traffic_limit_gb` | Monthly upload + download allowance in decimal GB; `1000` = 1 TB, `0` or omitted displays ♾️; display only, does not stop traffic; Server and Agent v0.7.0+ |
| `interval` | Report interval in seconds; defaults to 30 when missing or non-positive |
| `interfaces` | Exact NIC whitelist; explicitly select the actual WAN device, such as `pppoe-wan` or `eth1`, to avoid counting LAN/WAN traffic twice |
| `tls_skip_verify` | Disables TLS certificate verification; defaults to false |
| `allow_plain_http` | Allows remote plaintext HTTP; defaults to false. Loopback HTTP is permitted without it |
| `tcpping` | List of named TCP targets in `host:port` format |

The installer asks separately for currency, the amount for the entire billing period, the billing period, and the actual next renewal date. It does not infer the date from today or the original purchase date, so a transferred annual node can retain its existing renewal date. `forever` skips the date question; free service and unlimited monthly traffic are independent settings. Billing metadata is reported to the Server and retained in its database/API. The card footer shows the amount with its billing-period suffix and the next scheduled renewal date. Zero amounts display `Free`; missing amounts display `Not set`. Reports from Agents that omit billing cost preserve the previously saved cost. Upgrade the Server before using the new Agent billing fields and `24m`/`36m` periods.

Finite periods control the renewal countdown. Monthly traffic resets on the day of month in `expires_at`, regardless of whether the billing period is one, three, six, twelve, twenty-four, or thirty-six months. `forever` displays ♾️ in the due-date position, ignores any expiry date, and resets traffic on the first of each month in the Server timezone. Server v0.6.4+ is required for `forever`. Without an expiry date or `forever`, monthly traffic is unavailable. The configured `expires_at` date anchors the payment schedule. The countdown shows `Due today` throughout a renewal day; on the next day, the displayed date advances by the billing period until it is current or future. Every candidate retains the original day, clamped to the end of shorter months, so a day-31 schedule returns to day 31 after February. The stored anchor is unchanged, and monthly traffic still resets independently. This is a scheduled payment reminder, not confirmation of payment or service status. Update the configuration and restart the Agent only when the schedule changes.

Date-only settings retain their configured calendar date in the Server timezone, including the UTC-noon encoding sent by existing Agents and UTC-midnight date values. Renewal days count calendar days, including across daylight-saving changes; day 29–31 resets clamp to shorter months. When upgrading from the earlier UTC/local-date behavior, an existing active traffic cycle keeps its accumulated usage until the next corrected local reset. Renewing the date or changing the payment period preserves that transition when the monthly reset day stays the same. The original baseline remains available; restarting the Server does not repeat the transition. Keep the Server timezone stable.

`monthly_traffic_limit_gb` controls the traffic allowance independently of expiry. For example, a permanent node can still have a 1 TB monthly allowance. TRF displays combined monthly upload and download usage against this allowance, using decimal units: `1000 GB = 1 TB`. A zero or omitted allowance displays ♾️; exceeding a finite allowance highlights the percentage but does not block traffic. Changing the allowance does not reset accumulated usage.

Usage starts at the first observed counter baseline; Needle cannot recover traffic used before monitoring began or reconcile it with a provider's meter. Upgrade both Server and Agent to v0.7.0+ for allowance reporting and logical CPU counts. Core counts are collected automatically and do not require a configuration option.

For an OpenWrt PPPoE router, choose the actual WAN device explicitly:

```yaml
hostname: "home-router"
region: CN
interfaces: ["pppoe-wan"]
```

The OpenWrt installer recommends WAN's `l3_device` when available. Multiple WAN interfaces can be listed manually, but do not count multiple layers of the same connection. The OpenWrt installer offers these TCP Ping choices starting in v0.7.0, with no automatic connectivity detection:

| Mode | Targets |
| --- | --- |
| `1) IPv4 only` (default) | CMv4, CUv4, CTv4 |
| `2) IPv6 only` | CMv6, CUv6, CTv6 |
| `3) Dual stack` | All six presets |
| `4) Custom` | Select individual presets and optionally add your own targets |
| `5) Disabled` | No TCP Ping; system metrics continue reporting |

Custom targets require unique names and `host:port` addresses; bracket literal IPv6 addresses, for example `[2001:db8::1]:443`. Up to 32 targets are supported. Choose according to the node's usable outbound connectivity, including NAT or WARP routes.

The `tcpping` list remains the source of truth. During an interactive upgrade, choose **Keep existing configuration** (default) or **Reconfigure TCP Ping, monthly allowance, and optional billing settings**. Reselection updates `tcpping` and optionally `monthly_traffic_limit_gb` (Enter preserves the existing allowance). The final billing question defaults to **No**, preserving existing currency, amount, period, and renewal date. Choosing **Yes** replaces all four billing fields through separate prompts; the token, hostname, Server URL, network interfaces, and other settings are preserved. The previous file is saved as `agent.yaml.bak` with mode 600. OpenWrt installation failures restore the original configuration along with the old binary and service.

Use `upgrade --keep-config` to suppress the choice, or `upgrade --reconfigure` to go directly to the five TCP Ping options, followed by the monthly allowance and optional billing prompts. Non-interactive upgrades keep the original configuration; explicit reconfiguration requires a terminal. `--keep-config` and `--reconfigure` cannot be combined. You can also edit the list and restart the Agent manually; there is no runtime auto-detection or automatic removal of unreachable targets. To disable probes:

```yaml
tcpping: []
```

The dashboard's live card/list summaries follow the latest report. Disabling all probes hides the card/list probe results and the TCP Ping chart, Show/Hide controls, and route statistics in Node Detail. Every node keeps the `TCP Ping` title and `1d / 7d / 30d` controls directly below the four system charts; there is no enabled-target count. Cards retain a three-row probe area with the same divider and a centered, muted `TCP Ping · Off` label, keeping a consistent minimum height and aligned edges within each grid row; custom targets can expand cards naturally. System charts retain their history-range controls and weekly date drill-down. Re-enabling probes restores their summaries and chart automatically. Detail charts and statistics show only currently enabled targets. Previously enabled targets are hidden, and their stored data expires under the existing history retention rules. Chart Show/Hide controls affect display only; they do not change the Agent's configuration.

Restart after editing:

```sh
/etc/init.d/needle-agent restart
logread -e needle-agent
```

Keep the database, environment files, configurations, and tokens out of source control. Use HTTPS for public access; Tunnel or reverse-proxy protection does not replace the Server's token checks.

</details>

## Reference

<details>
<summary><strong>Node cards and live probe results</strong></summary>

The card header shows online status, hostname, region, uptime, and the renewal countdown; permanent nodes show ♾️ for expiry. Cards use compact spacing while preserving font sizes, all metrics, dividers, and aligned probe areas for enabled and disabled nodes. The countdown has no Due-date hover tooltip. A footer below the probes shows the billing cost on the left and the next scheduled renewal date on the right. Amounts use the configured currency and period (`/mo`, `/3mo`, `/6mo`, `/yr`, `/2yr`, `/3yr`, or ` · Lifetime`); free service displays `Free`, unconfigured cost/date displays `Not set`, and permanent dates display ♾️. The date and countdown advance automatically by the configured billing period after a renewal day has passed; there is no overdue-day counter. The countdown shows `Due today` on the renewal day itself.

| Card area | Display |
| --- | --- |
| CPU | Utilization, logical core count (`4C` = four logical CPUs / vCPUs), and the one-minute load average (`Load 0.14`, not 14% CPU utilization) |
| MEM / DSK | Utilization bars and used / total capacity |
| TRF | Combined monthly upload + download, allowance, and usage percentage; for example, `396 GB / 1 TB` and `39.6%`. An unlimited allowance displays ♾️ and actual usage such as `164 GB used`, with no percentage |
| Network, left | Current download and upload rates in bytes per second |
| Network, right | Separate monthly download and upload totals, aligned with their current rates |
| Probes | Currently enabled targets, ordered CM / CU / CT, with v4 fixed on the left and v6 on the right; other custom targets appear below |

Cards no longer cycle through a single selected probe. The list layout summarizes the first target in the latest report. Live latency and loss reflect the latest TCP connection attempt: success shows `0%`, failure shows `100%` and no latency. Detail chart statistics instead summarize the selected history window, so their loss percentages can differ.

Detail probe summaries show average latency, jitter, and loss beside each target. On desktop, IPv4 targets occupy the first row and IPv6 targets the second, centered in three equal columns; narrower screens wrap the summaries. Click a target to show or hide its chart line, or use Show / Hide for all lines. Disabled targets are omitted. The dashboard uses coordinated gray-blue accents in both light and dark themes; warning and error colors remain distinct.

The dashboard refreshes every 30 seconds. Detail chart line selection, Show/Hide controls, historical ranges, and daily drill-down remain available. Agents that have not yet been upgraded still report their existing metrics; their logical core count remains unavailable until they send it.

</details>

<details>
<summary><strong>Upgrading to v0.7.4</strong></summary>

1. Upgrade the Server using its Docker or binary section above. SQLite columns are migrated automatically and existing history is retained.
2. Download the latest Agent installer for each machine and run `upgrade` from an interactive terminal.
3. Choose **Keep existing configuration** (default) to preserve the file unchanged, or **Reconfigure TCP Ping, monthly allowance, and optional billing settings** to select the desired probes and optional allowance. Press Enter at the allowance prompt to retain its current value; enter `0` to remove a limit.
4. Existing tokens, hostnames, Server URLs, and interfaces are preserved. No token registration is needed again. Restart after manual YAML edits.

Non-interactive upgrades keep the original configuration. Use `upgrade --keep-config` for an explicit unchanged configuration, or `upgrade --reconfigure` to enter the configuration flow directly (requires a terminal). Upgrading does not automatically change an existing six-target configuration to IPv4-only; choose a new mode explicitly if needed.

If probes, traffic allowance, period, and renewal date are already correct, use `upgrade --keep-config`, then add only `billing_currency` and `billing_amount` to the existing YAML and restart the Agent. No renewal date re-entry or token registration is needed. For example, `billing_currency: "USD"` and `billing_amount: "50.00"` with an existing `billing_period: "12m"` means USD 50 per year. The card projects the next scheduled renewal from the existing `expires_at`; monthly traffic continues to reset on that date's day of month, independently of the payment period. Upgrade the Server first so it can accept billing cost and the new two-/three-year periods.

</details>

<details>
<summary><strong>Dashboard refresh troubleshooting</strong></summary>

The dashboard refreshes node data every 30 seconds. Requests time out after 15 seconds, so an unresponsive connection cannot leave future refreshes permanently blocked. The version/Server info request is independent: its failure is logged in the browser console without marking successfully updated nodes as stale.

If the node request fails, the dashboard retains the previous view and identifies a connection failure, timeout, HTTP error, or invalid response. A display error is reported separately. Retry forces a fresh render; periodic refreshes also recover automatically. Browser console errors include the failing endpoint. The Server logs database failures from `/api/agents` with the affected query and Agent ID, without exposing database details in the HTTP response.

Metrics and TCP Ping history refresh independently and show an error with a Retry button beside the affected section. Switching nodes or ranges cancels pending detail requests and ignores late responses. Closing or rebuilding a detail view disposes its charts. Existing chart-line selections remain saved locally.

</details>

<details>
<summary><strong>Traffic counter regressions and recovery</strong></summary>

The Server uses reported system uptime to distinguish a confirmed reboot from an ordinary counter decrease. A confirmed reboot retains the previous usage and adds the new boot's counters. With continuous or missing uptime, a decrease is treated conservatively: previous per-direction high watermarks are retained, and a rebound cannot count the same bytes twice. Late samples do not move the live accumulator backwards.

Network accounting does not depend on CPU collection succeeding. Reports without network counters do not seed or update usage; a measured zero remains valid. Missing collectors appear as unavailable values and chart gaps, and do not dilute new averages. Legacy zero placeholders without proof of network collection are skipped when selecting a new baseline. Delayed reports belong to their sampled month; a slightly fast Agent clock cannot reset usage before the Server's local boundary. The first observed network counter in each cycle establishes the baseline, so traffic between the boundary and that observation is not recoverable.

Updated Agents send the sample timestamp before probing and transport, and report successful collectors even if another collector fails. A delayed report can add history without overwriting current billing settings, traffic allowance, region, CPU core count, or enabled probes. Older Agents continue to use Server receipt time. When initializing a baseline-only legacy accumulator, the Server preserves its previously visible usage from retained samples before processing the arriving report, including a delayed sample or a confirmed reboot. Baseline selection and initialization are bounded to the relevant cycle.

The updated Agent treats an empty or unmatched network-interface snapshot as a collection failure, retaining its previous rate baseline for recovery. This collector-side check requires an Agent upgrade; the Server-side accounting fixes work with existing Agents.

After an interface reset or interface-set change without a machine reboot, totals alone cannot establish the exact traffic lost at the transition. Usage resumes when a counter exceeds its prior high watermark; traffic during catch-up may be undercounted. This avoids treating hundreds of GB of existing counters as new traffic. Specifying a stable `interfaces` whitelist helps keep the counter scope consistent.

Server versions v0.6.4 through v0.7.0 counted any backwards movement as a reset and could inflate monthly usage. The uptime-aware fix requires only a Server upgrade: existing Agents already report uptime. An upgrade prevents new inflation but does not silently rewrite existing monthly totals.

A Server build containing this fix provides `repair-traffic`. It freezes the legacy accumulator on first database migration and compares the original and corrected algorithms over retained raw samples. Preview the current cycle for one node or `all`. Applying requires a trusted `--since` timestamp when the faulty accumulator was known active, such as a confirmed deployment time; the old schema did not retain its activation time. Without this bound, the preview shows candidates only. Corrections cover only supported events in retained history after that time, preserve the unknown prefix before that window, and are applied at most once per cycle. Missing/ambiguous samples or inconsistent totals are reported for manual review; they are never reconstructed from hourly maxima. A simple latest-counter-minus-baseline calculation is not reliable across resets.

Stop the Server before applying corrections. The apply command creates a consistent, mode-600 `needle.db.traffic-backup-TIMESTAMP.db` before changing usage. Tokens, metric history, and Agent configurations are retained. Keep backups outside source control and do not downgrade to the old accounting implementation afterward.

For a binary deployment, using the updated Server binary:

```sh
sudo systemctl stop needle-server
# Preview only; inspect each node's reason and before/after values (bytes)
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db repair-traffic all
# Replace --since with a confirmed deployment time for the faulty accumulator
# Apply only the supported corrections for the selected node (ID or hostname)
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db repair-traffic --since 2026-10-18T03:17:38Z --apply 7
sudo systemctl start needle-server
```

For Docker Compose, from the deployment directory, after pulling an image containing the fix (replace the example `--since` with a trusted deployment timestamp):

```sh
docker compose stop needle-server
docker compose run --rm --no-deps needle-server -db /data/needle.db repair-traffic all
docker compose run --rm --no-deps needle-server -db /data/needle.db repair-traffic --since 2026-10-18T03:17:38Z --apply 7
docker compose up -d
```

</details>

<details>
<summary><strong>History views and retention</strong></summary>

| View | Data | Use |
| --- | --- | --- |
| `1d` | Raw samples | Inspect the last 24 hours |
| `7d` | 15-minute downsampled buckets | Review weekly trends |
| Date view | Raw samples for a selected recent calendar day | Drill down from the weekly overview |
| `30d` | Hourly rollups | Review monthly trends |

Click a date below the seven-day TCP Ping chart to switch all detail charts to that day. Red dots indicate anomaly summaries. Click `7d` to return. Raw data is kept for seven days; hourly data is kept for 90 days. Older date windows use hourly data.

The purge keeps complete raw boundary hours (up to one extra hour). Hourly summaries merge each newly ingested row once, including late reports and reports retained through Server downtime. Metrics use per-collector sample counts; TCP Ping latency averages use successful samples, while loss uses all attempts. Aggregation, progress checkpoints, and deletion commit together; a storage error retains raw data for a later retry. Restarting or repeating maintenance does not duplicate counts.

On upgrade, recent complete hours are rebuilt from retained raw data. Older overlapping raw/hourly data can be ambiguous because earlier versions did not record which rows were aggregated. Such hourly summaries are preserved, with both sources copied into `*_rollup_legacy` evidence tables under the same 90-day retention. Missing history is not invented or silently recomputed. Deleting a node removes its raw history, summaries, traffic baselines, and migration evidence before its ID can be reused. After upgrading, avoid downgrading to versions that do not understand these checkpoints and missing-collector values.

</details>

<details>
<summary><strong>Build from source and upgrade scope</strong></summary>

Use the Go version declared in [go.mod](go.mod) (currently 1.26.4), plus a C compiler for the SQLite Server:

```sh
git clone https://github.com/Robinproxy/Needle.git
cd Needle
go test ./...
go build -o needle-server ./cmd/server
CGO_ENABLED=0 go build -o needle-agent ./cmd/agent
```

Release workflows build Linux amd64 and arm64. The Server uses CGO for SQLite; standalone Agent packages use static builds with `CGO_ENABLED=0`.

Dashboard and API changes usually require only a Server upgrade. Upgrade Agents for collector, transport, or local configuration changes. Read release notes and back up Server data before upgrades. All installer scripts are in [scripts/](scripts/).

</details>

## Credits

- Thanks to [akiasprin](https://github.com/akiasprin) for open-sourcing [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2), which inspired parts of Needle's dashboard design.
- Thanks to [zstaticcdn](https://lf3-ips.zstaticcdn.com/) for providing TCP Ping endpoints.
- Thanks to [OpenCode](https://opencode.ai/), [DeepSeek](https://deepseek.com/), [Grok](https://x.ai/), [GLM](https://zhipuai.cn/), and [OpenAI Codex](https://openai.com/codex/) for development and code-review assistance.
- Thanks to every open-source author, contributor, and Needle user who has shared feedback.

## License

[MIT](https://opensource.org/license/mit)
