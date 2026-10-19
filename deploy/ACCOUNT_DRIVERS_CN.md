# 同一面板的账号管理方式

Sub2API 现在可为新账号选择原生管理或 `Codex 增强`。账号、授权、Token 刷新、额度、用户、分组、代理、调度、订阅和计费继续由同一个 Sub2API 管理；增强方式将 Responses HTTP/SSE 和 WebSocket 的网络执行交给受管 Rust 模块。

## 1. 构建并启用模块

驱动来源由 `drivers/codex-rs/upstream-lock.json` 固定，不使用浮动分支。Windows 在仓库根目录执行：

```powershell
./drivers/codex-rs/prepare-upstream.ps1
cargo +1.99.1 build --release --manifest-path drivers/codex-rs/Cargo.toml --locked
$env:COREPACK_ENABLE_AUTO_PIN = '0'
pnpm --dir frontend run build
Push-Location backend
go build -tags embed -o ../sub2api.exe ./cmd/server
Pop-Location
```

Windows 需要 Rust 1.99.1、protoc 与 MSVC 构建工具；Rust 输出位于 `drivers/codex-rs/target/release/sub2api-codex-account-driver.exe`。Linux 构建要求见 [驱动文档](../drivers/codex-rs/README.md)。原有 PostgreSQL、Redis 与初始化流程保持原样，不需要再部署 CPR 的数据库或 Web 控制台。

在现有 Sub2API `config.yaml` 中加入：

```yaml
account_drivers:
  codex_rs_http:
    enabled: true
    # 使用本机绝对路径；Linux 使用对应平台二进制
    executable_path: 'D:/sub2api/drivers/sub2api-codex-account-driver.exe'
    sha256: '<当前平台二进制的 SHA256>'
    start_timeout_seconds: 10
```

用 `Get-FileHash <文件> -Algorithm SHA256` 或 Linux 的 `sha256sum <文件>` 获取摘要。摘要不匹配、进程启动失败或合同不兼容时，增强方式保持不可用，账号不会自动改用原生 Responses 传输。

也可用环境变量覆盖，方便本地开发和容器配置：

```text
ACCOUNT_DRIVERS_CODEX_RS_HTTP_ENABLED=true
ACCOUNT_DRIVERS_CODEX_RS_HTTP_EXECUTABLE_PATH=<绝对路径>
ACCOUNT_DRIVERS_CODEX_RS_HTTP_SHA256=<SHA256>
ACCOUNT_DRIVERS_CODEX_RS_HTTP_START_TIMEOUT_SECONDS=10
```

重启 Sub2API 后，宿主自动启动和关闭模块。模块只监听带随机认证身份的本机 RPC 地址，无需手动创建 CPR Client Key 或设置上游 Base URL 桥接。

## 2. 在面板接入账号

在「账号管理 → 添加账号」选择：

1. 平台：OpenAI
2. 管理方式：Codex 增强
3. 接入方式：现有 OAuth、普通/移动端 Refresh Token 或标准 Codex Session 导入
4. 按原来的流程选择分组、代理、并发、优先级、倍率与模型权限

管理方式仅在运行模块已就绪时可选；可从管理员 API `GET /api/v1/admin/account-drivers` 查询有效目录。当前增强方式支持标准 OpenAI OAuth，API Key、PAT、Agent Identity 与影子账号使用原生管理。平台或认证类型变为不兼容组合时，表单会明确转回原生并解释原因。

驱动握手声明 `websocket` 且运行就绪时，可以在账号高级设置选择既有的 WS 模式；HTTP/SSE-only 旧驱动不会开放 WS。运行模块停机或换回旧版本后，已有账号保留保存的 WS 配置与绑定，普通字段仍能编辑；重新启用 WS 必须等待相应能力恢复。底层 ID 和配置键仍是 `codex-rs-http` / `codex_rs_http`，无需更改已有账号或环境变量。

WS 仍受 Sub2API 原有全局开关控制。要让面板选择的 `ctx_pool`、`passthrough`、`http_bridge` 模式实际参与路由，将以下项合并进现有 `gateway.openai_ws` 配置后重启；`mode_router_v2_enabled` 默认关闭时，账号 mode 不参与决策，仅使用历史 enabled 开关。`ingress_mode_default: off` 保持未显式选择 WS 的账号关闭，按账号设置开放：

```yaml
gateway:
  openai_ws:
    enabled: true
    oauth_enabled: true
    responses_websockets_v2: true
    mode_router_v2_enabled: true
    ingress_mode_default: off
    force_http: false
```

详情与列表包含管理方式、运行版本和可用状态；筛选、刷新、导出与批量操作使用同一绑定。运行模块停止后，已有增强账号保留原方式并显示不可用，仍可编辑普通账号设置；新的增强账号创建或导入会被拒绝。

现有账号继续使用原生方式。已有账号的 driver 切换必须从维护面板或维护 API 发起；系统保留账号 ID、分组和历史，不通过复制旋转 OAuth 凭据绕过原有安全规则。普通 PUT 仍不允许直接修改管理方式。

## 3. 当前功能范围

| 能力 | 归属与支持 |
| --- | --- |
| 授权、导入、刷新、额度、模型目录与用户配置 | 原生生命周期，共用现有面板和 API |
| Responses HTTP/SSE | 增强模式调用 Rust 模块，保留 prepared body 与多值 headers |
| Chat Completions / Messages 转换 | 复用 Sub2API 原来的 Responses 转换与结果处理 |
| 图片、Search、compact 等其他端点 | 按端点明确委托现有原生实现，不因增强执行失败临时降级 |
| 选号、并发、限速、计费和订阅 | 原有 Sub2API 主链，不建立第二份费用账本 |
| Responses WebSocket | Rust 复用 CPR 原生握手、TLS 与代理；宿主保留连接池、逐轮处理、用量和连接本地续接 |
| CPR 完整调度与独立账号生命周期 | 本 preset 未提供，继续使用 Sub2API 原有服务 |

可能已发送的进程故障、未知上游失败或首输出超时不会自动换号重放。明确的结构化额度拒绝可以沿用宿主的新链路账号轮换；增量续接继续受约束。模块没有内部重试，也不获取 Refresh Token。

Rust 模块实际复用 CPR 的 HTTP client、原生 WS opening、TLS/custom CA、代理与连接隔离；由 Sub2API 提供最终 wire，因此本 preset 不重新执行 CPR 的请求编码、OAuth、额度或 CPR 连接池。完整复用范围与固定源码更新流程见 [驱动 README](../drivers/codex-rs/README.md)。

WS 连接按账号、绑定 revision、完整 Authorization 摘要、用户/Key 范围和运行代次隔离。访问令牌刷新、运行进程重启或范围变化后，旧连接不能承接原来的增量 `previous_response_id`；无法找到所属连接时拒绝续接，由客户端显式提供完整历史开启新链。业务帧可能已发送时，不自动重连重发或转成 HTTP，避免重复执行。消息上限为 16 MiB，客户端过慢使队列超过 64 帧或 32 MiB 时明确终止连接，避免无限占用内存或控制帧挂起。Ping/Pong、关闭与取消不进入计费数据。

## 4. Docker 与更新

默认 Sub2API Dockerfile 使用 Alpine。Debian/glibc 构建的 Rust 二进制不能直接当作 Alpine 可执行文件挂载；使用新增的 [统一账号驱动镜像](Dockerfile.account-drivers) 及其部署说明，或在兼容的原生系统运行双方二进制。

更新时先准备新的固定 CPR commit 与维护补丁，运行模块合同和宿主回归测试，再更新二进制摘要并维护重启。数据库迁移是新增编号的 243/244，账号绑定随账号原子保存；不要改写已应用迁移。

运行中升级可在管理员面板的「账号驱动运行时」执行，也可调用管理员 API。系统先启动候选并校验 SHA256、握手和 HTTP/SSE/WS 能力，候选通过后原子切换；旧 generation 默认按 30 秒排空，必要时可强制结束。驱动按 SHA256 保存不可变快照，支持上一代回滚；候选能力不能低于当前代次。

账号维护切换会持久化 operation，暂停新请求、刷新和编辑，等待业务 lease 完成并清理该账号的 WS response anchor，再用 expected binding revision 事务提交新的 driver/revision。操作支持 UUID 幂等重试、取消和重启后的过期恢复。

「内容审计」菜单中的「请求审计」会保存并展示 API 请求的原始客户端上下行和已进入网关 transport 的上游上下行。内容按 request ID、分片和方向保存，管理员可查看 headers、原始 body、JSON、SSE/WS 帧及关联 attempt；审计查询自身不会递归记录。

不要在保留增强绑定的数据库上直接回退到不识别驱动字段的旧 Sub2API 主程序。该旧版本可能把新账号当作原生处理；回退必须先验证数据库、绑定、配置和驱动的兼容性。驱动失败不代表可以删除绑定或自动改写管理方式。

## 5. 本次验证

标准管理员权限、创建/导入、真实 SQL 筛选与绑定保持通过测试；Windows/Linux release 的18项 Rust RPC 合同、15项生产 runner 和12项主链/能力用例通过，覆盖 HTTP/SSE、真实 WSS/CONNECT、ctx_pool/透传两轮续接与用量、发送后断连、取消和背压。现有 WS 相关217项后端回归、168项前端测试与生产构建也通过。

没有导入真实账号或发起真实模型调用。浏览器访问权限被拒绝，视觉与实际浏览器交互验收尚未完成；组件测试、生产构建和应用 API 验证不能替代这一项。详细证据见 [实施状态](../openspec/changes/unified-account-drivers/implementation-status.md)。
