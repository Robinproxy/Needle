# Needle

轻量级、纯出站上报的服务器监控探针

## 设计理念

- **安全最小化** — Agent 纯上报，Server 永不主动连接 Agent，无 WebSSH，无命令执行
- **零升级麻烦** — 无需升级 Agent，数据格式向前兼容
- **极简部署** — 单二进制（Server ~12MB，Agent ~9MB），SQLite 零配置，无外部依赖
- **隐私优先** — 自定义 Hostname/Region，Server 仅存你配置的数据

## 架构

```
┌──────────────┐     POST /api/report     ┌──────────────┐
│  Needle Agent │ ───────────────────────→ │ Needle Server│
│  (VPS/节点)   │      Bearer Token       │  (Dashboard) │
│              │ ←─ HTTP 200 {"status":"ok"}│              │
└──────────────┘                           └──────┬───────┘
                                                   │
                                           ┌───────┴───────┐
                                           │   SQLite DB    │
                                           │  (./data/)     │
                                           └───────────────┘
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

访问 `http://你的VPSIP:8008` 查看仪表盘。

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

```bash
# Server
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-server.sh | sudo bash

# Agent（在每台 VPS 上）
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-agent.sh | sudo bash
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
billing_period: "1m"                             # 1m/3m/6m/12m，可选，计费周期
expires_at: "2026-08-15"                         # YYYY-MM-DD，可选，续费日期
interval: 30                                     # 上报间隔（秒）
insecure: false                                  # 关闭 TLS 验证（自签名证书用）
tcpping:
  - name: "CMv4"
    target: "sh-cm-v4.ip.zstaticcdn.com:80"
    interval: 60
  - name: "CMv6"
    target: "sh-cm-v6.ip.zstaticcdn.com:80"
    interval: 60
  - name: "CUv4"
    target: "sh-cu-v4.ip.zstaticcdn.com:80"
    interval: 60
  - name: "CUv6"
    target: "sh-cu-v6.ip.zstaticcdn.com:80"
    interval: 60
  - name: "CTv4"
    target: "sh-ct-v4.ip.zstaticcdn.com:80"
    interval: 60
  - name: "CTv6"
    target: "sh-ct-v6.ip.zstaticcdn.com:80"
    interval: 60
```

### Server 环境变量

| 变量 | 说明 | 默认 |
|---|---|---|
| `NEEDLE_TOKEN` | 认证 Token，Agent 连接必须携带 | **必填** |
| `NEEDLE_LISTEN` | 监听地址（二进制运行用，如 `:9000`） | `:8008` |
| `NEEDLE_PORT` | Docker 宿主机端口映射（仅数字） | `8008` |

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

## Git 工作流

项目基于 GitHub，日常维护操作：

```bash
# 克隆仓库
git clone https://github.com/Robinproxy/Needle.git
cd Needle

# 查看当前状态
git status
git log --oneline -10

# 拉取最新代码
git pull

# 查看本地和远程的差异
git diff
git diff --stat

# 提交代码
git add .
git commit -m "description of changes"
git push

# 打标签发布新版本（触发 GitHub Actions 自动构建）
git tag v0.3.5
git push origin v0.3.5

# 查看已有标签
git tag -l
git describe --tags --abbrev=0

# 回退本地提交（慎用）
git reset --soft HEAD~1    # 保留修改
git reset --hard HEAD~1    # 丢弃修改

# 删除标签（本地 + 远程）
git tag -d v0.3.5
git push origin :refs/tags/v0.3.5
```

### Release 流程

1. 改完代码 → `git commit` → `git push`
2. 打 tag → `git tag v0.3.x` → `git push origin v0.3.x`
3. GitHub Actions 自动构建：
   - **Release** → 生成 `needle-linux-amd64.tar.gz` + `needle-linux-arm64.tar.gz` + 安装脚本 + checksums
   - **Docker** → 构建 `ghcr.io/robinproxy/needle:latest` + `:v0.3.x`

### Docker 更新

```bash
# 在 VPS 上更新容器
docker compose pull
docker compose up -d

# 如果修改了 tag，先更新 docker-compose.yml 中的镜像 tag
# 然后清理旧镜像
docker image prune
```

---

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/report` | Agent 上报数据 |
| `POST` | `/api/unregister` | Agent 注销 |
| `GET` | `/api/info` | 服务器信息和版本 |
| `GET` | `/api/agents` | 所有节点概览 |
| `GET` | `/api/agents/:id/metrics?range=168h` | 节点指标历史 |
| `GET` | `/api/agents/:id/tcpping?range=168h` | TCPing 历史 |
| `GET` | `/api/agents/:id/traffic` | 流量统计 |
| `DELETE` | `/api/agents/:id` | 删除离线节点 |
