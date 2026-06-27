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

## 快速安装

### Docker 部署（推荐）

#### 前提条件

- 安装了 Docker Engine 和 Docker Compose 的 Linux 服务器
- 一个用于认证的 Token（任意字符串，Agent 连接时需要用到）

#### 第 1 步：创建目录和配置文件

```bash
mkdir -p ~/needle && cd ~/needle
```

创建 `.env` 文件，填入你的 Token：

```bash
echo "NEEDLE_TOKEN=你的token" > .env
```

创建 `docker-compose.yml`：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/docker-compose.yml -o docker-compose.yml
```

或者手动创建：

```yaml
services:
  needle-server:
    image: ghcr.io/robinproxy/needle:latest
    ports:
      - "${NEEDLE_PORT:-8008}:8008"
    environment:
      NEEDLE_TOKEN: "${NEEDLE_TOKEN:?error: set NEEDLE_TOKEN in .env or environment}"
    volumes:
      - ./data:/data
    restart: unless-stopped
```

#### 第 2 步：启动容器

```bash
docker compose up -d
```

> 首次启动会自动从 `ghcr.io/robinproxy/needle` 拉取镜像。如果镜像尚未构建（新版本刚发布），也可以使用下面的**本地构建**方式。

#### 第 3 步：验证运行状态

```bash
docker compose ps
docker compose logs -f
```

看到日志输出 `Needle Server listening on :8008` 即表示启动成功。

按 `Ctrl+C` 退出日志查看。

#### 环境变量

查看当前配置的环境变量：

```bash
# 查看 .env 文件
cat .env

# 查看容器内生效的环境变量
docker compose exec needle-server env | grep NEEDLE
```

#### 第 4 步：访问仪表盘

打开浏览器访问 `http://你的VPSIP:8008`，即可看到仪表盘。

> 如果使用云服务器，需要在防火墙/安全组中放行 8008 端口。

#### 指定端口

默认使用宿主机的 8008 端口。想用其他端口（如 8080）：

```bash
echo "NEEDLE_PORT=8080" >> .env
docker compose up -d
```

这会映射宿主机的 8080 到容器的 8008。仪表盘访问 `http://你的VPSIP:8080`。

---

### Docker 本地构建

如果不想等待 GHCR 镜像拉取，也可以直接从源码构建：

```bash
git clone https://github.com/Robinproxy/Needle.git ~/needle
cd ~/needle
echo "NEEDLE_TOKEN=你的token" > .env
docker compose up -d --build
```

---

### 二进制安装（无 Docker）

适合没有 Docker 的环境。从 [Releases](https://github.com/Robinproxy/Needle/releases) 下载对应架构的 `.tar.gz`：

```bash
# Server
tar xzf needle-linux-amd64.tar.gz needle-server
./needle-server -l :8008 -token your-token

# 带日志在后台运行
nohup ./needle-server -l :8008 -token your-token > needle.log 2>&1 &
```

---

### 一键脚本安装（systemd）

适合长期运行的生产环境。脚本会自动安装二进制、创建 systemd 服务。

```bash
# Server
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-server.sh | sudo bash
```

交互式配置：
- 监听地址（默认 `:8008`）
- Token（留空自动生成 32 位随机）

安装后自动作为 systemd 服务运行。

**Server 安装路径**：

| 内容 | 路径 |
|---|---|
| 二进制 | `/opt/needle/bin/needle-server` |
| 环境变量 | `/opt/needle/.env` |
| 数据库 | `/opt/needle/data/needle.db` |
| 日志 | `journalctl -u needle-server -f` |

---

## 安装 Agent

### 一键脚本安装

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-agent.sh | sudo bash
```

交互式配置：
- Hostname（默认系统主机名，仪表盘上的节点名）
- Region（ISO 国家码，如 CN/SG/US，仪表盘显示国旗）
- Server URL（Server 地址，如 `http://1.2.3.4:8008`）
- Token（和 Server 设置的 Token 一致）
- TCPing 目标（默认上海节点，可直接回车确认）

**Agent 安装路径**：

| 内容 | 路径 |
|---|---|
| 二进制 | `/opt/needle-agent/bin/needle-agent` |
| 配置文件 | `/opt/needle-agent/agent.yaml` |
| 日志 | `journalctl -u needle-agent -f` |

### 手动安装（二进制）

从 [Releases](https://github.com/Robinproxy/Needle/releases) 下载：

```bash
tar xzf needle-linux-amd64.tar.gz needle-agent agent.yaml.example
cp agent.yaml.example agent.yaml
# 编辑 agent.yaml，填入 server 地址和 token
./needle-agent agent.yaml
```

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
rm -rf ~/needle

# Agent
sudo systemctl stop needle-agent
sudo systemctl disable needle-agent
sudo rm /etc/systemd/system/needle-agent.service
sudo rm -rf /opt/needle-agent
```

---

## 配置文件

### agent.yaml

```yaml
hostname: ""                                     # 可选，默认系统主机名
server: http://1.2.3.4:8008                      # 必填，Server 地址
token: your-token                                # 必填，和 Server 一致
region: SG                                       # ISO 国家码，如 CN/SG/US
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

**文件路径**：
- 一键安装：`/opt/needle-agent/agent.yaml`
- 手动安装：当前目录下的 `agent.yaml`

### Server 环境变量

| 变量 | 说明 | 默认 |
|---|---|---|
| `NEEDLE_TOKEN` | 认证 Token，Agent 连接必须携带 | **必填** |
| `NEEDLE_LISTEN` | 监听地址（二进制运行用，如 `:9000`） | `:8008` |
| `NEEDLE_PORT` | Docker 宿主机端口映射（仅数字） | `8008` |

---

## Cloudflare Tunnel

Server 本身无 TLS 支持，推荐配合 Cloudflare Tunnel 提供 HTTPS：

```bash
cloudflared tunnel create needle
```

`~/.cloudflared/config.yml`:

```yaml
tunnel: needle
credentials-file: /root/.cloudflared/needle.json
ingress:
  - hostname: needle.example.com
    service: http://localhost:8008
  - service: http_status:404
```

```bash
cloudflared tunnel route dns needle needle.example.com
cloudflared tunnel run needle
```

Agent 配置中 `server` 改为 `https://needle.example.com`。

---

## 构建

需要 Go 1.26+ 和 gcc（go-sqlite3 需要 CGO）。

```bash
git clone https://github.com/Robinproxy/Needle.git
cd Needle

# 本地编译（macOS/Linux）
make build

# 交叉编译 Linux amd64/arm64 二进制
make release

# 编译后二进制在 bin/ 或 release/ 目录
```

### 发布新版本

```bash
git tag v0.2.0
git push origin v0.2.0
```

推送 tag 后 GitHub Actions 自动构建：
- **release.yml** — 编译 Linux 二进制并上传到 Releases
- **docker.yml** — 构建 Docker 镜像并推送到 `ghcr.io/robinproxy/needle`

Docker 用户后续可以直接 `docker compose pull` 更新镜像。
