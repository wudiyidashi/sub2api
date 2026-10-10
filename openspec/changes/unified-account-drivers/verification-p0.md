# P0 账号驱动验证

验证日期：2026-10-09。Sub2API 基线 `3a6fd1c9d`，CPR 固定提交 `d875a62652e278eb5beef54356e594d1d1149e37`（v3.22.1）。隔离源码位于 `tools/account-driver-poc/`，不会进入现有生产请求路径。

本文件保留最初 HTTP/SSE PoC 的历史验收和当时未完成项。随后实际交付的面板绑定、生产 HTTP/SSE 和 WS 能力及当前限制以 [实施状态](implementation-status.md) 和 [驱动验证](../../../drivers/codex-rs/verification.md) 为准。

## 已有运行证据

Windows x64 上安装 Rust 1.97.0 后，`cargo +1.97.0 build --locked` 成功构建未经修改的 CPR `provider-openai`、`gateway-protocol` 与 Rust runner。保留上游 rust-version 与 hyper-util 固定补丁。原本默认 Rust 1.94.1 不满足要求，因此没有通过降低上游要求绕过构建。

`go test -v -count=1 -timeout=60s ./...`：7 项测试全部通过，耗时 1.30 秒。测试只使用脱敏本机 HTTP fake upstream 和随机进程密钥。

| 验证场景 | 结果与证据 |
| --- | --- |
| 指定账号一次 CPR HTTP/SSE 请求 | Fake upstream 断言 `/codex/responses`、宿主指定的 Authorization 与 ChatGPT account header，解压 CPR zstd 请求，核验未知 JSON 字段和强制 stream；请求计数为 1 |
| 原始 SSE 与 usage | 未知 SSE 事件、注释和字段保持原始字节；7 个 input、3 个 output Token 作为观测返回，未写账本 |
| 未许可取消 | `ready/not_sent` 后取消，上游请求计数为 0；身份回调已完成，未执行推理 |
| 宿主回调隔离 | 回调核验进程密钥、account ID、credential revision、binding revision，账号 99 被拒绝，无上游发送 |
| 真实发送后进程崩溃 | 上游确认收到完整请求且尚未回响应后 kill Rust 进程，Go 宿主保留发送许可前记录的 `ambiguous` |
| 响应背压时取消 | 宿主接收 headers 后停止读取，仍通过双向请求流取消；fake upstream 观察连接在 3 秒内释放 |
| RPC 身份认证与明确上游拒绝 | 缺少进程密钥被拒绝；429 保留原始错误 JSON 和明确 `sent`，请求计数为 1 |
| 进程关闭 | 每项测试关闭宿主 stdin 或主动 kill，验证子进程退出；资源清理超过 3 秒即测试失败 |

Linux x64 Debian Bookworm 容器验证已通过：`docker build --progress=plain -t sub2api-account-driver-poc .` 使用 Rust 1.97 与 Go 1.26，完整构建相同 CPR crate 后实际执行同一组 7 项 Go 测试，耗时 0.601 秒。验证镜像 ID 为 `sha256:388f3e446d715760041c987e0007bf7b16fe5aaab49a61d0b9530599a998ef95`。镜像仅作为本地验证产物，没有运行公共网关或部署现有项目。

`cargo +stable fmt --check` 与 `go vet ./...` 同样通过。Windows 的 MSVC linker 有一条建立 `.lib/.exp` 的输出 warning，构建退出码为 0。

## 实际可复用边界

已验证以下调用链可以运行，不调用 CPR 的组装根或数据库：

```text
Sub2API 风格 Go 宿主选择账号
  → 版本化握手 + 双向 gRPC Execute
  → 宿主 ResolveIdentity 回调核验账号与 revision
  → CodexResponsesRequest::from_body（force_http_sse = true）
  → CodexBackendClient::new
  → create_response_stream_with_pool_account(request, context, None)
  → CPR HTTP/zstd/SSE 实现
```

这不是新写 tiny HTTP 代替 CPR。PoC 直接引用原仓库公共 transport 客户端，保留上游未知 JSON/SSE 内容，不使用 Client Key 或 CPR 的 API gateway。Rust 编译仍依赖 `gateway-core`、`gateway-admin`，因此已证明“指定账号运行边界”，尚未完成“独立 transport SDK 抽离”。

CPR `initialize()` 会构造 selector、leases、affinity、quota、catalog、admin 和 workers，不可作为驱动入口。`CodexProvider` 属于账号调度 owner，也不应由宿主调用。HTTPRequired 分支的 `create_response_stream_with_pool_account` 直接进入一次 `outbound.body(body).send().await`，没有选号或隐式 HTTP 重试；PoC 也不创建 WebSocketPool、connection budget 或 Provider engine。

凭据类型中的 secret/metadata、Token client、profile/header、catalog/usage parser 可继续评估复用，但其 lifecycle 服务大多依赖 repository、lease、runtime policy 和持久化 ports。尚未证明生命周期可以直接抽离成一次宿主操作。CPR WebSocketPool 的 key 与 connection-local continuation owner 值对象已定位；尚未运行 WS 续接，因此不能启用 WS 能力。

## runner 合同结论

选择独立受管 gRPC runner 作为可行候选。已验证版本化 stdout 握手、随机 loopback 地址、双向请求/响应、受限宿主身份回调、取消和 stdin EOF 退出。尚未证明兼容现有 HashiCorp go-plugin 握手或 broker，不能把这个二进制放进现有插件目录就宣称可用。

身份回调与 HTTP client 配置在 `ready/not_sent` 前完成；宿主须先记录 `ambiguous`，再发送 `permit_send`。许可后输出只推进到 `sent`，不会回落到 `not_sent`。进程在发送和报告之间崩溃时，宿主仍保留保守事实，不能因 RPC 断开自动改成未发送。PoC 的宿主记录为测试内存状态；生产接入须将这一门槛纳入真正的 attempt 状态与重试判断。

双向流使用容量为 2 的响应队列和独立取消 token。输出消费者停止读取时，取消仍能结束正在执行的 HTTP 请求；控制流随 worker 退出失效，避免遗留任务。进程代次、并发 attempt、宿主排空和驱动升级仍需要生产合同测试。

## 可继续推进的范围

可继续在现有 PluginManager 内增加受管 runner adapter，并适配现有 `httpUpstream` 接口：宿主准备已选账号与 wire，建立 Execute 流，在发送许可前冻结 attempt 状态，通过响应 body reader 交付原始 SSE，在 context 取消时发 cancel并关闭流。绑定必须声明独立 transport preset，与生命周期归属分别建模，不能同时命中旧 transport v1。

CPR 成功响应公共类型只暴露部分 response metadata，无法由 PoC 恢复全部多值 headers。生产 transport adapter 需给 CPR 增加最小 response-head hook，或抽离 HTTP client 返回完整状态、headers 与 body；不能声称当前合同已经完整透明。usage 还需 request/attempt/sequence/final、实际模型、cached/image/tool等字段以及宿主现有计费映射。

目前只证明 HTTP/SSE执行与候选 runner；完整 Codex 增强管理方式仍不可发布。剩余阻碍包括 OAuth/导入/刷新/额度一次操作抽离、唯一刷新调度与 CAS、生产 PluginManager 接入、完整 response head、真实协议 fixture、WebSocket与continuation、凭据/代理隔离和并发/代次排空。原生 registry、能力 API 与面板可独立推进，增强选项应保持不可用，直到至少一种执行协议与生命周期全链路通过。
