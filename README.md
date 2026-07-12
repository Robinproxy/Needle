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

需要 root（脚本安装时），且本机可访问 GitHub Releases / ghcr。脚本支持 **curl 或 wget**。

若机器没有 curl（Debian 最小安装常见）：

```bash
apt-get update && apt-get install -y curl
# 或
apt-get update && apt-get install -y wget
```

---

### Server：Docker（推荐）

#### 安装

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

#### 升级

```bash
cd ~/needle
docker compose pull
docker compose up -d
```

#### 运维

```bash
# 日志
docker compose logs -f needle-server

# 列节点 / 删节点（exec 不会走 ENTRYPOINT，须再写 needle-server）
docker compose exec needle-server \
  needle-server -db /data/needle.db list-agents
docker compose exec needle-server \
  needle-server -db /data/needle.db delete-agent <hostname|id>
docker compose exec needle-server \
  needle-server -db /data/needle.db -y delete-agent <hostname|id>

# 备份数据库
cp -a data/needle.db data/needle.db.bak
```

#### 卸载

```bash
# 停容器，保留 ./data
docker compose down

# 停容器并删除数据
docker compose down -v && rm -rf data
```

#### 本地构建镜像

```bash
git clone https://github.com/Robinproxy/Needle.git
cd Needle
echo "NEEDLE_TOKEN=$(openssl rand -hex 16)" > .env
docker compose up -d --build
```

---

### Server：二进制（systemd）

统一入口 `needle-server.sh`：

```bash
# curl
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  -o /tmp/needle-server.sh
# 或 wget
wget -qO /tmp/needle-server.sh \
  https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh

sudo bash /tmp/needle-server.sh              # 未安装 → install；已安装 → upgrade
sudo bash /tmp/needle-server.sh install      # 交互安装
sudo bash /tmp/needle-server.sh upgrade      # 升级二进制，保留 .env 与 data/
sudo bash /tmp/needle-server.sh status
sudo bash /tmp/needle-server.sh uninstall    # 停服务+删二进制，默认保留 data/ 与 .env
sudo bash /tmp/needle-server.sh uninstall --purge   # 连 data/ 与 .env 一起删除
```

管道（需 curl）：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh | sudo bash
```

安装路径：二进制 `/opt/needle/bin/needle-server`，配置 `/opt/needle/.env`，数据库 `/opt/needle/data/needle.db`。

列节点 / 删节点（**不用** shell 子命令，直接调二进制）：

```bash
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db list-agents
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db delete-agent <hostname|id>
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db -y delete-agent <hostname|id>
```

也可从 Releases 解压后前台运行（无 systemd）：

```bash
TOKEN=$(openssl rand -hex 16)
tar xzf needle-linux-amd64.tar.gz needle-server
./needle-server -l :8008 -token "$TOKEN"
```

---

### Agent：二进制（systemd）

统一入口 `needle-agent.sh`（每台 VPS）：

```bash
# curl
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh \
  -o /tmp/needle-agent.sh
# 或 wget
wget -qO /tmp/needle-agent.sh \
  https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh

sudo bash /tmp/needle-agent.sh              # 未安装 → install；已安装 → upgrade
sudo bash /tmp/needle-agent.sh install      # 交互安装
sudo bash /tmp/needle-agent.sh upgrade      # 零交互升级（保留 agent.yaml）
sudo bash /tmp/needle-agent.sh status
sudo bash /tmp/needle-agent.sh uninstall    # 仅卸本机（默认）
sudo bash /tmp/needle-agent.sh uninstall --unregister   # 先通知 Server 删节点，再卸本机
```

管道：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh | sudo bash
```

> **说明：** Agent `uninstall` 默认只停服务并删除 `/opt/needle-agent`，**不会**清 Server 数据库。  
> 需要同时从面板去掉节点时，使用 `uninstall --unregister`；若 Agent 已无法连 Server，再到 Server 上执行 `delete-agent`。

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

**Docker**（`docker compose exec` **不会**走镜像 ENTRYPOINT，服务名后须再写一次二进制名；数据卷为 `/data`）：

```bash
docker compose exec needle-server \
  needle-server -db /data/needle.db list-agents
docker compose exec needle-server \
  needle-server -db /data/needle.db delete-agent dedi-us
docker compose exec needle-server \
  needle-server -db /data/needle.db -y delete-agent 46
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

Agent 卸载时若要同时通知 Server 删节点：

```bash
sudo bash /tmp/needle-agent.sh uninstall --unregister
```

或调用二进制（需 Token，配置文件路径按实际填写）：

```bash
/opt/needle-agent/bin/needle-agent -unregister /opt/needle-agent/agent.yaml
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

