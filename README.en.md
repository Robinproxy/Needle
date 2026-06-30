<p align="center">
  <img src="https://raw.githubusercontent.com/Robinproxy/Needle/main/internal/server/static/favicon.svg" width="72" height="72" alt="Needle">
</p>

<h1 align="center">Needle</h1>

<p align="center">
  <a href="README.md">中文</a> · <a href="README.en.md">English</a>
</p>

<p align="center">
  Lightweight, pure-outbound VPS monitoring dashboard
</p>

<p align="center">
  <a href="https://github.com/Robinproxy/Needle/releases"><img src="https://img.shields.io/github/v/release/Robinproxy/Needle?style=flat-square" alt="Release"></a>
  <a href="https://github.com/Robinproxy/Needle/blob/main/LICENSE"><img src="https://img.shields.io/github/license/Robinproxy/Needle?style=flat-square" alt="License"></a>
  <a href="https://github.com/Robinproxy/Needle/actions"><img src="https://img.shields.io/github/actions/workflow/status/Robinproxy/Needle/docker.yml?branch=main&style=flat-square" alt="Build"></a>
</p>

---

## Design Principles

| Principle | Description |
|-----------|-------------|
| 🔒 **Security Minimal** | Agent reports only, Server never initiates connections. No WebSSH, no command execution |
| 🔄 **Graceful Upgrade** | No forced Agent upgrades. New features via optional fields, backward compatible |
| 📦 **Minimal Deployment** | Two standalone binaries for Server + Agent. SQLite zero configuration, no external dependencies |
| 🕶️ **Privacy First** | Customizable Hostname/Region. Your data stays on your infrastructure |

## Features

| Feature | Description |
|---------|-------------|
| ⏱ **Billing Countdown** | Real-time days-until-next-billing display, hover for due date, auto-renewal by billing period |
| 🎯 **TCPing Target Switching** | Click CMv4/CUv6 labels on cards to cycle through probe lines |
| 📊 **Sparkline Trends** | Expand a card to view CPU / Memory / Network traffic mini trend charts |
| 🔴 **One-Click Offline Cleanup** | Click the red status dot on an offline node to delete it and all its data |
| 🌍 **Region Filtering** | Top info bar shows country flags and node counts. Click to filter by region |

## Architecture

```
                        ┌─ Agent (VPS 1) ─┐
                        │  CMv4/CUv6/CTv4 │
                        └───────┬─────────┘
                                │ POST /api/report
                        ┌─ Agent (VPS 2) ─┐      Bearer Token
                        │  CMv4/CUv6/CTv4 │──────────┐
                        └───────┬─────────┘          │
                                │ POST /api/report   │
                                                 ┌───┴──────────┐
                        ┌─ Agent (VPS N) ─┐      │ Needle Server│
                        │  CMv4/CUv6/CTv4 │─────→│  (Dashboard) │
                        └─────────────────┘      │  SQLite DB   │
                                                 └──────────────┘
```

---

## Quick Start

### Docker (Recommended)

```bash
mkdir -p ~/needle && cd ~/needle

cat > docker-compose.yml << 'EOF'
services:
  needle-server:
    image: ghcr.io/robinproxy/needle:latest
    ports:
      - "${NEEDLE_PORT:-8008}:8008"
    environment:
      NEEDLE_TOKEN: "${NEEDLE_TOKEN:?error: set NEEDLE_TOKEN in .env}"
    volumes:
      - ./data:/data
    restart: unless-stopped
EOF

echo "NEEDLE_TOKEN=$(openssl rand -hex 16)" > .env
docker compose up -d
```

Custom port:

```bash
echo "NEEDLE_PORT=8080" >> .env
docker compose up -d
```

### Binary

```bash
# Download the tarball for your architecture from Releases
TOKEN=$(openssl rand -hex 16)
tar xzf needle-linux-amd64.tar.gz needle-server
./needle-server -l :8008 -token "$TOKEN"
# Or run in background
nohup ./needle-server -l :8008 -token "$TOKEN" > needle.log 2>&1 &
```

### One-Line Install (systemd)

Server:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-server.sh | sudo bash
```

Agent (run on each VPS):

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-agent.sh | sudo bash
```

### Docker Build Locally

```bash
git clone https://github.com/Robinproxy/Needle.git
cd Needle
echo "NEEDLE_TOKEN=$(openssl rand -hex 16)" > .env
docker compose up -d --build
```

---

## Configuration

### agent.yaml

```yaml
hostname: ""                                     # Optional, defaults to OS hostname
server: http://1.2.3.4:8008                      # Required, Server address
token: your-token                                # Required, must match Server token
region: SG                                       # ISO country code, e.g. CN/SG/US
billing_period: "1m"                             # 1m/3m/6m/12m, optional, billing cycle
expires_at: "2026-08-15"                         # YYYY-MM-DD, optional, expiry date
interval: 30                                     # Report interval (seconds)
insecure: false                                  # Disable TLS verification (self-signed certs)
tcpping:
  - name: "CMv4"
    target: "sh-cm-v4.ip.zstaticcdn.com:80"
  - name: "CMv6"
    target: "sh-cm-v6.ip.zstaticcdn.com:80"
  - name: "CUv4"
    target: "sh-cu-v4.ip.zstaticcdn.com:80"
  - name: "CUv6"
    target: "sh-cu-v6.ip.zstaticcdn.com:80"
  - name: "CTv4"
    target: "sh-ct-v4.ip.zstaticcdn.com:80"
  - name: "CTv6"
    target: "sh-ct-v6.ip.zstaticcdn.com:80"
```

### Server Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `NEEDLE_TOKEN` | Auth token, required for Agent connections | **Required** |
| `NEEDLE_LISTEN` | Listen address (binary mode, e.g. `:9000`) | `:8008` |
| `NEEDLE_PORT` | Docker host port mapping (digits only) | `8008` |

---

## Install Paths

### Server (systemd)

| Item | Path |
|------|------|
| Binary | `/opt/needle/bin/needle-server` |
| Environment file | `/opt/needle/.env` |
| Database | `/opt/needle/data/needle.db` |
| Logs | `journalctl -u needle-server -f` |

### Docker

| Item | Path |
|------|------|
| Data directory | `./data/` |
| Database | `./data/needle.db` |

### Agent (systemd)

| Item | Path |
|------|------|
| Binary | `/opt/needle-agent/bin/needle-agent` |
| Config file | `/opt/needle-agent/agent.yaml` |
| Logs | `journalctl -u needle-agent -f` |

---

## Uninstall

```bash
# Server (binary + systemd)
sudo systemctl stop needle-server
sudo systemctl disable needle-server
sudo rm /etc/systemd/system/needle-server.service
sudo rm -rf /opt/needle

# Server (Docker)
docker compose down -v
rm -rf ./data

# Agent
sudo systemctl stop needle-agent
sudo systemctl disable needle-agent
sudo rm /etc/systemd/system/needle-agent.service
sudo rm -rf /opt/needle-agent
```

---

## Credits

- **TCPing Nodes** — [zstaticcdn](https://lf3-ips.zstaticcdn.com/) provides global probe endpoints
- **UI Inspiration** — [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2) dashboard design
