# PORTAL - Public Open Relay To Access Localhost

[English](./README.md) | [简体中文](./README.zh-CN.md)

<p align="center"><img width="800" alt="Portal Demo" src="./portal.gif" /></p>

<p align="center">安全地将你的本地应用暴露到公共互联网，无需端口转发、NAT 配置或 DNS 设置。<br />Portal 是一个具备端到端加密（E2EE）的自托管中继网络。你可以连接任意中继，也可以自行部署。</p><br />

## 为什么选择 Portal？

将本地服务发布到互联网通常很复杂。
一般需要开放入站端口、配置 NAT 或防火墙、管理 DNS，并终止 TLS。

Portal 通过反转连接模型来消除这些复杂性。
应用主动向中继建立出站连接，由中继将服务暴露到公共互联网，并将入站流量路由回应用，同时保持端到端 TLS。

与其他隧道服务不同，Portal 是自托管且无许可门槛的。你可以在自己的域名上运行中继，也可以连接任意中继。

## 特性

- **NAT 友好连接**：无需开放入站端口，也可在 NAT 或防火墙后工作
- **自动子域路由**：为每个应用分配独立子域名（`your-app.<base-domain>`）
- **端到端租户 TLS**：中继基于 SNI 路由，而租户 TLS 在你这一侧终止，并通过中继支持的无密钥签名完成证书签发
- **无许可托管**：任何人都可以运行自己的 Portal，无需审批
- **一条命令完成设置**：用一条命令暴露任意本地应用
- **UDP 中继（实验性）**：支持原始 UDP 中继

## Portal 如何提供端到端加密

Portal 的设计确保租户 TLS 在你这一侧终止，而不是在中继侧终止。在正常数据路径中，中继只转发加密流量，无法访问租户 TLS 明文。

1. 中继接受公共连接，并且只读取基于 SNI 路由所需的 TLS ClientHello。
2. 它通过反向会话转发租户连接的原始加密字节，而不会终止租户 TLS。
3. 你这一侧的 Portal 客户端充当 TLS 服务器，并在本地完成租户握手。
4. 对于由中继托管的域名，Portal 客户端通过 `/v1/sign` 获取证书签名，将中继仅作为无密钥签名预言机使用。
5. 会话密钥完全在你这一侧导出。中继只提供证书签名，不会接收租户流量密钥材料。
6. 握手完成后，中继继续转发密文，无需获取租户 TLS 明文即可保持流量路由。

Portal 还会检查中继是否真正保留了 TLS 透传。Portal 客户端会连接自己的公共端点，并比较由客户端两端观测到的 TLS exporter 值。如果两者不一致，`portal expose` 默认会拒绝该中继。

## 组件

- **Relay**：将公共请求路由到正确已连接应用的服务器。
- **Tunnel**：一个 CLI 代理，用于通过中继转发你的本地应用。

## 快速开始

### 运行 Portal Relay

```bash
git clone https://github.com/gosuda/portal
cd portal
docker compose up
```

如需部署到公网域名，参见 [docs/deployment.md](docs/deployment.md)。

### 通过 Tunnel 暴露本地服务

对于使用 `docker compose up` 启动的本地中继：

```bash
curl -ksSL https://localhost:4017/install.sh | bash
portal expose 3000 --relays https://localhost:4017
```

```powershell
$ProgressPreference = 'SilentlyContinue'
irm https://localhost:4017/install.ps1 | iex 
portal expose 3000 --relays https://localhost:4017
```

使用托管中继时，请将 `https://localhost:4017` 替换为你的中继 URL。
中继落地页也会为当前中继生成精确的安装命令。
CLI 用法和安装细节请参见 [cmd/portal-tunnel/README.md](cmd/portal-tunnel/README.md)。

### 使用 Go SDK（高级）

更多示例请参见 [portal-toys](https://github.com/gosuda/portal-toys)。

## 架构

参见 [docs/architecture.md](docs/architecture.md)。
架构决策记录参见 [docs/adr/README.md](docs/adr/README.md)。

## 示例

| 示例 | 说明 |
|------|------|
| [nginx reverse proxy](docs/examples/nginx-proxy/) | 在 nginx 后部署 Portal，并使用 L4 SNI 路由与 TLS 终止 |
| [nginx + multi-service](docs/examples/nginx-proxy-multi-service/) | 在单个 nginx 实例后与其他 Web 服务一起运行 Portal |

## 公共 Relay Registry

Portal 官方公共中继注册表地址为：

`https://raw.githubusercontent.com/gosuda/portal/main/registry.json`

Portal tunnel 客户端默认可以包含该注册表，Relay UI 也会从同一路径读取官方中继列表。

如果你运营一个公开的 Portal 中继，请提交 Pull Request，将你的中继 URL 添加到 `registry.json`。保持注册表更新能让社区更容易发现公共中继。

## 贡献

欢迎社区贡献！

1. Fork 本仓库
2. 创建功能分支（`git checkout -b feature/amazing-feature`）
3. 提交你的变更（`git commit -m 'Add amazing feature'`）
4. 推送到分支（`git push origin feature/amazing-feature`）
5. 创建 Pull Request

## 许可证

MIT License - 参见 [LICENSE](LICENSE)
