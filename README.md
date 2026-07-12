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

Needle 把监控拆成两个**互不指挥**的角色：

| 角色 | 做什么 | 不做什么 |
|------|--------|----------|
| **Server** | 接收上报、写入 SQLite、**只做展示**（仪表盘） | 不主动连接 Agent、无 WebSSH、无远程命令、**面板上不能删节点** |
| **Agent** | 本机采集指标，**纯出站** `POST /api/report` | 无入站控制面；Server 被攻破也**不能**操控 Agent 主机 |

要点：

1. **互不干扰** — 两边进程生命周期独立；停 Server 不影响 Agent，停 Agent 只是面板上线状态变化。  
2. **运维全在终端** — 安装、升级、卸载、列节点、清库记录都在 SSH 里完成，不依赖面板写操作。  
3. **运维脚本** — 提供 `needle-server.sh` / `needle-agent.sh` 统一装升卸；节点数据用二进制 CLI `list-agents` / `delete-agent`。

---

## 架构

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

数据流只有 **Agent → Server**，没有反向控制通道。

---

## 安全要点

| 措施 | 说明 |
|------|------|
| 零信任上报 | Server 永不主动连 Agent |
| 面板只读 | 无 `DELETE /api/agents` 等远程删除 API |
| Token | `Authorization: Bearer`；`agent.yaml` / Server `.env` 权限 600 |
| 二进制校验 | Release 附带 `.sha256`，脚本下载后校验 |
| Agent 沙箱 | systemd 加固（NoNewPrivileges、ProtectSystem 等） |

---

## 功能一览

| 功能 | 说明 |
|------|------|
| 系统指标 | CPU / 内存 / 磁盘 / 网速 / 负载 / 运行时间 |
| 流量周期 | 按计费周期展示用量，到期可自动对齐清零逻辑 |
| TCPing | 多线路延迟（如 CMv4/CUv6），卡片可切换显示 |
| Region | 国家/地区标识，仪表盘展示节点分布 |

---

## 部署方式

脚本支持 **curl 或 wget**。无 curl 时可：`apt-get update && apt-get install -y curl`（或 wget）。

### Server · Docker（推荐）

#### 一键部署

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

自定义端口：`echo "NEEDLE_PORT=8080" >> .env && docker compose up -d`

#### 运维

```bash
cd ~/needle

# 升级
docker compose pull && docker compose up -d

# 日志
docker compose logs -f needle-server

# 列节点 / 删节点（exec 不走 ENTRYPOINT，服务名后须再写 needle-server）
docker compose exec needle-server \
  needle-server -db /data/needle.db list-agents
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

#### 目录

| 路径 | 说明 |
|------|------|
| `~/needle/docker-compose.yml` | 编排（路径按你创建的目录） |
| `~/needle/.env` | `NEEDLE_TOKEN`，可选 `NEEDLE_PORT` |
| `~/needle/data/needle.db` | SQLite 数据库 |
| 容器内 `/data` | 数据卷挂载点 |
| 镜像 | `ghcr.io/robinproxy/needle:latest` |

本地构建：

```bash
git clone https://github.com/Robinproxy/Needle.git && cd Needle
echo "NEEDLE_TOKEN=$(openssl rand -hex 16)" > .env
docker compose up -d --build
```

---

### Server · 二进制（systemd）

#### 一键部署脚本

```bash
# curl
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  -o /tmp/needle-server.sh
# 或 wget
wget -qO /tmp/needle-server.sh \
  https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh

sudo bash /tmp/needle-server.sh install
```

管道：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh | sudo bash
```

无参时：未安装 → `install`，已安装 → `upgrade`。

#### 运维

```bash
sudo bash /tmp/needle-server.sh              # 智能 install / upgrade
sudo bash /tmp/needle-server.sh upgrade      # 只换二进制，保留 .env 与 data/
sudo bash /tmp/needle-server.sh status
sudo bash /tmp/needle-server.sh uninstall    # 停服务 + 删二进制，默认保留 data/ 与 .env
sudo bash /tmp/needle-server.sh uninstall --purge   # 连 data/ 与 .env 一起删除

# 日志
journalctl -u needle-server -f

# 列节点 / 删节点（二进制 CLI，不需要 NEEDLE_TOKEN）
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent <hostname|id>
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y delete-agent <hostname|id>
```

> `delete-agent` **只删 Server 库里的数据**，不会停远端 Agent。若 Agent 仍在上报，节点会重新出现。

#### 目录

| 路径 | 说明 |
|------|------|
| `/opt/needle/bin/needle-server` | Server 二进制 |
| `/opt/needle/.env` | `NEEDLE_LISTEN`、`NEEDLE_TOKEN`（权限 600） |
| `/opt/needle/data/needle.db` | SQLite |
| `/etc/systemd/system/needle-server.service` | systemd unit |

也可从 [Releases](https://github.com/Robinproxy/Needle/releases) 解压后前台运行（无 systemd）：

```bash
TOKEN=$(openssl rand -hex 16)
tar xzf needle-linux-amd64.tar.gz needle-server
./needle-server -l :8008 -token "$TOKEN"
```

---

### Agent · 二进制（systemd）

在每台 VPS 上执行。

#### 一键部署脚本

```bash
# curl
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \
  -o /tmp/needle-agent.sh
# 或 wget
wget -qO /tmp/needle-agent.sh \
  https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh

sudo bash /tmp/needle-agent.sh install
```

管道：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh | sudo bash
```

无参时：未安装 → `install`，已安装 → `upgrade`。

#### 运维

```bash
sudo bash /tmp/needle-agent.sh              # 智能 install / upgrade
sudo bash /tmp/needle-agent.sh upgrade      # 零交互升级，保留 agent.yaml
sudo bash /tmp/needle-agent.sh status
sudo bash /tmp/needle-agent.sh uninstall    # 仅卸本机（默认，不碰 Server 库）
sudo bash /tmp/needle-agent.sh uninstall --unregister   # 先通知 Server 删节点，再卸本机

# 日志
journalctl -u needle-agent -f
```

#### 目录

| 路径 | 说明 |
|------|------|
| `/opt/needle-agent/bin/needle-agent` | Agent 二进制 |
| `/opt/needle-agent/agent.yaml` | 配置（权限 600） |
| `/etc/systemd/system/needle-agent.service` | systemd unit |

---

## 配置

### Server 环境变量

| 变量 | 说明 | 默认 |
|------|------|------|
| `NEEDLE_TOKEN` | Agent 上报认证 Token | **必填** |
| `NEEDLE_LISTEN` | 二进制监听地址（如 `:9000`） | `:8008` |
| `NEEDLE_PORT` | Docker 宿主机端口映射（仅数字） | `8008` |

### agent.yaml

```yaml
hostname: ""                                     # 可选，默认系统主机名
server: http://1.2.3.4:8008                      # 必填，Server 地址
token: your-token                                # 必填，和 Server 一致
region: SG                                       # ISO 国家码，如 CN/SG/US
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
| 面板上彻底去掉某台 VPS | 在该 VPS：`needle-agent.sh uninstall --unregister`；或先 stop Agent，再在 Server 上 `delete-agent` |
| Agent 已挂、只清面板残留 | Server：`delete-agent <hostname\|id>` |
| 删了节点又出现 | Agent 仍在上报 → 先停/卸 Agent，再 `delete-agent` |
| 只卸本机、Server 先留着 | `needle-agent.sh uninstall`（不加 `--unregister`） |

---

## 感谢

- **开发工具** — [opencode](https://opencode.ai) + [DeepSeek V4 Flash](https://deepseek.com/) 提供 AI 辅助编码
- **TCPing 节点** — [zstaticcdn](https://lf3-ips.zstaticcdn.com/) 提供全球探测节点
- **主题 UI 参考** — [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2) 的仪表盘设计灵感
