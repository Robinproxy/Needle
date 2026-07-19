# Needle

<p align="center">
  <img src="https://raw.githubusercontent.com/Robinproxy/Needle/main/internal/server/static/favicon.svg" width="72" height="72" alt="Needle">
</p>

<p align="center">
  轻量、单文件、面向个人服务器的流量与状态监控工具
</p>

<p align="center">
  <a href="README.en.md">English</a> ·
  <a href="https://github.com/Robinproxy/Needle/releases">Releases</a> ·
  <a href="https://github.com/Robinproxy/Needle/pkgs/container/needle">Container</a>
</p>

<p align="center">
  <a href="https://github.com/Robinproxy/Needle/actions/workflows/docker.yml"><img src="https://github.com/Robinproxy/Needle/actions/workflows/docker.yml/badge.svg?branch=main" alt="Docker build"></a>
</p>

Needle 由一个 Server 和多个 Agent 组成。Agent 只向外连接 Server，采集 CPU、内存、网络流量和 TCP Ping；Server 将数据保存在 SQLite 中，并提供只读 Web 面板。

## 功能

- CPU、内存、实时上下行速率与计费周期流量
- TCP Ping 多线路延迟监控，可按线路显示或隐藏
- `1d` 原始数据与 `7d` 降采样概览，兼顾细节和加载速度
- 点击 7 天图表下方的日期，可同步查看当天 CPU、内存、流量和 TCP Ping 原始数据
- 异常日期使用小红点提示，悬停可查看摘要；异常标记不会遮挡曲线
- 页面自动刷新，并区分加载中、暂无数据、数据过期和请求失败
- 每个 Agent 使用独立 Token，首次成功上报后绑定节点
- 单二进制部署，SQLite 存储，无前端构建依赖
- 支持 HTTPS、Cloudflare Tunnel、Docker Compose 和 systemd

## 架构

```text
Agent A ─┐
Agent B ─┼── HTTPS ──> Needle Server ──> SQLite
Agent C ─┘                   │
                             └── Web Dashboard
```

Agent 无需开放入站端口。生产环境建议让 Agent 通过 HTTPS 域名连接 Server，例如 Cloudflare Tunnel 或反向代理。

<details>
<summary><strong>快速部署</strong></summary>

<br>

### 1. 部署 Server（推荐 Docker Compose）

```bash
mkdir -p ~/needle/data
cd ~/needle
curl -fsSLO https://raw.githubusercontent.com/Robinproxy/Needle/main/docker-compose.yml
docker compose up -d
```

默认只监听宿主机 `127.0.0.1:8008`，数据库保存在 `~/needle/data/needle.db`。

查看运行状态：

```bash
docker compose ps
docker compose logs --tail=100 needle-server
curl -fsS http://127.0.0.1:8008/api/health
```

### 2. 配置 HTTPS

以 Cloudflare Tunnel 为例：

- cloudflared 运行在宿主机：源服务填写 `http://127.0.0.1:8008`
- cloudflared 与 Needle 在同一个 Docker 网络：可填写 `http://needle-server:8008`
- Agent 配置中填写公开 HTTPS 地址，例如 `https://needle.example.com`

Tunnel 到本机服务之间使用 HTTP 是正常的；Agent 到公开域名之间仍然是 HTTPS。

### 3. 安装 Agent

在每台被监控服务器上执行：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh
sudo bash /tmp/needle-agent.sh install
```

安装程序会生成独立 Token，并写入 `/opt/needle-agent/agent.yaml`。将其中的 `server` 改为 Server 的 HTTPS 地址，然后在 Server 上放行该 Token。

Docker Server：

```bash
cd ~/needle
docker compose exec needle-server needle-server -db /data/needle.db allow-token YOUR_TOKEN
```

二进制或 systemd Server：

```bash
sudo /opt/needle/bin/needle-server \
  -db /opt/needle/data/needle.db \
  allow-token YOUR_TOKEN
```

最后重启 Agent：

```bash
sudo systemctl restart needle-agent
sudo systemctl status needle-agent --no-pager
```

打开 `https://needle.example.com` 即可查看面板。

</details>

<details>
<summary><strong>历史数据说明</strong></summary>

<br>

| 视图 | 数据方式 | 适合用途 |
| --- | --- | --- |
| `1d` | 原始采样数据 | 排查最近 24 小时的具体变化 |
| `7d` | 15 分钟时间桶降采样 | 快速观察一周趋势，降低加载和绘制压力 |
| 日期视图 | 指定自然日的原始数据 | 从 7 天概览进入某一天详细排查 |

在 `7d` 视图中点击 TCP Ping 下方的日期，所有图表会同步切换到当天数据。日期上的红点表示当天存在异常提示；点击 `7d` 可返回一周概览。

</details>

<details>
<summary><strong>Agent 配置</strong></summary>

<br>

配置文件：`/opt/needle-agent/agent.yaml`

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

主要字段：

| 字段 | 说明 |
| --- | --- |
| `hostname` | 留空时使用系统主机名 |
| `server` | Server 地址，生产环境应使用 HTTPS |
| `token` | 每台 Agent 独立使用的认证 Token |
| `region` | 面板显示的地区代码 |
| `billing_period` | 流量计费周期，例如 `1m` 表示每月 1 日开始 |
| `expires_at` | 服务器到期日期，格式为 `YYYY-MM-DD` |
| `interval` | 上报间隔，单位为秒 |
| `tls_skip_verify` | 跳过 TLS 证书校验，仅用于临时排障 |
| `allow_plain_http` | 允许连接远程明文 HTTP Server，不建议启用 |
| `tcpping` | TCP Ping 目标列表 |

修改配置后检查并重启：

```bash
sudo nano /opt/needle-agent/agent.yaml
sudo systemctl restart needle-agent
sudo journalctl -u needle-agent -n 100 --no-pager
```

远程 HTTP 默认会被 Agent 拒绝；`http://127.0.0.1` 等回环地址不受影响。不要为绕过证书问题长期启用 `tls_skip_verify` 或 `allow_plain_http`。

</details>

## 日常运维

<details>
<summary><strong>Docker Server</strong></summary>

<br>

查看状态和实时日志：

```bash
cd ~/needle
docker compose ps
docker compose logs -f --tail=100 needle-server
```

升级镜像：

```bash
cd ~/needle
docker compose pull
docker compose up -d
docker image prune -f
```

重启或停止：

```bash
docker compose restart needle-server
docker compose stop needle-server
docker compose start needle-server
```

管理 Agent 和 Token：

```bash
# 查看已登记节点
docker compose exec needle-server needle-server -db /data/needle.db list-agents

# 查看允许接入的 Token
docker compose exec needle-server needle-server -db /data/needle.db list-tokens

# 新增 Token
docker compose exec needle-server needle-server -db /data/needle.db allow-token YOUR_TOKEN

# 撤销 Token，并删除与其绑定的节点数据
docker compose exec needle-server needle-server -db /data/needle.db revoke-token YOUR_TOKEN

# 按节点 ID 或主机名删除节点数据
docker compose exec needle-server needle-server -db /data/needle.db delete-agent HOSTNAME_OR_ID
```

`delete-agent` 只删除 Server 中的数据；如果 Agent 仍在运行并继续上报，节点会再次出现。永久移除节点时应先在 Agent 端执行 `uninstall --unregister`，或撤销其 Token。

#### 备份与恢复

SQLite 正在写入时不建议直接复制单个数据库文件。最稳妥的方式是短暂停止 Server，备份整个数据目录：

```bash
cd ~/needle
docker compose stop needle-server
cp -a data "data.backup.$(date +%Y%m%d-%H%M%S)"
docker compose start needle-server
```

恢复前先停止服务，并保留当前数据：

```bash
cd ~/needle
docker compose down
mv data "data.failed.$(date +%Y%m%d-%H%M%S)"
cp -a data.backup.YYYYMMDD-HHMMSS data
docker compose up -d
```

确认恢复正常后，再手动清理不需要的旧目录。

</details>

<details>
<summary><strong>systemd Server</strong></summary>

<br>

安装：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh install
```

升级、检查和日志：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh \
  | sudo bash -s -- upgrade
sudo systemctl status needle-server --no-pager
sudo journalctl -u needle-server -f
```

备份数据库：

```bash
sudo systemctl stop needle-server
sudo cp -a /opt/needle/data "/opt/needle/data.backup.$(date +%Y%m%d-%H%M%S)"
sudo systemctl start needle-server
```

卸载程序但保留数据和配置：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh uninstall
```

彻底删除程序、配置和数据：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-server.sh -o /tmp/needle-server.sh
sudo bash /tmp/needle-server.sh uninstall --purge
```

</details>

<details>
<summary><strong>Agent 二进制运维</strong></summary>

<br>

安装脚本部署的是 Agent 二进制，并由 systemd 管理。先下载运维脚本，后续可使用它查看状态、升级或卸载：

```bash
curl -fsSL https://raw.githubusercontent.com/Robinproxy/Needle/main/scripts/needle-agent.sh -o /tmp/needle-agent.sh
```

安装 Agent：

```bash
sudo bash /tmp/needle-agent.sh install
```

查看本机 Agent Token：

```bash
sudo sed -n 's/^token:[[:space:]]*//p' /opt/needle-agent/agent.yaml
```

复制上一步显示的 Token，然后在 Server 上放行。Docker Server：

```bash
cd ~/needle
docker compose exec needle-server needle-server -db /data/needle.db allow-token YOUR_TOKEN
```

二进制或 systemd Server：

```bash
sudo /opt/needle/bin/needle-server \
  -db /opt/needle/data/needle.db \
  allow-token YOUR_TOKEN
```

Agent 首次成功上报后，Server 会自动将该 Token 绑定到 Agent 主机名，无需额外注册命令。

查看安装信息、服务状态和最近日志：

```bash
sudo bash /tmp/needle-agent.sh status
sudo systemctl status needle-agent --no-pager
sudo journalctl -u needle-agent -n 100 --no-pager
```

持续查看实时日志：

```bash
sudo journalctl -u needle-agent -f
```

启动、停止或重启 Agent：

```bash
sudo systemctl start needle-agent
sudo systemctl stop needle-agent
sudo systemctl restart needle-agent
```

修改配置后重启，并通过日志确认连接是否成功：

```bash
sudo nano /opt/needle-agent/agent.yaml
sudo systemctl restart needle-agent
sudo journalctl -u needle-agent -n 50 --no-pager
```

升级二进制；现有 `/opt/needle-agent/agent.yaml` 不会被覆盖：

```bash
sudo bash /tmp/needle-agent.sh upgrade
sudo systemctl status needle-agent --no-pager
```

需要在前台排查时，先停止 systemd 服务，避免同一个 Agent 重复上报：

```bash
sudo systemctl stop needle-agent
sudo /opt/needle-agent/bin/needle-agent /opt/needle-agent/agent.yaml
# 按 Ctrl+C 退出后恢复服务
sudo systemctl start needle-agent
```

仅卸载本机二进制和 systemd 服务，Server 上的历史数据会保留：

```bash
sudo bash /tmp/needle-agent.sh uninstall
```

通知 Server 删除节点数据后再卸载：

```bash
sudo bash /tmp/needle-agent.sh uninstall --unregister
```

</details>

<details>
<summary><strong>常用路径</strong></summary>

<br>

| 内容 | 路径 |
| --- | --- |
| Server 二进制 | `/opt/needle/bin/needle-server` |
| Server 环境配置 | `/opt/needle/.env` |
| Server 数据库 | `/opt/needle/data/needle.db` |
| Agent 二进制 | `/opt/needle-agent/bin/needle-agent` |
| Agent 配置 | `/opt/needle-agent/agent.yaml` |
| systemd 服务 | `/etc/systemd/system/needle-server.service`、`needle-agent.service` |

</details>

<details>
<summary><strong>升级说明</strong></summary>

<br>

- Web 面板和 Server API 的新功能只需要升级 Server。
- Agent 采集、安全策略或本地配置能力发生变化时，才需要升级 Agent。
- Server 升级脚本保留数据库和 `.env`；Agent 升级脚本保留 `agent.yaml`。
- 升级前建议先备份 Server 数据目录，并阅读对应版本的 Release Notes。

</details>

<details>
<summary><strong>安全建议</strong></summary>

<br>

- 对外访问必须使用 HTTPS，Server 端口只绑定回环地址或内网地址。
- 每台 Agent 使用独立 Token，不要在多台机器之间复用。
- Token 泄露后立即执行 `revoke-token`，再为节点生成新 Token。
- 不要将数据库、`.env`、`agent.yaml` 或 Token 提交到代码仓库。
- Cloudflare Tunnel 或反向代理不能替代 Server 自身的 Token 校验。

</details>

<details>
<summary><strong>从源码构建</strong></summary>

<br>

需要 Go 1.24 或更高版本：

```bash
git clone https://github.com/Robinproxy/Needle.git
cd Needle
go test ./...
go build -o needle-server ./cmd/server
go build -o needle-agent ./cmd/agent
```

Server 参数：

```text
-l       监听地址，也可使用 NEEDLE_LISTEN
-db      SQLite 数据库路径
-cert    TLS 证书路径
-key     TLS 私钥路径
-y       跳过删除或撤销操作的确认
```

</details>

## 感谢

- 感谢 [akiasprin](https://github.com/akiasprin) 开源的 [NodeGet-StatusShowR2](https://github.com/akiasprin/NodeGet-StatusShowR2)，为 Needle 的仪表盘设计提供了参考与灵感。
- 感谢 [zstaticcdn](https://lf3-ips.zstaticcdn.com/) 提供 TCP Ping 探测节点。
- 感谢 [OpenCode](https://opencode.ai/)、[DeepSeek](https://deepseek.com/)、[Grok](https://x.ai/)、[GLM](https://zhipuai.cn/) 和 [OpenAI Codex](https://openai.com/codex/) 在项目开发与代码审查过程中提供辅助。
- 感谢所有开源项目作者、贡献者和使用 Needle 并提供反馈的朋友。

## License

[MIT](LICENSE)
