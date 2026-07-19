# Needle

<p align="center">
  <img src="https://raw.githubusercontent.com/Robinproxy/Needle/main/internal/server/static/favicon.svg" width="72" height="72" alt="Needle">
</p>

<p align="center">
  A lightweight, single-binary traffic and system monitor for personal servers
</p>

<p align="center">
  <a href="README.md">中文</a> ·
  <a href="https://github.com/Robinproxy/Needle/releases">Releases</a> ·
  <a href="https://github.com/Robinproxy/Needle/pkgs/container/needle">Container</a>
</p>

<p align="center">
  <a href="https://github.com/Robinproxy/Needle/releases"><img src="https://img.shields.io/github/v/release/Robinproxy/Needle?style=flat-square" alt="Release"></a>
  <a href="https://github.com/Robinproxy/Needle/blob/main/LICENSE"><img src="https://img.shields.io/github/license/Robinproxy/Needle?style=flat-square" alt="License"></a>
  <a href="https://github.com/Robinproxy/Needle/actions"><img src="https://img.shields.io/github/actions/workflow/status/Robinproxy/Needle/docker.yml?branch=main&style=flat-square" alt="Build"></a>
</p>

Needle consists of one Server and multiple Agents. Agents make outbound-only connections to collect CPU, memory, network traffic, and TCP Ping metrics. The Server stores them in SQLite and serves a read-only web dashboard.

## Features

- CPU, memory, real-time network rates, and billing-cycle traffic usage
- Multi-route TCP Ping monitoring with per-route show and hide controls
- Raw `1d` history and a downsampled `7d` overview for detail without slow rendering
- Click a date below the seven-day chart to load raw CPU, memory, traffic, and TCP Ping data for that day
- Anomaly dates are marked with a small red dot and a hover summary without covering chart lines
- Automatic refresh with distinct loading, no-data, stale-data, and request-error states
- A unique token per Agent, bound to its node after the first successful report
- Single-binary deployment, SQLite storage, and no frontend build dependencies
- HTTPS, Cloudflare Tunnel, Docker Compose, and systemd support

## Architecture

```text
Agent A ─┐
Agent B ─┼── HTTPS ──> Needle Server ──> SQLite
Agent C ─┘                   │
                             └── Web Dashboard
```

Agents do not need inbound ports. In production, connect them to the Server through an HTTPS hostname provided by Cloudflare Tunnel or a reverse proxy.

<details>
<summary><strong>Quick Start</strong></summary>

<br>

### 1. Deploy the Server (Docker Compose recommended)

```bash
mkdir -p ~/needle/data
cd ~/needle
curl -fsSLO https://raw.githubusercontent.com/Robinproxy/Needle/main/docker-compose.yml
docker compose up -d
```

By default, the service is bound to `127.0.0.1:8008` on the host and the database is stored at `~/needle/data/needle.db`.

Check the service:

```bash
docker compose ps
docker compose logs --tail=100 needle-server
curl -fsS http://127.0.0.1:8008/api/health
```

### 2. Configure HTTPS

For Cloudflare Tunnel:

- If cloudflared runs on the host, set the origin service to `http://127.0.0.1:8008`.
- If cloudflared and Needle share a Docker network, use `http://needle-server:8008`.
- Set the Agent Server URL to the public HTTPS address, such as `https://needle.example.com`.

Using HTTP between the Tunnel and a local service is expected; traffic from the Agent to the public hostname remains protected by HTTPS.

### 3. Install an Agent

Run this on every server to be monitored:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh
sudo bash /tmp/needle-agent.sh install
```

The installer generates a unique token and writes `/opt/needle-agent/agent.yaml`. Set `server` to the Server's HTTPS address, then allow the token on the Server.

Docker Server:

```bash
cd ~/needle
docker compose exec needle-server needle-server -db /data/needle.db allow-token YOUR_TOKEN
```

Binary or systemd Server:

```bash
sudo /opt/needle/bin/needle-server \
  -db /opt/needle/data/needle.db \
  allow-token YOUR_TOKEN
```

Restart the Agent:

```bash
sudo systemctl restart needle-agent
sudo systemctl status needle-agent --no-pager
```

Open `https://needle.example.com` to view the dashboard.

</details>

<details>
<summary><strong>History Views</strong></summary>

<br>

| View | Data | Best for |
| --- | --- | --- |
| `1d` | Raw samples | Inspecting specific changes during the last 24 hours |
| `7d` | 15-minute downsampled buckets | Reviewing weekly trends with lower loading and rendering cost |
| Date view | Raw samples for one calendar day | Drilling down from the seven-day overview |

In the `7d` view, click a date below the TCP Ping chart to switch every chart to that day. A red dot marks a date with an anomaly summary. Click `7d` to return to the weekly overview.

</details>

<details>
<summary><strong>Agent Configuration</strong></summary>

<br>

Configuration file: `/opt/needle-agent/agent.yaml`

```yaml
hostname: ""
server: https://needle.example.com
token: replace-with-unique-agent-token
region: SG

billing_period: "1m"
expires_at: "2026-08-15"
interval: 30

tls_skip_verify: false
allow_plain_http: false

tcpping:
  - name: CMv4
    target: sh-cm-v4.ip.zstaticcdn.com:80
```

Key fields:

| Field | Description |
| --- | --- |
| `hostname` | Uses the system hostname when empty |
| `server` | Server URL; HTTPS is required for production |
| `token` | A unique authentication token for this Agent |
| `region` | Region code shown on the dashboard |
| `billing_period` | Traffic billing cycle, for example `1m` starts on the first day of each month |
| `expires_at` | Server renewal date in `YYYY-MM-DD` format |
| `interval` | Reporting interval in seconds |
| `tls_skip_verify` | Skips TLS certificate verification; use only for temporary troubleshooting |
| `allow_plain_http` | Allows a remote plaintext HTTP Server; not recommended |
| `tcpping` | TCP Ping target list |

After editing the configuration, restart and check the Agent:

```bash
sudo nano /opt/needle-agent/agent.yaml
sudo systemctl restart needle-agent
sudo journalctl -u needle-agent -n 100 --no-pager
```

Remote HTTP is rejected by default; loopback addresses such as `http://127.0.0.1` remain allowed. Do not enable `tls_skip_verify` or `allow_plain_http` permanently to work around certificate problems.

</details>

## Operations

<details>
<summary><strong>Docker Server</strong></summary>

<br>

Check status and follow logs:

```bash
cd ~/needle
docker compose ps
docker compose logs -f --tail=100 needle-server
```

Upgrade the image:

```bash
cd ~/needle
docker compose pull
docker compose up -d
docker image prune -f
```

Restart or stop the service:

```bash
docker compose restart needle-server
docker compose stop needle-server
docker compose start needle-server
```

Manage Agents and tokens:

```bash
# List registered nodes
docker compose exec needle-server needle-server -db /data/needle.db list-agents

# List allowed tokens
docker compose exec needle-server needle-server -db /data/needle.db list-tokens

# Add a token
docker compose exec needle-server needle-server -db /data/needle.db allow-token YOUR_TOKEN

# Revoke a token and delete its bound node data
docker compose exec needle-server needle-server -db /data/needle.db revoke-token YOUR_TOKEN

# Delete node data by ID or hostname
docker compose exec needle-server needle-server -db /data/needle.db delete-agent HOSTNAME_OR_ID
```

`delete-agent` only removes Server-side data. If the Agent is still reporting, the node will reappear. To remove it permanently, run `uninstall --unregister` on the Agent or revoke its token first.

#### Backup and restore

Do not copy a single SQLite file while it may be written. For a consistent backup, briefly stop the Server and copy the entire data directory:

```bash
cd ~/needle
docker compose stop needle-server
cp -a data "data.backup.$(date +%Y%m%d-%H%M%S)"
docker compose start needle-server
```

Stop the service before restoring and preserve the current data directory:

```bash
cd ~/needle
docker compose down
mv data "data.failed.$(date +%Y%m%d-%H%M%S)"
cp -a data.backup.YYYYMMDD-HHMMSS data
docker compose up -d
```

After verifying the restore, remove obsolete backup directories manually.

</details>

<details>
<summary><strong>systemd Server</strong></summary>

<br>

Install:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh install
```

Upgrade, inspect status, and view logs:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  | sudo bash -s -- upgrade
sudo systemctl status needle-server --no-pager
sudo journalctl -u needle-server -f
```

Back up the database:

```bash
sudo systemctl stop needle-server
sudo cp -a /opt/needle/data "/opt/needle/data.backup.$(date +%Y%m%d-%H%M%S)"
sudo systemctl start needle-server
```

Remove the program while keeping configuration and data:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh uninstall
```

Remove the program, configuration, and data:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh uninstall --purge
```

</details>

<details>
<summary><strong>Agent Binary Operations</strong></summary>

<br>

The installation script deploys an Agent binary managed by systemd. Download the operations script first:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh
```

Install the Agent:

```bash
sudo bash /tmp/needle-agent.sh install
```

Inspect installation details, service status, and recent logs:

```bash
sudo bash /tmp/needle-agent.sh status
sudo systemctl status needle-agent --no-pager
sudo journalctl -u needle-agent -n 100 --no-pager
```

Follow live logs:

```bash
sudo journalctl -u needle-agent -f
```

Start, stop, or restart the Agent:

```bash
sudo systemctl start needle-agent
sudo systemctl stop needle-agent
sudo systemctl restart needle-agent
```

Edit the configuration, restart, and confirm connectivity in the logs:

```bash
sudo nano /opt/needle-agent/agent.yaml
sudo systemctl restart needle-agent
sudo journalctl -u needle-agent -n 50 --no-pager
```

Upgrade the binary. The existing `/opt/needle-agent/agent.yaml` is preserved:

```bash
sudo bash /tmp/needle-agent.sh upgrade
sudo systemctl status needle-agent --no-pager
```

For foreground troubleshooting, stop the systemd service first to avoid duplicate reports:

```bash
sudo systemctl stop needle-agent
sudo /opt/needle-agent/bin/needle-agent /opt/needle-agent/agent.yaml
# Press Ctrl+C, then restore the service
sudo systemctl start needle-agent
```

Remove only the local binary and systemd service while retaining Server history:

```bash
sudo bash /tmp/needle-agent.sh uninstall
```

Ask the Server to delete the node before uninstalling:

```bash
sudo bash /tmp/needle-agent.sh uninstall --unregister
```

</details>

<details>
<summary><strong>Common Paths</strong></summary>

<br>

| Item | Path |
| --- | --- |
| Server binary | `/opt/needle/bin/needle-server` |
| Server environment | `/opt/needle/.env` |
| Server database | `/opt/needle/data/needle.db` |
| Agent binary | `/opt/needle-agent/bin/needle-agent` |
| Agent configuration | `/opt/needle-agent/agent.yaml` |
| systemd units | `/etc/systemd/system/needle-server.service`, `needle-agent.service` |

</details>

<details>
<summary><strong>Upgrade Notes</strong></summary>

<br>

- Dashboard and Server API features require only a Server upgrade.
- Upgrade Agents only for collector, transport security, or local configuration changes.
- The Server upgrade script preserves the database and `.env`; the Agent upgrade script preserves `agent.yaml`.
- Back up the Server data directory and read the relevant release notes before upgrading.

</details>

<details>
<summary><strong>Security Recommendations</strong></summary>

<br>

- Use HTTPS for public access and bind the Server port only to loopback or a private network.
- Give every Agent a unique token and never reuse one across hosts.
- If a token leaks, run `revoke-token` immediately and issue a new token for the node.
- Never commit the database, `.env`, `agent.yaml`, or tokens to source control.
- Cloudflare Tunnel and reverse proxies do not replace the Server's token validation.

</details>

<details>
<summary><strong>Build from Source</strong></summary>

<br>

Go 1.24 or newer is required:

```bash
git clone https://github.com/Robinproxy/Needle.git
cd Needle
go test ./...
go build -o needle-server ./cmd/server
go build -o needle-agent ./cmd/agent
```

Server flags:

```text
-l       Listen address; NEEDLE_LISTEN is also supported
-db      SQLite database path
-cert    TLS certificate path
-key     TLS private key path
-y       Skip confirmation for delete and revoke operations
```

</details>

## Credits

- Thanks to [akiasprin](https://github.com/akiasprin) for open-sourcing [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2), which inspired parts of Needle's dashboard design.
- Thanks to [zstaticcdn](https://lf3-ips.zstaticcdn.com/) for providing TCP Ping endpoints.
- Thanks to [OpenCode](https://opencode.ai/), [DeepSeek](https://deepseek.com/), [Grok](https://x.ai/), [GLM](https://zhipuai.cn/), and [OpenAI Codex](https://openai.com/codex/) for development and code-review assistance.
- Thanks to every open-source author, contributor, and Needle user who has shared feedback.

## License

[MIT](LICENSE)
