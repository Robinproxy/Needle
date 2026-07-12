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

| 理念 | 说明 |
|------|------|
| 🔒 **零信任上报** | 基于零信任架构，Agent 纯上报，Server 永不主动连接，无 WebSSH，无命令执行 |
| 🔄 **灵活升级** | 不强制 Agent 升级，新功能通过可选字段扩展，旧版本兼容运行 |
| 📦 **极简部署** | Server + Agent 两个独立二进制，SQLite 零配置，无外部依赖 |

## 安全设计

| 类别 | 措施 | 说明 |
|------|------|------|
| 🔐 **完整性** | 二进制 SHA256 校验 | 每次 Release 自动生成 `.sha256`，安装/升级脚本下载后即时校验，防止供应链篡改 |
| 🔑 **Token 保护** | Header-only 传输 | Token 仅通过 `Authorization: Bearer` 头传输，不出现在 JSON body 中，减少日志/审计暴露面 |
| 🔑 **Token 保护** | 常量时间比较 | 服务端使用 `crypto/subtle.ConstantTimeCompare` 进行 token 比对，防止时序攻击 |
| 🔑 **Token 保护** | 不暴露在进程列表 | 安装/升级脚本的 curl 使用 `--data-binary @-` 从 stdin 读取 token，不出现在 `ps` 中 |
| 🔑 **Token 保护** | 配置文件权限 | `agent.yaml` 权限设置为 `600`，仅 root 可读 |
| 🛡️ **Agent 沙箱** | systemd 安全加固 | 进程运行时启用 `NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome=true`、`PrivateTmp=true`、`RestrictNamespaces=true`、`LockPersonality=true`、`RestrictRealtime=true`、`RestrictSUIDSGID=true`、`RemoveIPC=true`、清空 Capability |
| 📡 **传输安全** | HTTP 明文警告 | Agent 使用 HTTP 连接时自动打印警告，提醒生产环境应使用 HTTPS |
| 🛡 **面板只读** | 无远程删除 API | 不提供 `DELETE /api/agents`；清理节点仅在 Server 本机 CLI 执行 |
| 🔄 **向后兼容** | 旧版 Agent 无需升级 | 旧 Agent 同时携带 Header + Body token，新 Server 只读 Header，完全兼容 |

## 特色功能

| 功能 | 说明 |
|------|------|
| ⏱ **Traffic 到期自动清零** | 按计费周期自动重置流量统计，展示各周期用量 |
| 🎯 **TCPing 目标切换** | 点击卡片上的 CMv4/CUv6 标签循环切换显示线路 |
| 🛠 **本机 CLI 清理节点** | 面板只展示、不提供删除；在 Server 主机用 CLI 删除节点 |
| 🏁 **国旗自定义** | 自定义 Region 标识，仪表盘直观展示全球节点分布 |

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

---

## 快速开始

### Docker 部署（推荐）

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

自定义端口：

```bash
echo "NEEDLE_PORT=8080" >> .env
docker compose up -d
```

### 二进制部署

```bash
# 从 Releases 下载对应架构的 tar.gz
TOKEN=$(openssl rand -hex 16)
tar xzf needle-linux-amd64.tar.gz needle-server
./needle-server -l :8008 -token "$TOKEN"
# 或后台运行
nohup ./needle-server -l :8008 -token "$TOKEN" > needle.log 2>&1 &
```

### 一键脚本安装（systemd）

需要 root，且本机可访问 GitHub Releases。脚本内部支持 **curl 或 wget**。

若机器没有 curl（Debian 最小安装常见），先装一个下载工具：

```bash
# Debian / Ubuntu
apt-get update && apt-get install -y curl
# 或
apt-get update && apt-get install -y wget
```

推荐**先下载再执行**（交互配置更稳）：

Server：

```bash
# curl
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-server.sh \
  -o /tmp/needle-install-server.sh
# 或 wget
wget -qO /tmp/needle-install-server.sh \
  https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-server.sh

sudo bash /tmp/needle-install-server.sh
```

Agent（在每台 VPS 上运行）：

```bash
# curl
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-agent.sh \
  -o /tmp/needle-install-agent.sh
# 或 wget
wget -qO /tmp/needle-install-agent.sh \
  https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-agent.sh

sudo bash /tmp/needle-install-agent.sh
```

也可用管道（需本机已有 curl 或先 `apt-get install -y curl`）：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-server.sh | sudo bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-agent.sh | sudo bash
```

### Agent 一键升级

零交互升级：自动读取 `/opt/needle-agent/agent.yaml`，下载最新版、校验 SHA256、替换二进制、更新 systemd 安全配置并重启。

```bash
# curl
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/upgrade-agent.sh \
  -o /tmp/needle-upgrade-agent.sh
# 或 wget
wget -qO /tmp/needle-upgrade-agent.sh \
  https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/upgrade-agent.sh

sudo bash /tmp/needle-upgrade-agent.sh
```

### Docker 本地构建

```bash
git clone https://github.com/Robinproxy/Needle.git
cd Needle
echo "NEEDLE_TOKEN=$(openssl rand -hex 16)" > .env
docker compose up -d --build
```

---

## 配置

### agent.yaml

```yaml
hostname: ""                                     # 可选，默认系统主机名
server: http://1.2.3.4:8008                      # 必填，Server 地址
token: your-token                                # 必填，和 Server 一致
region: SG                                       # ISO 国家码，如 CN/SG/US
billing_period: "1m"                             # 1月/3月/6月/12月，可选，计费周期
expires_at: "2026-08-15"                         # YYYY-MM-DD，可选，续费日期
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

### Server 环境变量

| 变量 | 说明 | 默认 |
|---|---|---|
| `NEEDLE_TOKEN` | 认证 Token，Agent 连接必须携带 | **必填** |
| `NEEDLE_LISTEN` | 监听地址（二进制运行用，如 `:9000`） | `:8008` |
| `NEEDLE_PORT` | Docker 宿主机端口映射（仅数字） | `8008` |

---

## 运维：Server CLI（list / delete）

面板为**纯展示**：无删除按钮，也**没有**远程删除接口（无 `DELETE /api/agents`）。  
清理节点须在 **Server 本机** 操作本地 SQLite。CLI 模式**不启动 HTTP**，也**不需要** `NEEDLE_TOKEN`。

### 命令

| 命令 | 说明 |
|------|------|
| `list-agents` | 列出节点：`id` / `hostname` / `region` / `last_seen` |
| `delete-agent <id\|hostname>` | 按数字 id 或 hostname 删除节点及其全部历史数据 |

### 全局参数

| 参数 | 说明 | 默认 |
|------|------|------|
| `-db <path>` | SQLite 数据库路径 | `./data/needle.db` |
| `-y` | `delete-agent` 时跳过交互确认 | 关闭 |

### 删除范围

执行 `delete-agent` 会删除：

- `agents` 表中该节点
- 该节点全部 `metrics`
- 该节点全部 `tcpping_results`

**不会**停止远端 Agent 进程。若 Agent 仍在上报，节点会重新出现在面板上；需先停 Agent 或改配置后再删。

### 用法示例

**systemd 安装：**

```bash
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent dedi-us
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y delete-agent 46
```

**二进制（当前目录 data）：**

```bash
./needle-server -db ./data/needle.db list-agents
./needle-server -db ./data/needle.db delete-agent <hostname|id>
./needle-server -db ./data/needle.db -y delete-agent <hostname|id>
```

**Docker**（镜像 `ENTRYPOINT` 为 `needle-server`，参数直接跟在服务名后；数据卷为 `/data`）：

```bash
docker compose exec needle-server -db /data/needle.db list-agents
docker compose exec needle-server -db /data/needle.db delete-agent dedi-us
docker compose exec needle-server -db /data/needle.db -y delete-agent 46
```

### 输出示例

```text
ID     HOSTNAME                 REGION   LAST_SEEN
1      Lam-JP                   JP       2026-07-12T07:00:00Z
46     dedi-us                  US       2026-07-12T07:01:00Z
```

删除时默认交互确认：

```text
Delete agent "dedi-us" (id=46) and all its metrics/tcpping data? [y/N]
deleted agent "dedi-us" (id=46)
```

### Agent 侧注销（可选）

Agent 重装或改 hostname 时，可在 **Agent 机器**上注销（需 Token，与 Server CLI 互补）：

```bash
needle-agent -unregister /opt/needle-agent/agent.yaml
```

---

## 安装路径

### Server（systemd）

| 内容 | 路径 |
|---|---|
| 二进制 | `/opt/needle/bin/needle-server` |
| 环境变量 | `/opt/needle/.env` |
| 数据库 | `/opt/needle/data/needle.db` |
| 日志 | `journalctl -u needle-server -f` |

### Docker

| 内容 | 路径 |
|---|---|
| 数据目录 | `./data/` |
| 数据库 | `./data/needle.db` |

### Agent（systemd）

| 内容 | 路径 |
|---|---|
| 二进制 | `/opt/needle-agent/bin/needle-agent` |
| 配置文件 | `/opt/needle-agent/agent.yaml` |
| 日志 | `journalctl -u needle-agent -f` |

---

## 卸载

```bash
# Server（二进制 + systemd）
sudo systemctl stop needle-server
sudo systemctl disable needle-server
sudo rm /etc/systemd/system/needle-server.service
sudo rm -rf /opt/needle

# Server（Docker）
docker compose down -v
rm -rf ./data

# Agent
sudo systemctl stop needle-agent
sudo systemctl disable needle-agent
sudo rm /etc/systemd/system/needle-agent.service
sudo rm -rf /opt/needle-agent
```

---

## 感谢

- **开发工具** — [opencode](https://opencode.ai) + [DeepSeek V4 Flash](https://deepseek.com/) 提供 AI 辅助编码
- **TCPing 节点** — [zstaticcdn](https://lf3-ips.zstaticcdn.com/) 提供全球探测节点
- **主题 UI 参考** — [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2) 的仪表盘设计灵感

