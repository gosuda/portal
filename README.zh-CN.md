# PORTAL - Public Open Relay To Access Localhost

[English](./README.md) | [简体中文](./README.zh-CN.md)

<p align="center"><img width="800" alt="Portal Demo" src="./portal.gif" /></p>

<p align="center">将本地应用暴露到公网，无需端口转发、NAT 配置或 DNS 设置。<br />Portal 是一个无需信任的中继网络，中继无法访问你的流量。你可以连接任意中继，也可以自行部署自己的中继。</p><br />

## 功能特性

- **为 localhost 提供公网 HTTPS**：通过 TCP 透传实现 NAT 友好的发布方式（无需端口转发）
- **端到端 TLS**：TLS 在你这一侧终止，并内置 MITM 检测，因此中继无法访问明文
- **一条命令即可启动**：以最少配置启动中继和隧道
- **自托管中继**：既可以连接公共中继，也可以运行你自己的中继
- **中继发现与中继池**：将发现到的中继作为中继池使用，支持多中继访问与故障切换
- **无需登录、无需 API Key**：使用 SIWE 验证所有权，并支持基于 ENS 的身份
- **原生 TCP 与 UDP 传输**：原生 TCP 反向会话，并可选支持 UDP（不依赖 SSH 或 WebSocket）

## 对比

| 功能 | Portal | ngrok | Cloudflare Tunnel | frp |
|------|--------|-------|-------------------|-----|
| 端到端加密 | **是** — TLS 在你这一侧终止 | 可选 — 支持 TLS 透传，但默认在边缘终止 | 否 — Cloudflare 始终在边缘解密 | 仅隧道 TLS — 客户端↔服务器加密，非端到端 |
| 需要账号 | **否** — 基于 SIWE 的所有权验证 | 是 | 是 | 否 |
| 可自托管中继 | **是** | 仅企业版 | 否 | 是 |
| MITM 检测 | **是** — 内置 TLS exporter 检查 | 否 | 否 | 否 |
| 多中继故障切换 | **是** | 由 ngrok 管理 | 是 — 内置多数据中心 | 否 |
| 自定义域名 | **是** | 仅付费方案 | 是 | 是 |
| 开源 | **是** — MIT | 否 | 仅客户端（cloudflared，Apache 2.0） | 是 — Apache 2.0 |
| 传输协议 | 原生 TCP / UDP | HTTP/S, TCP, TLS | HTTP/S, TCP, UDP（私有网络） | HTTP/S, TCP, UDP |

## 快速开始

### 公开你的本地应用：

```bash
curl -fsSL https://github.com/gosuda/portal/releases/latest/download/install.sh | bash
portal expose 3000
```

```powershell
$ProgressPreference = 'SilentlyContinue'
irm https://github.com/gosuda/portal/releases/latest/download/install.ps1 | iex
portal expose 3000
```

然后你就可以通过一个公网 HTTPS URL 访问你的应用。
安装细节请参见 [cmd/portal-tunnel/README.md](cmd/portal-tunnel/README.md)。

### 运行你自己的中继

```bash
git clone https://github.com/gosuda/portal
cd portal && cp .env.example .env
docker compose up
```

部署到公网域名时，请参见 [docs/deployment.md](docs/deployment.md)。

### 运行原生应用（高级）

更多示例请参见 [portal-toys](https://github.com/gosuda/portal-toys)。

## 架构

请参见 [docs/architecture.md](docs/architecture.md)。
架构决策请参见 [docs/adr/README.md](docs/adr/README.md)。

## 示例

| 示例 | 说明 |
|---------|-------------|
| [nginx reverse proxy](docs/examples/nginx-proxy/) | 在 nginx 后部署 Portal，并使用 L4 SNI 路由和 TLS 终止 |
| [nginx + multi-service](docs/examples/nginx-proxy-multi-service/) | 在同一个 nginx 实例后，将 Portal 与其他 Web 服务一起运行 |

## 公共中继注册表

Portal 官方公共中继注册表为：

`https://raw.githubusercontent.com/gosuda/portal/main/registry.json`

Portal 隧道客户端可以默认包含这个注册表，Relay UI 也会从同一路径读取官方中继列表。

如果你正在运营公共 Portal 中继，请提交一个 Pull Request，将你的中继 URL 添加到 `registry.json`。持续维护这个注册表可以让社区更容易发现公共中继。

## Portal 如何提供端到端加密

Portal 的设计目标是让租户 TLS 在你这一侧终止，而不是在中继侧终止。在正常数据路径中，中继只转发加密流量，无法访问租户 TLS 明文。

1. 中继接收公网连接，并且只读取基于 SNI 路由所需的 TLS ClientHello。
2. 它通过反向会话将租户连接作为原始加密字节转发，而不会终止租户 TLS。
3. 你这一侧的 Portal 客户端充当 TLS 服务器，并在本地完成租户握手。
4. 对于由中继托管的域名，Portal 客户端通过 `/v1/sign` 获取证书签名，此时中继只作为无密钥签名预言机使用。
5. 会话密钥完全在你这一侧派生。中继只提供证书签名，不会接收租户流量密钥。
6. 握手完成后，中继继续转发密文，无需租户 TLS 明文即可保持流量路由。

Portal 还会检查中继是否真的保持了 TLS 透传。Portal 客户端会连接到自己的公网端点，并比较由客户端控制的两端观察到的 TLS exporter 值。如果两者不一致，`portal expose` 默认会拒绝该中继。

## 贡献

欢迎社区贡献！

1. Fork 此仓库
2. 创建功能分支（`git checkout -b feature/amazing-feature`）
3. 提交你的修改（`git commit -m 'Add amazing feature'`）
4. 推送到分支（`git push origin feature/amazing-feature`）
5. 创建 Pull Request

## 许可证

MIT License，详见 [LICENSE](LICENSE)
