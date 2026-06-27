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

## 快速安装

### Server — 一键脚本

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-server.sh | sudo bash
```

交互式配置：
- 监听地址（默认 `:8008`）
- Token（留空自动生成 32 位随机）

安装后自动作为 systemd 服务运行，仪表盘地址 `http://<ip>:8008`。

**安装路径**：
| 内容 | 路径 |
|---|---|
| 二进制 | `/opt/needle/bin/needle-server` |
| 环境变量 | `/opt/needle/.env` |
| 数据库 | `/opt/needle/data/needle.db` |
| 日志 | `journalctl -u needle-server -f` |

### Server — Docker

先克隆仓库，然后构建启动：

```bash
git clone https://github.com/Robinproxy/Needle.git
cd Needle

# 创建 .env 文件
echo "NEEDLE_TOKEN=your-token" > .env

# 构建并启动
docker compose up -d
```

### Agent — 一键脚本

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/install-agent.sh | sudo bash
```

交互式配置：
- Hostname（默认系统主机名）
- Region（ISO 国家码，如 CN/SG/US）
- Server URL
- Token
- TCPing 目标（6 个上海节点默认，可自定义修改）

**安装路径**：
| 内容 | 路径 |
|---|---|
| 二进制 | `/opt/needle-agent/bin/needle-agent` |
| 配置文件 | `/opt/needle-agent/agent.yaml` |
| 日志 | `journalctl -u needle-agent -f` |

## 手动安装

从 [Releases](https://github.com/Robinproxy/Needle/releases) 下载对应架构的 `.tar.gz`：

```bash
# Server
tar xzf needle-linux-amd64.tar.gz needle-server
./needle-server -l :8008 -token your-token

# Agent
tar xzf needle-linux-amd64.tar.gz needle-agent agent.yaml.example
cp agent.yaml.example agent.yaml
# 编辑 agent.yaml
./needle-agent agent.yaml
```

## 卸载

```bash
# Server
sudo systemctl stop needle-server
sudo systemctl disable needle-server
sudo rm /etc/systemd/system/needle-server.service
sudo rm -rf /opt/needle

# Agent
sudo systemctl stop needle-agent
sudo systemctl disable needle-agent
sudo rm /etc/systemd/system/needle-agent.service
sudo rm -rf /opt/needle-agent
```

## 配置文件

### agent.yaml

```yaml
hostname: ""                                     # 可选，默认系统主机名
server: https://needle.example.com               # 必填
token: your-token                                # 必填
region: SG                                       # ISO 国家码
interval: 30                                     # 上报间隔（秒）
insecure: false                                  # 关闭 TLS 验证
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
| `NEEDLE_TOKEN` | 认证 Token | **必填** |
| `NEEDLE_LISTEN` | 监听地址 | `:8008` |
| `NEEDLE_PORT` | Docker 主机端口映射 | `8008` |

## Cloudflare Tunnel

Server 本身无 TLS，推荐配合 Cloudflare Tunnel：

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

## 构建

需要 Go 1.26+ 和 gcc（go-sqlite3 需要 CGO）。

```bash
git clone https://github.com/Robinproxy/Needle.git
cd Needle

# 本地编译
make build

# 交叉编译（Linux）
make release
```

### Release 打包

```bash
git tag v0.1.1
git push origin v0.1.1
```

推送 tag 后 GitHub Actions 自动构建并发布到 Releases。
