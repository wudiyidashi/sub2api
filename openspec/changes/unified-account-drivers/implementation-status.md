# 首个可用账号驱动版本与验收状态

日期：2026-10-09。当前实现两种面板管理方式：原生管理，以及“原生生命周期 + Codex HTTP/SSE、WS transport”的固定增强 preset。该范围可实际创建、导入和执行请求；完整 CPR 生命周期仍未开放，账号维护切换和受管运行时热升级已交付。

## 已交付代码

- 后端 `internal/accountdriver` 注册表，只公开已就绪的原生/增强方式，运行失败不切换账号绑定
- 管理员能力接口 `GET /api/v1/admin/account-drivers`，沿用现有管理员鉴权
- 迁移 243/244、Ent/SQL/Redis 读投影；账号两列 id/revision 原子保存，存量默认原生，数据库记录初始 revision=1，旧缓存0仅兼容缺字段
- 完整/lite DTO、导出/Session导入、普通编辑和真实 SQL 模式筛选保留绑定；未知模式、不兼容平台/认证、非空设置和切换明确拒绝
- 面板创建的管理方式入口、只读编辑状态、账号列表列和筛选；筛选贯穿刷新、导出、倍率与批量操作
- 新旧服务兼容：目录 404 保留原生流程；其他加载错误显示提示；未集成驱动不可选
- 受管 Rust/Go HTTP/SSE 运行模块，复用锁定 CPR client/TLS/custom CA/代理/连接隔离，返回完整响应；API1、来源/补丁锁、摘要校验、取消、发送状态与进程关闭
- 单连接 WS 双向 RPC，复用 CPR prepared opening、TLS 与代理；宿主保留连接池、response ID 归属和逐轮业务处理，按 Key/账号/AT 摘要/绑定/运行代次/URL/代理隔离
- 运行时协商 `websocket`，旧 HTTP-only 驱动不启用 WS；面板开放现有模式，停机/降级保留已保存配置，编辑窗口打开期间同步最新能力
- WS 可能已发送、未知续接或不兼容连接时禁止自动重放；失去 WS 能力或强制 HTTP 时也拒绝原连接的增量续接，提示客户端以完整输入开启新链
- 消息 16 MiB，宿主读队列 64 帧/32 MiB；慢消费超限明确关闭，Ping/Pong、write ACK 与取消不会因满队列无限挂起
- 真实 Gateway 与账号测试分派，保留 prepared wire；费用、额度、模型规范化与唯一 Token 刷新继续原生，不发送 RT 给执行模块
- 首输出超时、未知失败与发送后断连停止换号；结构化额度拒绝只允许新链按宿主政策轮换
- 可选统一 Debian/glibc Dockerfile，将宿主与模块放在同一应用容器，两者均以 uid1000 运行；不运行第二套网关或数据库
- Makefile 将新的前端驱动测试加入 critical 测试名单；tools/check_account_driver_api.mjs 提供真实本机 API smoke

## 运行验证

工具链：后端 Go 1.27.2；前端 Node 20.19/pnpm 10.6；Rust 1.97。未降低正式工程版本要求，前端 package.json 和 lockfile 未因本地安装变更。Ent 生成在 Windows 出现短暂文件映射锁，最终通过写入重试完成，仅账号相关生成代码改变；主 go.mod 未改。

后端通过：

```text
go test -tags unit ./internal/accountdriver ./internal/handler/admin ./internal/handler/dto ./internal/server/routes -count=1
go test -tags unit ./internal/service -run 'Test(CreateAccount|UpdateAccount|DuplicateAccount|BulkUpdateAccounts|AccountDriver|OAuthRefresh|PersistAccountCredentials|AccountCredential)' -count=1
go build ./cmd/server
go build -tags embed ./cmd/server
```

覆盖目录不可变性、ready 状态、原生/增强创建、完整/lite 默认、绑定编辑保持/切换拒绝、筛选/导出/导入、管理员未登录401/普通用户403/管理员200，以及账号与 OAuth 回归。真实 PostgreSQL 测试覆盖 CRUD、SQL count/分页隔离、旧 snapshot 不覆盖绑定、刷新和 bulk 凭据更新保持绑定。

前端通过类型检查、i18n 完整性、驱动选择器/API 与既有创建/编辑/列表/筛选/批量测试（最终9个目标文件168项）；并完成生产构建。包含新建能力协商、旧运行时、已保存配置保持，以及面板保持打开时更新失效 binding 的回归。目标 ESLint 使用现有 transitive vue-eslint-parser 路径运行，未为本次功能修改依赖。仓库的普通 ESLint 命令在 pnpm 隔离依赖下存在 parser 顶层解析缺口；目标检查的特殊环境需保留在验证记录中。

初始 P0 Windows/Linux 各7项测试通过，见 [verification-p0.md](verification-p0.md)。HTTP 阶段生产合同 Windows/Linux release 各11项通过：完整状态/多值 headers、raw body、401、代理、5 MiB 请求、身份隔离、许可前取消、发送后 kill/drop、背压、单次发送，以及响应头超时与其后长 body。响应头等待继承 gateway.openai_response_header_timeout，外部取消与超时都 abort 并等待同一网络任务。

WS 版本为 driver 0.2.0，保留 `codex-rs-http` ID/API1。最终 Windows release SHA256 为 `5d01acaa3064fc429b78b80079c6266c78e4c2a46f65d28faef7a5a51b582d66`。同一 binary 的18项 RPC 合同（HTTP11+WS7）与15项 host runner测试通过，包含多轮原始消息、2 MiB binary、重复握手头、双方 Ping/Pong、关闭码、握手拒绝正文、取消、进程退出、scope 篡改零上游、并发写序列与背压终止。runner 区分已完成轮次的连接历史和新一次尚未 Send 的操作，控制失败不能清除在途业务发送事实。平台构建与新来源补丁证据见 [运行模块验证](../../../drivers/codex-rs/verification.md)。

相同固定来源的 Linux amd64 release 镜像 `sub2api-codex-driver-verification` / `sha256:2e06b3a9d4fc0b2c360e6243a8468740d70139e6c4b3c5ee0c2331782cb638f0` 实际执行18项 RPC 合同和15项最新生产 runner 测试，均通过。Rustfmt、严格 Clippy、fresh clone 应用补丁及重复 prepare 幂等检查通过。

真实 Go service 主链5项通过：真实 Rust 与本机 CONNECT→自签 chatgpt.com TLS、完整头/SSE、原生17/5/cached4用量、503与首输出超时不重放、Stop不回落原生。发送 guard 单元测试另覆盖明确429与增量链区分。

新 WS 主链已用实际 Windows release + 本机 CONNECT/custom CA WSS 验证：两轮 `previous_response_id` 同一 socket、逐轮2/3用量、发送后断连只有一次create/CONNECT且零 HTTP 回落。能力丢失与全局 ForceHTTP 后续接返回400，未增加任何上游请求。另有真实客户端 WS passthrough 两轮，BeforeTurn/AfterTurn 各执行[1,2]，分别解析 input_tokens=2/3，共用一个 CONNECT且原生 HTTP 调用为0。额外覆盖可信 binding 不接受身份头注入、AT/Key/绑定/代次/代理的连接池兼容性和每轮发送事实。

内容审计工作台已融合到安全审计菜单，增加请求审计标签和请求详情页。迁移246新增原始 payload 分片表；全局 API middleware 保存客户端原始 request/response headers 与 body，网关上游 transport 同时保存 upstream request/response，默认上限64 MiB、1 MiB分片、异步队列落库。详情页按四个方向展示原始文本、JSON、SSE/WS事件、headers、大小和截断状态，并关联 Ops 错误、上游 attempts 与 Prompt 风险事件。内容审计查询接口本身不再次写入审计表。

既有 WS/Pool/Ingress/Passthrough/AccountDriver 相关217个顶层用例通过（31.760s）；`internal/service/openai_ws_v2` 独立完整测试另通过（3.298s）。本次新增真实驱动主链与能力/隔离用例共12项（HTTP5+WS3+unit4），使用最终 0.2.0 Windows release 通过（3.440s）。最终前端构建后已重新编译带 embed 的宿主，未使用旧面板资源。

Linux 最终驱动镜像再叠加当前完整 backend 源码执行同样12项主链/能力测试，全部通过（3.436s）；验证镜像 `sub2api-codex-ws-service-verification` / `sha256:ffe0a2492ef7dcc39c36e25d824ab6f8bfeffad778a2feb6eb05932de71ce116`。实际 driver SHA256 为 `2efd52001a0bb5a914b8972e9442401acd28400a96625afb9bc32c46dcd4aebc`，容器握手再次确认 version=0.2.0、http/sse/websocket。专属 dockerignore 明确保留测试源，实际日志确认12项 PASS/0项 SKIP，未把空测试算作验收。该验证镜像不是完整发行应用镜像，原标准 Docker 前后端双 builder 链仍按下文的范围说明。

独立审查发现 Images handler 原有循环忽略 NextAccountStop，已补防线。真实 Rust Images handler 的 HTTP503与SSE错误都只发送第一账号一次、不回落原生且不增加换号；原生两账号重试回归仍通过。

完整 service 套件已运行（Go1.27.2、unit、parallel4、timeout10m），未全绿：`TestCommandCodeGatewayPassesThroughCatalogProtocols` 与 `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort` 失败。对原始 HEAD 的只读归档按相同工具链/参数复测，准确复现相同两项失败；未修改这些业务或放宽测试。新增账号字段触发的 plugin snapshot denylist 分类回归已修复且不再失败。不能将完整后端报告为全部通过，证据保存在本次 .dev/account-drivers 的 current/baseline 日志。

统一 Linux 镜像实际 release 构建驱动，验证 ldd、pg_dump18.6、uid1000、宿主/子进程与 /health/ready目录；镜像内 release 模块再通过9项合同及5项主链测试。宿主镜像这次使用本机交叉编译的 builder fixture；标准 Dockerfile --target backend-builder 构建命令已提供，但该完整双阶段构建路径未重复运行。不能将 fixture builder 验证写成标准链路全部执行。

真实应用在独立 PostgreSQL18/Redis8 与 DATA_DIR 启动，不接触原数据库。使用生成的 fixture 管理员、合成管理状态和无效凭据，不调用真实模型或提交用户合规确认。Windows 与统一 Linux 镜像的 API smoke 验证 ready 增强 OAuth 创建、原生生命周期、id/revision持久化、SQL筛选、switch/type拒绝。实际 kill Windows模块后，目录不再公开增强，已有绑定仍可读/过滤/普通编辑，创建增强明确拒绝；没有降级为native。

WS 更新后的 Windows 实际应用 API smoke 又验证了协商目录、增强账号 ctx_pool 配置保存与普通编辑、SQL筛选。终止该 fixture 的 Rust 子进程后，目录只剩 native；原账号仍为增强且 available=false，保留之前的 WS capability 和配置。同值 extra 普通编辑200，修改 WS 模式400且数据库值保持原样。测试实例和专用 PostgreSQL/Redis 已停止。

浏览器访问本机测试页面被浏览器权限工具拒绝，原因是此次访问授权被拒绝；未通过其他浏览器或自动化绕过。**未完成视觉与实际浏览器交互验收**，组件测试、构建和 API smoke 不能替代这一项。

## 仍未交付

增强方式可在配置受管模块后供标准 OpenAI OAuth 选择；WS 同时受原有全局开关和账号模式控制。账号维护切换与运行时代次升级现已开放：维护任务持久化绑定版本、暂停新准入、等待业务 lease 和 WS 连接排空，再以 CAS 提交 driver/revision；进程升级先完成摘要、握手和 HTTP/SSE/WS 合同检查，候选成功后原子发布，新请求进入新代次，旧代次按期限排空或由管理员强制结束。升级记录保存不可变二进制快照，支持上一代回滚；候选能力不能低于当前代次。当前连接本地续接需要原宿主进程存活，多副本 owner 路由未验收。这个二进制使用独立受管RPC能力，不能冒充既有go-plugin transport v1包。

完整 credential revision/CAS 的写入、缓存、恢复与影子边界已审计到 backend/internal/accountdriver/README.md；首个 preset 不另写凭据或新增刷新 owner，RPC只使用已准备请求的AT快照。当前 WS 凭据身份是完整 Authorization 的 SHA256，RPC credential_revision=0；全局凭据 CAS 仍是独立生命周期的后续目标。绑定更新只能通过维护切换的持久化操作和 expected revision 完成，普通 PUT、批量编辑、刷新、隐私、配额和恢复操作在维护期间会被拒绝；进程重启会恢复未完成任务并将过期任务标记为失败后恢复原 schedulable 状态。

本批代码未提交、未推送、未部署到既有服务；原来的桥接草稿与本地改动保持原样。
