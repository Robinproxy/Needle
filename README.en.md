<p align="center">
  <img src="https://raw.githubusercontent.com/Robinproxy/Needle/main/internal/server/static/favicon.svg" width="72" height="72" alt="Needle">
</p>

<h1 align="center">Needle</h1>

<p align="center">
  Lightweight, pure-outbound VPS monitoring dashboard
</p>

<p align="center">
  <a href="README.md">中文</a> · <a href="README.en.md">English</a>
</p>

<p align="center">
  <a href="https://github.com/Robinproxy/Needle/releases"><img src="https://img.shields.io/github/v/release/Robinproxy/Needle?style=flat-square" alt="Release"></a>
  <a href="https://github.com/Robinproxy/Needle/blob/main/LICENSE"><img src="https://img.shields.io/github/license/Robinproxy/Needle?style=flat-square" alt="License"></a>
  <a href="https://github.com/Robinproxy/Needle/actions"><img src="https://img.shields.io/github/actions/workflow/status/Robinproxy/Needle/docker.yml?branch=main&style=flat-square" alt="Build"></a>
</p>

---

## Design Principles

- **Pure outbound** — Agent reports only; Server never initiates connections to Agent
- **Read-only dashboard** — All ops happen in the terminal; the panel never writes
- **No shared secrets** — VPS nodes are isolated; one unique token per machine

---

## Highlights

| Feature | Description |
|---------|-------------|
| ⏱ **Traffic cycle** | Usage shown per billing period |
| 🎯 **TCPing multi-line** | Switch CMv4 / CUv6 and other probe lines |
| 🏁 **Region flags** | Custom region labels |

---

## Architecture

```
┌──────────────┐              ┌──────────────────┐
│  Agent VPS   │── POST ──→   │                  │
│  (unique     │  Bearer      │  Needle Server   │
│   token)     │  + metrics   │  ┌────────────┐  │
└──────────────┘              │  │ Dashboard  │  │
                              │  └────────────┘  │
                              │  ┌────────────┐  │
                              │  │ SQLite     │  │
                              │  │ node data  │  │
                              │  │ agent_tokens│ │
                              │  └────────────┘  │
                              └──────────────────┘
```


## Command Cheatsheet

Scripts support **curl or wget**. No curl: `apt-get update && apt-get install -y curl`.

### Server · Docker

| Action | Command |
|--------|---------|
| Deploy | See full compose block under [Deploy · Docker](#server--dockerrecommended) |
| Upgrade | `cd ~/needle && docker compose pull && docker compose up -d` |
| Logs | `docker compose logs -f needle-server` |
| Allow token | `docker compose exec needle-server needle-server -db /data/needle.db allow-token <token>` |
| List tokens | `docker compose exec needle-server needle-server -db /data/needle.db list-tokens` |
| List agents | `docker compose exec needle-server needle-server -db /data/needle.db list-agents` |
| Revoke token | `docker compose exec needle-server needle-server -db /data/needle.db -y revoke-token <token>` |
| Delete agent | `docker compose exec needle-server needle-server -db /data/needle.db delete-agent <hostname\|id>` |
| Delete agent (-y) | `docker compose exec needle-server needle-server -db /data/needle.db -y delete-agent <hostname\|id>` |
| Backup | `cp -a data/needle.db data/needle.db.bak` |
| Uninstall (keep data) | `docker compose down` |
| Uninstall (purge) | `docker compose down -v && rm -rf data` |

> `exec` skips ENTRYPOINT: after service name `needle-server`, write the binary name `needle-server` again.

### Server · Binary

| Action | Local script | Pipe |
|--------|--------------|------|
| Download script | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh` | — |
| Install | `sudo bash /tmp/needle-server.sh install` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \| sudo bash` |
| Smart install/upgrade | `sudo bash /tmp/needle-server.sh` | same (no args) |
| Upgrade | `sudo bash /tmp/needle-server.sh upgrade` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \| sudo bash -s -- upgrade` |
| Status | `sudo bash /tmp/needle-server.sh status` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \| sudo bash -s -- status` |
| Uninstall (keep data) | `sudo bash /tmp/needle-server.sh uninstall` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \| sudo bash -s -- uninstall` |
| Uninstall (purge) | `sudo bash /tmp/needle-server.sh uninstall --purge` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \| sudo bash -s -- uninstall --purge` |
| Allow token | `sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token <token>` | — |
| List tokens | `sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-tokens` | — |
| List agents | `sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents` | — |
| Delete agent | `sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent <hostname\|id>` | — |
| Delete agent (-y) | `sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y delete-agent <hostname\|id>` | — |
| Revoke token | `sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y revoke-token <token>` | — |
| Logs | `journalctl -u needle-server -f` | — |
| Cleanup temp script | `rm -f /tmp/needle-server.sh` | not needed for pipe |

### Agent · Binary

| Action | Local script | Pipe |
|--------|--------------|------|
| Download script | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh` | — |
| Install | `sudo bash /tmp/needle-agent.sh install` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash` |
| Smart install/upgrade | `sudo bash /tmp/needle-agent.sh` | same (no args) |
| Upgrade | `sudo bash /tmp/needle-agent.sh upgrade` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash -s -- upgrade` |
| Status | `sudo bash /tmp/needle-agent.sh status` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash -s -- status` |
| Uninstall local | `sudo bash /tmp/needle-agent.sh uninstall` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash -s -- uninstall` |
| Uninstall + notify Server | `sudo bash /tmp/needle-agent.sh uninstall --unregister` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash -s -- uninstall --unregister` |
| Logs | `journalctl -u needle-agent -f` | — |
| Cleanup temp script | `rm -f /tmp/needle-agent.sh` | not needed for pipe |

### Path Cheatsheet

| Role | Path | Notes |
|------|------|-------|
| Docker | `~/needle/docker-compose.yml` | Compose file |
| Docker | `~/needle/.env` | Optional `NEEDLE_PORT` |
| Docker | `~/needle/data/needle.db` | SQLite (token allow-list) |
| Docker | `/data` in container | Data volume |
| Server binary | `/opt/needle/bin/needle-server` | Binary |
| Server binary | `/opt/needle/.env` | `NEEDLE_LISTEN` (mode 600) |
| Server binary | `/opt/needle/data/needle.db` | SQLite |
| Server binary | `/etc/systemd/system/needle-server.service` | unit |
| Agent | `/opt/needle-agent/bin/needle-agent` | Binary |
| Agent | `/opt/needle-agent/agent.yaml` | Config + **per-agent token** (mode 600) |
| Agent | `/etc/systemd/system/needle-agent.service` | unit |

---

## Deploy Details

### Server · Docker (Recommended)

#### Deploy

```bash
mkdir -p ~/needle && cd ~/needle

cat > docker-compose.yml << 'EOF'
services:
  needle-server:
    image: ghcr.io/robinproxy/needle:latest
    ports:
      - "${NEEDLE_PORT:-8008}:8008"
    environment:
      NEEDLE_LISTEN: ":8008"
    volumes:
      - ./data:/data
    restart: unless-stopped
EOF

# Optional port: echo "NEEDLE_PORT=8080" >> .env
docker compose up -d
```

> **No global `NEEDLE_TOKEN`.** Each Agent has its own token; register with `allow-token`.

#### Ops

```bash
cd ~/needle

# Upgrade
docker compose pull && docker compose up -d

# Logs
docker compose logs -f needle-server

# Allow Agent token (full token printed at Agent install)
docker compose exec needle-server \
  needle-server -db /data/needle.db allow-token <token>

# List token allow-list / agents
docker compose exec needle-server \
  needle-server -db /data/needle.db list-tokens
docker compose exec needle-server \
  needle-server -db /data/needle.db list-agents

# Revoke token (bound node data is cleaned up too)
docker compose exec needle-server \
  needle-server -db /data/needle.db -y revoke-token <token>

# Delete agent data
docker compose exec needle-server \
  needle-server -db /data/needle.db delete-agent <hostname|id>
docker compose exec needle-server \
  needle-server -db /data/needle.db -y delete-agent <hostname|id>

# Backup
cp -a data/needle.db data/needle.db.bak

# Uninstall (keep data)
docker compose down

# Uninstall (purge data)
docker compose down -v && rm -rf data
```

#### Paths

| Path | Notes |
|------|-------|
| `~/needle/docker-compose.yml` | Compose (path = your install dir) |
| `~/needle/.env` | Optional `NEEDLE_PORT` |
| `~/needle/data/needle.db` | SQLite (token allow-list) |
| `/data` in container | Volume mount |
| Image | `ghcr.io/robinproxy/needle:latest` |

Build locally:

```bash
git clone https://github.com/Robinproxy/Needle.git && cd Needle
docker compose up -d --build
```

---

### Server · Binary (systemd)

#### Deploy

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh install
```

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh | sudo bash
```

#### Ops · Script (pick one)

Local script:

```bash
sudo bash /tmp/needle-server.sh              # smart install / upgrade
sudo bash /tmp/needle-server.sh upgrade      # binary only; keeps .env and data/
sudo bash /tmp/needle-server.sh status
sudo bash /tmp/needle-server.sh uninstall    # stop + remove binary; keeps data/ and .env by default
sudo bash /tmp/needle-server.sh uninstall --purge   # also remove data/ and .env
```

Pipe:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  | sudo bash -s -- upgrade
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  | sudo bash -s -- status
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  | sudo bash -s -- uninstall
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  | sudo bash -s -- uninstall --purge
```

#### Ops · Token / Agent CLI

```bash
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token <token>
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-tokens
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent <hostname|id>
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y delete-agent <hostname|id>
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y revoke-token <token>

journalctl -u needle-server -f
```

> `delete-agent` only removes Server DB rows; it does not stop the remote Agent. If the Agent still reports, the node will reappear.

Remove temp script when done:

```bash
rm -f /tmp/needle-server.sh
```

#### Paths

| Path | Notes |
|------|-------|
| `/opt/needle/bin/needle-server` | Server binary |
| `/opt/needle/.env` | `NEEDLE_LISTEN` (mode 600) |
| `/opt/needle/data/needle.db` | SQLite |
| `/etc/systemd/system/needle-server.service` | systemd unit |

Or run from [Releases](https://github.com/Robinproxy/Needle/releases) in foreground (no systemd):

```bash
tar xzf needle-linux-amd64.tar.gz needle-server
./needle-server -l :8008 -db ./data/needle.db
```

---

### Agent · Binary (systemd)

Run on each VPS.

#### Deploy

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \
  -o /tmp/needle-agent.sh
sudo bash /tmp/needle-agent.sh install
```

Pipe:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh | sudo bash
```

Install **auto-generates a unique token** and prints it, for example:

```text
Agent token: a1b2c3d4...
  sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token a1b2c3d4...
  docker compose exec needle-server \
    needle-server -db /data/needle.db allow-token a1b2c3d4...
```

**Run `allow-token` on the Server, or reports get 401.** Token is stored in local `agent.yaml`.

#### Ops · Script (pick one)

Local script:

```bash
sudo bash /tmp/needle-agent.sh              # smart install / upgrade
sudo bash /tmp/needle-agent.sh upgrade      # zero-interaction upgrade; keeps agent.yaml (incl. token)
sudo bash /tmp/needle-agent.sh status
sudo bash /tmp/needle-agent.sh uninstall    # local only (default; Server DB untouched)
sudo bash /tmp/needle-agent.sh uninstall --unregister   # notify Server to drop node, then uninstall local
```

Pipe:

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \
  | sudo bash -s -- upgrade
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \
  | sudo bash -s -- status
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \
  | sudo bash -s -- uninstall
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \
  | sudo bash -s -- uninstall --unregister
```

Logs and cleanup:

```bash
journalctl -u needle-agent -f
rm -f /tmp/needle-agent.sh
```

#### Paths

| Path | Notes |
|------|-------|
| `/opt/needle-agent/bin/needle-agent` | Agent binary |
| `/opt/needle-agent/agent.yaml` | Config (**unique token**, mode 600) |
| `/etc/systemd/system/needle-agent.service` | systemd unit |

---

## Configuration

### Server

| Variable | Description | Default |
|----------|-------------|---------|
| `NEEDLE_LISTEN` | Listen address | `:8008` |
| `NEEDLE_PORT` | Docker host port | `8008` |

> No global `NEEDLE_TOKEN`. Auth is DB allow-list only (`allow-token`).

### agent.yaml

```yaml
hostname: ""                                     # optional; defaults to OS hostname
server: http://1.2.3.4:8008                      # required; Server URL
token: replace-with-unique-agent-token           # unique per host; must allow-token on Server
region: SG                                       # ISO country code
billing_period: "1m"                             # 1m/3m/6m/12m, optional
expires_at: "2026-08-15"                         # YYYY-MM-DD, optional
interval: 30                                     # report interval (seconds)
insecure: false                                  # true = skip TLS cert verify
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

---

## Common Scenarios

| Scenario | What to do |
|----------|------------|
| New Agent never online | Did you run `allow-token` on Server? |
| Where is the token | Agent: `/opt/needle-agent/agent.yaml`; Server: `agent_tokens` in `needle.db` |
| Remove a VPS from panel | Agent: `uninstall --unregister`; or Server: `delete-agent` / `revoke-token` |
| Deleted node reappears | Agent still reporting → stop/uninstall Agent, then delete |
| Change hostname (token already bound) | Gets 401; `revoke-token` then `allow-token`, or issue a new token |
| Upgrade from old shared token | **Incompatible**: regenerate token per host and `allow-token` |

---

## Credits

- **Development tools** — [opencode](https://opencode.ai) + [DeepSeek V4 Flash](https://deepseek.com/) + [Grok](https://x.ai/) + [GLM](https://zhipuai.cn/) for AI-assisted coding
- **TCPing nodes** — [zstaticcdn](https://lf3-ips.zstaticcdn.com/) global probe endpoints
- **UI inspiration** — [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2) dashboard design
