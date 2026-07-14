<p align="center">
  <img src="https://raw.githubusercontent.com/Robinproxy/Needle/main/internal/server/static/favicon.svg" width="72" height="72" alt="Needle">
</p>

<h1 align="center">Needle</h1>

<p align="center">
  轻量级、纯出站上报的 VPS 监控面板
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

## 设计理念

- **纯出站** — Agent 只上报 ，Server 永远不主动连 Agent
- **面板只读** — 运维全部在终端完成，面板不做写操作
- **零共享密钥** — 各vps相互隔离，一机一密钥

---

## 特色功能

| 特色 | 说明 |
|------|------|
| ⏱ **Traffic 周期** | 按计费周期展示用量 |
| 🎯 **TCPing 多线路** | CMv4 / CUv6 等线路可切换 |
| 🏁 **Region 国旗** | 自定义地区标识 |

---

## 架构

```
┌──────────────┐              ┌──────────────────┐
│  Agent VPS   │── POST ──→   │                  │
│  (唯一 token)│  Bearer      │  Needle Server   │
└──────────────┘  + 指标       │  ┌────────────┐  │
                              │  │ Dashboard  │  │
                              │  └────────────┘  │
                              │  ┌────────────┐  │
                              │  │ SQLite     │  │
                              │  │ 节点数据    │  │
                              │  │ agent_tokens│ │
                              │  └────────────┘  │
                              └──────────────────┘
```


## 命令速查

curl：`apt-get update && apt-get install -y curl`。

<details>
<summary>Server · Docker</summary>

| Operation | Command |
|------|------|
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

> `exec` 不走 ENTRYPOINT：服务名 `needle-server` 后须再写一次二进制名 `needle-server`。

</details>

<details>
<summary>Server · 二进制</summary>

| Operation | Local Script | Pipe |
|------|----------|------|
| Download script | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh` | — |
| Install | `sudo bash /tmp/needle-server.sh install` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \| sudo bash` |
| install/upgrade | `sudo bash /tmp/needle-server.sh` | same (no args) |
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

</details>

<details>
<summary>Agent · 二进制</summary>

| Operation | Local Script | Pipe |
|------|----------|------|
| Download script | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh` | — |
| Install | `sudo bash /tmp/needle-agent.sh install` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash` |
| install/upgrade | `sudo bash /tmp/needle-agent.sh` | same (no args) |
| Upgrade | `sudo bash /tmp/needle-agent.sh upgrade` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash -s -- upgrade` |
| Status | `sudo bash /tmp/needle-agent.sh status` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash -s -- status` |
| Uninstall local | `sudo bash /tmp/needle-agent.sh uninstall` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash -s -- uninstall` |
| Uninstall + notify Server | `sudo bash /tmp/needle-agent.sh uninstall --unregister` | `curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \| sudo bash -s -- uninstall --unregister` |
| Logs | `journalctl -u needle-agent -f` | — |
| Cleanup temp script | `rm -f /tmp/needle-agent.sh` | not needed for pipe |

</details>

<details>
<summary>目录速查</summary>

| Role | Path | Description |
|------|------|------|
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

</details>

---

## 部署详解

### Server · Docker（推荐）

#### 部署

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

# 可选端口：echo "NEEDLE_PORT=8080" >> .env
docker compose up -d
```

> **不再需要全局 `NEEDLE_TOKEN`。** Agent 用独立 token，用 `allow-token` 登记。

<details>
<summary>运维</summary>

```bash
cd ~/needle

# 升级
docker compose pull && docker compose up -d

# 日志
docker compose logs -f needle-server

# 登记 Agent token（安装 Agent 时打印的完整 token）
docker compose exec needle-server \
  needle-server -db /data/needle.db allow-token <token>

# 查看 token 白名单 / 节点
docker compose exec needle-server \
  needle-server -db /data/needle.db list-tokens
docker compose exec needle-server \
  needle-server -db /data/needle.db list-agents

# 吊销 token（已绑定的节点数据会一并清理）
docker compose exec needle-server \
  needle-server -db /data/needle.db -y revoke-token <token>

# 删节点数据
docker compose exec needle-server \
  needle-server -db /data/needle.db delete-agent <hostname|id>
docker compose exec needle-server \
  needle-server -db /data/needle.db -y delete-agent <hostname|id>

# 备份
cp -a data/needle.db data/needle.db.bak

# 卸载（保留数据）
docker compose down

# 卸载（含数据）
docker compose down -v && rm -rf data
```

</details>

#### 目录

| 路径 | 说明 |
|------|------|
| `~/needle/docker-compose.yml` | 编排（路径按你创建的目录） |
| `~/needle/.env` | 可选 `NEEDLE_PORT` |
| `~/needle/data/needle.db` | SQLite（含 token 白名单） |
| 容器内 `/data` | 数据卷挂载点 |
| 镜像 | `ghcr.io/robinproxy/needle:latest` |

本地构建：

```bash
git clone https://github.com/Robinproxy/Needle.git && cd Needle
docker compose up -d --build
```

---

### Server · 二进制（systemd）

#### 部署

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh install
```

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh | sudo bash
```

<details>
<summary>运维 · 脚本（二选一）</summary>

本地脚本：

```bash
sudo bash /tmp/needle-server.sh              # 智能 install / upgrade
sudo bash /tmp/needle-server.sh upgrade      # 只换二进制，保留 .env 与 data/
sudo bash /tmp/needle-server.sh status
sudo bash /tmp/needle-server.sh uninstall    # 停服务 + 删二进制，默认保留 data/ 与 .env
sudo bash /tmp/needle-server.sh uninstall --purge   # 连 data/ 与 .env 一起删除
```

管道：

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

</details>

<details>
<summary>运维 · Token / 节点 CLI</summary>

```bash
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token <token>
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-tokens
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent <hostname|id>
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y delete-agent <hostname|id>
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y revoke-token <token>

journalctl -u needle-server -f
```

> `delete-agent` 只删 Server 库数据，不会停远端 Agent。若 Agent 仍在上报，节点会重新出现。

用完可删临时脚本：

```bash
rm -f /tmp/needle-server.sh
```

</details>

#### 目录

| 路径 | 说明 |
|------|------|
| `/opt/needle/bin/needle-server` | Server 二进制 |
| `/opt/needle/.env` | `NEEDLE_LISTEN`（权限 600） |
| `/opt/needle/data/needle.db` | SQLite |
| `/etc/systemd/system/needle-server.service` | systemd unit |

也可从 [Releases](https://github.com/Robinproxy/Needle/releases) 解压后前台运行（无 systemd）：

```bash
tar xzf needle-linux-amd64.tar.gz needle-server
./needle-server -l :8008 -db ./data/needle.db
```

---

### Agent · 二进制（systemd）

在每台 VPS 上执行。

#### 部署

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \
  -o /tmp/needle-agent.sh
sudo bash /tmp/needle-agent.sh install
```

管道：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh | sudo bash
```

安装结束会**自动生成独立 token** 并打印，例如：

```text
Agent token: a1b2c3d4...
  sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db allow-token a1b2c3d4...
  docker compose exec needle-server \
    needle-server -db /data/needle.db allow-token a1b2c3d4...
```

**请到 Server 上执行 `allow-token`，否则上报 401。** Token 保存在本机 `agent.yaml`。

<details>
<summary>运维 · 脚本（二选一）</summary>

本地脚本：

```bash
sudo bash /tmp/needle-agent.sh              # 智能 install / upgrade
sudo bash /tmp/needle-agent.sh upgrade      # 零交互升级，保留 agent.yaml（含 token）
sudo bash /tmp/needle-agent.sh status
sudo bash /tmp/needle-agent.sh uninstall    # 仅卸本机（默认，不碰 Server 库）
sudo bash /tmp/needle-agent.sh uninstall --unregister   # 先通知 Server 删节点，再卸本机
```

管道：

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

日志与清理：

```bash
journalctl -u needle-agent -f
rm -f /tmp/needle-agent.sh
```

</details>

#### 目录

| 路径 | 说明 |
|------|------|
| `/opt/needle-agent/bin/needle-agent` | Agent 二进制 |
| `/opt/needle-agent/agent.yaml` | 配置（含**独立 token**，权限 600） |
| `/etc/systemd/system/needle-agent.service` | systemd unit |

---

## 配置

### Server

| 变量 | 说明 | 默认 |
|------|------|------|
| `NEEDLE_LISTEN` | 监听地址 | `:8008` |
| `NEEDLE_PORT` | Docker 宿主机端口 | `8008` |

> 已无全局 `NEEDLE_TOKEN`。鉴权仅靠数据库白名单（`allow-token`）。

### agent.yaml

```yaml
hostname: ""                                     # 可选，默认系统主机名
server: http://1.2.3.4:8008                      # 必填，Server 地址
token: replace-with-unique-agent-token           # 每台唯一；须在 Server allow-token
region: SG                                       # ISO 国家码
billing_period: "1m"                             # 1m/3m/6m/12m，可选
expires_at: "2026-08-15"                         # YYYY-MM-DD，可选
interval: 30                                     # 上报间隔（秒）
insecure: false                                  # true = 跳过 TLS 证书验证
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

## 常见场景

| 场景 | 做法 |
|------|------|
| 删除 VPS | Agent：`uninstall --unregister`； Server：`delete-agent` / `revoke-token` |
| 更换 hostname | 需 `revoke-token` 再 `allow-token` |

---

## 感谢

- **开发工具** — [opencode](https://opencode.ai) + [DeepSeek V4 Flash](https://deepseek.com/) + [Grok](https://x.ai/) + [GLM](https://zhipuai.cn/) 提供 AI 辅助编码
- **TCPing 节点** — [zstaticcdn](https://lf3-ips.zstaticcdn.com/) 提供全球探测节点
- **主题 UI 参考** — [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2) 的仪表盘设计灵感
