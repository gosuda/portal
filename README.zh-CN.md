# PORTAL - Public Open Relay To Access Localhost

[English](./README.md) | [简体中文](./README.zh-CN.md)

<p align="center"><img width="800" alt="Portal Demo" src="./portal.gif" /></p>

<p align="center">将你的本地应用暴露到公网，无需端口转发、NAT 配置或 DNS 设置。<br />Portal 是一个自托管、端到端加密（E2EE）的中继网络。你既可以连接任意中继，也可以自己部署。</p><br />

## 为什么选择 Portal？

将本地服务发布到互联网通常很复杂。
通常需要开放入站端口、配置 NAT 或防火墙、管理 DNS，并终止 TLS。

Portal 通过反转连接模型来消除这些复杂性。
应用主动向中继建立出站连接，由中继把服务暴露到公网，并将传入流量路由回应用，同时保持端到端 TLS。

与其他隧道服务不同，Portal 是自托管且无需许可的。你可以在自己的域名上运行中继，也可以连接任意中继。

## 特性

- **NAT 友好连接**：无需开放入站端口，也能在 NAT 或防火墙后工作
- **自动子域路由**：为每个应用分配独立子域（`your-app.<base-domain>`）
- **租户端到端 TLS**：中继通过 SNI 路由，而租户 TLS 在你的侧边通过 relay-backed keyless signing 终止
- **无需许可的托管**：任何人都可以运行自己的 Portal，无需审批
- **单命令启动**：用一条命令暴露任意本地应用
- **UDP Relay（实验性）**：支持原始 UDP 转发

## Portal 如何提供端到端加密

Portal 的设计目标是让租户 TLS 终止在你的侧边，而不是在中继侧。在正常数据路径中，中继只转发加密流量，无法访问租户 TLS 明文。

1. 中继接收公网连接，并且只读取 SNI 路由所需的 TLS ClientHello。
2. 中继通过反向会话转发原始加密字节，不会终止租户 TLS。
3. 你侧边的 Portal 客户端作为 TLS 服务器，在本地完成租户握手。
4. 对于 relay-hosted domain，Portal 客户端通过 `/v1/sign` 获取证书签名，把中继仅作为 keyless signing oracle 使用。
5. 会话密钥完全在你的侧边派生。中继只提供证书签名，不会获得租户流量密钥。
6. 握手完成后，中继继续转发密文，无需访问租户 TLS 明文即可持续路由流量。

Portal 还会检查中继是否真正保留了 TLS passthrough。Portal 客户端会连接自己的公网端点，并比较由两端客户端控制的 TLS exporter 值。如果两者不同，`portal expose` 默认会拒绝该中继。

## 组件

- **Relay**：负责把公网请求路由到正确已连接应用的服务器
- **Tunnel**：通过中继代理本地应用的 CLI 代理

## 快速开始

### 运行 Portal Relay

```bash
git clone https://github.com/gosuda/portal
cd portal
docker compose up
```

如果要部署到公网域名，请参见 [docs/deployment.md](docs/deployment.md)。

### 通过 Tunnel 暴露本地服务

先从官方 GitHub release asset 安装 tunnel：

```bash
curl -fsSL https://github.com/gosuda/portal/releases/latest/download/install.sh | bash
portal expose 3000
```

```powershell
$ProgressPreference = 'SilentlyContinue'
irm https://github.com/gosuda/portal/releases/latest/download/install.ps1 | iex
portal expose 3000
```
CLI 用法和安装细节请参见 [cmd/portal-tunnel/README.md](cmd/portal-tunnel/README.md)。

### 使用 Go SDK（高级）

更多示例请参见 [portal-toys](https://github.com/gosuda/portal-toys)。

## 架构

请参见 [docs/architecture.md](docs/architecture.md)。
架构决策请参见 [docs/adr/README.md](docs/adr/README.md)。

## 示例

| 示例 | 说明 |
|---------|-------------|
| [nginx reverse proxy](docs/examples/nginx-proxy/) | 将 Portal 部署在 nginx 后面，使用 L4 SNI 路由和 TLS 终止 |
| [nginx + multi-service](docs/examples/nginx-proxy-multi-service/) | 在同一个 nginx 后面同时运行 Portal 和其他 Web 服务 |

## 公共 Relay Registry

Portal 的官方公共 relay registry 是：

`https://raw.githubusercontent.com/gosuda/portal/main/registry.json`

Portal tunnel 客户端可以默认包含这个 registry，relay UI 也会从同一路径读取它，以展示官方 relay 列表。

如果你在运营公共 Portal relay，欢迎提交 Pull Request，把你的 relay URL 添加到 `registry.json`。保持 registry 更新有助于社区更容易发现公共 relay。

## 贡献

欢迎社区贡献！

1. Fork 本仓库
2. 创建功能分支（`git checkout -b feature/amazing-feature`）
3. 提交修改（`git commit -m 'Add amazing feature'`）
4. 推送分支（`git push origin feature/amazing-feature`）
5. 创建 Pull Request

## 许可证

MIT License，参见 [LICENSE](LICENSE)
