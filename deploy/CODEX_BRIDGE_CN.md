# Sub2API + codex-proxy-rs 桥接部署

这个集成把 `codex-proxy-rs` 作为 Sub2API 的内部 Codex 上游。两套服务各自管理自己的数据：

- Sub2API：用户、下游 API Key、订阅、计费和多供应商路由。
- codex-proxy-rs：Codex/OpenAI OAuth 账号、Codex 客户端身份、Codex 上游请求和账号池。

不要把同一批 OAuth Token 同时导入两个项目，也不要让 Sub2API 把 OAuth Token 原文转交给桥接服务。

## 1. 初始化桥接配置

在 `deploy/` 目录执行：

```powershell
Copy-Item .\codex-proxy\config.example.yaml .\codex-proxy\config.yaml
```

编辑 `codex-proxy/config.yaml`，至少修改：

- `admin.default_password`
- 不要把这个文件提交到 Git，它包含管理端初始凭据和部署配置

在 `.env` 或当前 shell 中设置两个独立密码：

```text
CPR_DATABASE_PASSWORD=<48 位十六进制密码>
CPR_REDIS_PASSWORD=<48 位十六进制密码>
```

可选变量：

```text
CPR_IMAGE=ghcr.io/zyycn/codex-proxy-rs:v3.13.0
CPR_PORT=18080
```

## 2. 启动

从仓库根目录执行：

```powershell
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.codex.yml up -d
```

Sub2API 仍然使用 `8080`。桥接服务只绑定宿主机 `127.0.0.1:18080`，容器之间通过
`http://codex-proxy-rs:8080` 通信。

打开 `http://127.0.0.1:18080`，在 codex-proxy-rs 管理端：

1. 添加 OpenAI OAuth/Codex 账号。
2. 建立账号分组。
3. 创建一个 Client Key，并复制完整密钥。

## 3. 在 Sub2API 中添加桥接账号

在 Sub2API 管理端新增一个 OpenAI API Key 账号：

- 平台：`OpenAI`
- 类型：`API Key`
- Base URL：`http://codex-proxy-rs:8080`
- API Key：上一步创建的 codex-proxy-rs Client Key
- 将该账号绑定到需要使用 Codex Bridge 的 Sub2API 分组

Sub2API 会把该账号的 Responses 请求拼成：

```text
http://codex-proxy-rs:8080/v1/responses
```

codex-proxy-rs 再把请求转成官方 Codex 上游协议：

```text
https://chatgpt.com/backend-api/codex/responses
```

模型目录、SSE、Responses WebSocket 和账号轮换由 codex-proxy-rs 处理；Sub2API 继续记录下游用户和
API Key 的用量。

## 4. 验证

先检查两个服务健康状态：

```powershell
Invoke-WebRequest http://127.0.0.1:8080/health
Invoke-WebRequest http://127.0.0.1:18080/healthz
```

再从 Sub2API 的客户端 API Key 发起一次 Responses 请求。若失败，按顺序检查：

1. Sub2API 容器能否解析 `codex-proxy-rs`。
2. Sub2API 的账号 Base URL 是否没有写成宿主机地址。
3. Client Key 是否启用且绑定了可用的 codex-proxy-rs 账号分组。
4. codex-proxy-rs 管理端中的 OAuth 账号是否仍然有效。

## 安全边界

桥接服务的管理端口默认只暴露在本机；不要把 `18080` 直接暴露到公网。公网访问应通过独立的 HTTPS
反向代理，并分别保护 Sub2API 和 codex-proxy-rs 的管理端。
