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

- CPU, memory, disk, load, uptime, network rates, and monthly traffic usage
- Multi-target TCP Ping with selectable chart lines
- `1d` raw history, a downsampled `7d` overview with daily drill-down, and `30d` hourly trends
- Seven-day raw retention and 90-day hourly retention
- Monthly traffic accumulation that survives Agent reboots and counter resets
- Monthly, quarterly, semiannual, annual, and permanent billing periods; permanent nodes display ♾️
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

# Revoke a credential and delete its bound node and history
docker compose exec needle-server needle-server -db /data/needle.db revoke-token YOUR_TOKEN
# Delete a node, its history, and its bound token by ID or hostname
docker compose exec needle-server needle-server -db /data/needle.db delete-agent HOSTNAME_OR_ID
```

Deleting a registered node also removes its bound token. Allow the token again if you want that Agent to reconnect.

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
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db revoke-token YOUR_TOKEN
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent HOSTNAME_OR_ID
```

Revoking a token deletes its bound node data. Deleting a node also removes its bound token; allow the token again before reconnecting the Agent.

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

Enter the Server HTTPS URL and node details. Use a unique token per Agent; leaving the token empty generates one. Select `5) Permanent` for no expiry, or choose a finite billing period. Allow the printed token **on the Server** using the command in the matching Server section above. The first successful report binds the token to the hostname.

### Status, logs, and upgrades

```sh
sudo bash /tmp/needle-agent.sh status
sudo systemctl status needle-agent --no-pager
sudo journalctl -u needle-agent -n 100 --no-pager
sudo journalctl -u needle-agent -f

# Upgrade, preserving agent.yaml
sudo bash /tmp/needle-agent.sh upgrade

sudo systemctl restart needle-agent
sudo systemctl stop needle-agent
sudo systemctl start needle-agent
```

Edit `/opt/needle-agent/agent.yaml` as described under **Configuration**, then restart the service. For foreground troubleshooting, stop the service first:

```sh
sudo systemctl stop needle-agent
sudo /opt/needle-agent/bin/needle-agent /opt/needle-agent/agent.yaml
# Press Ctrl+C, then start the service again
sudo systemctl start needle-agent
```

### Uninstall

```sh
# Remove local installation; retain Server history
sudo bash /tmp/needle-agent.sh uninstall
# Request removal of the Server node before local uninstall
sudo bash /tmp/needle-agent.sh uninstall --unregister
```

Successful Server unregistration removes node history and the bound token. Allow a token on the Server before reconnecting a reinstalled Agent.

</details>

<details>
<summary><strong>4. Agent — OpenWrt / ImmortalWrt</strong></summary>

### Requirements and installation

Use root SSH access on OpenWrt / ImmortalWrt with procd. The installer uses BusyBox-compatible `sh`; Bash, systemd, and Docker are not required. Supported architectures are `x86_64` (amd64) and `aarch64` (arm64). ARM 32-bit and MIPS are not supported.

You need curl or HTTPS-capable wget, CA certificates, sha256sum, and access to GitHub API and Releases. The selected release must contain `needle-agent-linux-ARCH.tar.gz` and its `.sha256` file. These standalone packages are absent from v0.6.4 and require a subsequent release containing them. Static builds and installer checks pass. Agent reporting and procd lifecycle tests also pass in an isolated OpenWrt 24.10.4 ARM64 userspace with a Linux virtual-machine kernel. This does not replace validation on the target router, especially x86_64 hardware, WAN/PPPoE interfaces, and firmware reboot behavior.

Download first, then run from an interactive SSH terminal:

```sh
wget -O /tmp/needle-agent-openwrt.sh https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent-openwrt.sh
sh /tmp/needle-agent-openwrt.sh install
```

Enter the Server HTTPS URL and node details. The default reporting interval is 30 seconds and the default billing period is `forever` (Server v0.6.4+ required). Allow the printed token **on the Server** using the corresponding Server section above.

Confirm the WAN interface suggested by the installer. PPPoE commonly uses `pppoe-wan`; other WANs may use an interface such as `eth1`. The selected interface must exist. Avoid counting both a PPPoE interface and its underlying device, or both LAN and WAN for the same traffic. See the shared Agent settings under **Configuration** for multi-WAN setups.

### Status, logs, and upgrades

Installation enables the service at boot. Download the installer again if `/tmp` was cleared after reboot.

```sh
sh /tmp/needle-agent-openwrt.sh status
logread -e needle-agent

# Upgrade, retaining configuration and token
sh /tmp/needle-agent-openwrt.sh upgrade
# Replace vX.Y.Z with a release containing standalone Agent packages
sh /tmp/needle-agent-openwrt.sh upgrade --version vX.Y.Z

/etc/init.d/needle-agent restart
/etc/init.d/needle-agent stop
/etc/init.d/needle-agent start
/etc/init.d/needle-agent enable
/etc/init.d/needle-agent disable
```

Edit `/etc/needle/agent.yaml`, then restart. Missing checksums, checksum mismatches, or a binary that cannot execute abort installation before replacing the running Agent. The installer also checks that the process stays running after startup; failed installations restore the previous binary, configuration, service, version, and boot/running state. This startup check does not verify successful reports to the Server. For `unauthorized` reports, check the token allowed on the Server.

### Uninstall and firmware upgrades

```sh
# Keep local configuration and Server history
sh /tmp/needle-agent-openwrt.sh uninstall
# Also delete local configuration; retain Server history
sh /tmp/needle-agent-openwrt.sh uninstall --purge
# Delete the Server node and its bound token, then remove local files
sh /tmp/needle-agent-openwrt.sh uninstall --unregister --purge
```

Add `--yes` to skip uninstall confirmation. If unregistration fails, the installation is retained and the script attempts to restart the service.

The installer adds `/etc/needle/` to `/etc/sysupgrade.conf`. Firmware upgrades that keep settings can retain configuration; reinstall the Agent binary and service afterward. Reinstallation preserves existing configuration. `uninstall --purge` also removes the backup entry.

</details>

## Common Paths

<details>
<summary><strong>Server and Agent file locations</strong></summary>

| Deployment | Item | Path |
| --- | --- | --- |
| Server Docker | Compose file | `~/needle/docker-compose.yml` |
| Server Docker | Optional Compose environment | `~/needle/.env` |
| Server Docker | Host database | `~/needle/data/needle.db` |
| Server Docker | Container database | `/data/needle.db` |
| Server binary | Executable | `/opt/needle/bin/needle-server` |
| Server binary | Environment | `/opt/needle/.env` |
| Server binary | Database | `/opt/needle/data/needle.db` |
| Server binary | Service | `/etc/systemd/system/needle-server.service` |
| Agent binary | Executable | `/opt/needle-agent/bin/needle-agent` |
| Agent binary | Configuration | `/opt/needle-agent/agent.yaml` |
| Agent binary | Service | `/etc/systemd/system/needle-agent.service` |
| Agent OpenWrt | Executable | `/usr/bin/needle-agent` |
| Agent OpenWrt | Configuration | `/etc/needle/agent.yaml` |
| Agent OpenWrt | Installed version | `/etc/needle/version` |
| Agent OpenWrt | Service | `/etc/init.d/needle-agent` |
| Agent OpenWrt | Firmware backup list | `/etc/sysupgrade.conf` |

Docker host paths assume the deployment directory used above. Binary paths are the installer defaults.

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
<summary><strong>Agent — Shared settings for Linux Binary and OpenWrt</strong></summary>

Both installers use the same YAML format. Edit `/opt/needle-agent/agent.yaml` for Linux/systemd or `/etc/needle/agent.yaml` for OpenWrt/ImmortalWrt. A full example is available in [agent.yaml.example](agent.yaml.example).

```yaml
hostname: ""
server: "https://needle.example.com"
token: "replace-with-unique-agent-token"
region: SG
billing_period: "forever"
expires_at: ""
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
| `billing_period` | `1m`, `3m`, `6m`, `12m`, or `forever`; empty leaves the billing period unset |
| `expires_at` | Renewal date in `YYYY-MM-DD`; empty for `forever` |
| `interval` | Report interval in seconds; defaults to 30 when missing or non-positive |
| `interfaces` | Exact NIC whitelist; omitted or empty uses the virtual-interface exclusion filter |
| `tls_skip_verify` | Disables TLS certificate verification; defaults to false |
| `allow_plain_http` | Allows remote plaintext HTTP; defaults to false. Loopback HTTP is permitted without it |
| `tcpping` | List of named TCP targets in `host:port` format |

Finite periods control the renewal countdown. Monthly traffic resets on the day of month in `expires_at`, regardless of whether the billing period is one, three, six, or twelve months. `forever` displays ♾️, ignores any expiry date, and resets traffic on the first of each month in the Server timezone. Server v0.6.4+ is required for `forever`. Without an expiry date or `forever`, monthly traffic is unavailable. The countdown does not disable an Agent when a date is reached.

For a Linux server, omit `interfaces` to use automatic filtering, or specify the actual NIC:

```yaml
interfaces: ["eth0"]
```

For an OpenWrt PPPoE router, choose the actual WAN device explicitly:

```yaml
hostname: "home-router"
region: CN
interfaces: ["pppoe-wan"]
```

The OpenWrt installer recommends WAN's `l3_device` when available. Multiple WAN interfaces can be listed manually, but do not count multiple layers of the same connection. OpenWrt defaults to three IPv4 TCP Ping targets (CMv4, CUv4, CTv4); the Linux installer offers six IPv4/IPv6 targets. Both can be edited in `tcpping`.

Restart after editing:

```sh
# Linux binary / systemd
sudo systemctl restart needle-agent
sudo journalctl -u needle-agent -n 50 --no-pager
# OpenWrt / ImmortalWrt
/etc/init.d/needle-agent restart
logread -e needle-agent
```

Keep the database, environment files, configurations, and tokens out of source control. Use HTTPS for public access; Tunnel or reverse-proxy protection does not replace the Server's token checks.

</details>

## Reference

<details>
<summary><strong>History views and retention</strong></summary>

| View | Data | Use |
| --- | --- | --- |
| `1d` | Raw samples | Inspect the last 24 hours |
| `7d` | 15-minute downsampled buckets | Review weekly trends |
| Date view | Raw samples for a selected recent calendar day | Drill down from the weekly overview |
| `30d` | Hourly rollups | Review monthly trends |

Click a date below the seven-day TCP Ping chart to switch all detail charts to that day. Red dots indicate anomaly summaries. Click `7d` to return. Raw data is kept for seven days; hourly data is kept for 90 days. Older date windows use hourly data.

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
