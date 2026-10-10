# CPR HTTP/SSE/WebSocket 账号执行驱动

`codex-rs-http` 是 Sub2API 管理的 Rust 执行进程，0.2.0 支持 HTTP/SSE 和原生 WebSocket；历史 driver ID 保持不变。账号授权、凭据刷新、额度、调度和计费仍使用 Sub2API 原生生命周期。宿主交付已选账号的 prepared 请求，驱动不启动 CPR 完整网关、账号池、控制台、数据库或 workers。

HTTP 支持 `POST /codex/responses` 和 `POST /backend-api/codex/responses`，WebSocket 支持相同路径的 opening 和原始消息收发。真实 HTTPS/WSS 目标限定为 `chatgpt.com`，本机 HTTP/WS loopback 用于集成验证。其他目标在许可前明确拒绝；OAuth 生命周期归原生服务。

## 构建

需要 Git、Rust 1.97.0、protoc 和平台 C/C++ 构建工具。Windows 需要 MSVC，Linux 需要 OpenSSL、CMake、pkg-config 与 protobuf compiler。

```powershell
cd drivers/codex-rs
./prepare-upstream.ps1
cargo +1.97.0 build --release --locked
```

`prepare-upstream.ps1 -Source <本地CPR仓库>` 可以复用本地源仓库。脚本固定 `upstream-lock.json` 的提交，核验维护补丁 SHA256，应用 `patches/0001-host-owned-transports.patch`。已有副本包含其他修改时拒绝覆盖；重复执行已应用同一补丁的副本是幂等的。`upstream/` 和 `target/` 不进入主仓库。

Linux 使用 `sh prepare-upstream.sh [本地CPR仓库]`，需要 Git 和 Python 3，采用相同来源锁与补丁校验。宿主和 driver 的统一 glibc 镜像构建见 [部署说明](../../deploy/ACCOUNT_DRIVERS_DOCKER_CN.md)。

## 宿主通信

宿主启动二进制前设置 `SUB2API_ACCOUNT_DRIVER_TOKEN`（至少 32 字符的随机进程密钥）与 `SUB2API_ACCOUNT_DRIVER_CALLBACK`（宿主 callback 的 `http://127.0.0.1:<port>`）。保留 stdin；EOF 会关闭进程。stdout 第一行 JSON 提供 `protocol=sub2api-account-driver-v1`、`api_version=1`、随机 loopback `address`、`driver_id=codex-rs-http`、版本及 CPR 来源提交。

权威合同位于 `backend/pkg/accountdriverapi/v1/account_driver.proto`。Execute RPC 与 callback 都使用 metadata `authorization=<process token>`。宿主 callback 核验 request/attempt、账号、绑定和凭据 revision，再提供该次请求的 Authorization、ChatGPT account ID 与代理；不提供 refresh token。

宿主交付最终 URL、原始 body 和多值 headers。身份 callback 只覆盖 Authorization 与 ChatGPT account ID，其余未知 headers 保留；Connection、Proxy-Connection、Transfer-Encoding、Upgrade 等传输分帧头需由宿主在准备阶段移除。驱动不会二次 JSON 解码、改变 stream、重建历史或自动压缩正文。HTTP 401/429 等仍按原始状态、完整 headers、raw body 返回，由宿主沿用现有处理和计费。

调用次序是 `start → ready/not_sent → permit_send → ambiguous → head/sent → body → end`。宿主必须先记录 `ambiguous` 再发送 permit，直到响应结束都保留输入流；输入 EOF、cancel 或 RPC context 取消会终止上游请求。`header_timeout_ms` 只等待上游响应头，超时返回 `response_header_timeout` 和 ambiguous；一旦 head 已交付，body 流不再受该时限影响。进程在许可后异常退出时不能推断为未发送。驱动不执行重试。

输入 body 上限 256 MiB，gRPC 解码上限保留额外 1 MiB wire 开销；响应 body 按最大 64 KiB 分段。有界队列和独立取消输入保证响应背压不阻止取消。连接超时 10 秒，`timeout_ms=0` 不设置总请求超时，正值按该 attempt 限制。

## WebSocket 合同与所有权

`OpenWebSocket` 双向 RPC 一次只持有一条上游连接。宿主产生随机私有 `session_id`、可信用户/Key/session 的 `key_scope_hash`，以及完整 Authorization header bytes 的 SHA256 `credential_fingerprint`。宿主身份 callback 在 `ready` 之前核验这些字段与 account/binding；Rust 再从 callback 的 AT 复核 fingerprint，不接收 refresh token。

顺序为 `start → ready/not_sent → permit_connect → handshake/not_sent → message/write_ack/closed`。握手不会发送推理 payload；宿主必须在每个业务消息交付 RPC 前记录 may-sent，write ACK 仅证明该序号的帧已写入 socket，不能证明上游推理完成或允许重放。驱动不解析或改写 `response.create` JSON，也不迁移 `previous_response_id`。

宿主现有池负责连接复用、账号/绑定/凭据 fingerprint/Key 作用域隔离、连接本地 response ID 与代次失效。AT 变化时应选择不同池 key；驱动自身没有账号或连接缓存。原生 keepalive 由驱动常驻读循环处理，上游 Ping 自动 Pong；宿主 Ping 等待对应 Pong，控制帧不标记业务发送。

消息保留 text/binary 类型和原 bytes，单条最多 16 MiB；宿主 WS RPC 的 MaxRecv 至少 17 MiB。写入必须使用从 1 开始的递增 sequence，并等待 ACK。输入写队列 1 条、输出业务队列 2 条；控制队列独立。满业务队列暂停读取业务，close/cancel 输入仍可处理；关闭时最多 100 ms 发 close，并在有界时间内读取 close ACK，避免丢弃未读 TCP 数据直接 RST。连接丢失只报告失败，不自动重连或重发。

`handshake_timeout_ms=0` 不设置 opening 超时，正值仅限制握手；连接建立后由宿主控制每次 Read/Write/Ping 的期限，取消会结束整个 RPC 与 socket。正常上游 close 返回 code/reason，失败握手保留完整多值 headers 和最多 8 KiB 原始错误正文。

## CPR 复用与更新范围

维护补丁公开既有 timeout HTTP client 构造器，禁用 reqwest 默认协议 NACK 重试，并增加 prepared request 返回完整 `reqwest::Response` 的入口。WebSocket 公开 CPR 原生 opening/dial、TLS/custom CA、代理、permessage-deflate 和 raw stream 的小边界；继续复用其 TCP/DNS/连接 permit。这些 transport 模块变化可通过固定提交与维护补丁跟进。

宿主拥有 prepared wire，因此本 preset 不调用 CPR 会重建请求正文、强制 stream 或做 zstd 的 Responses 编码入口。OAuth、目录、额度、CPR 调度/重试/连接池政策与 request encoder 不会自动进入此 preset；需要对应合同及验收后再开放。当前宿主的未知 JSON 字段、原生 WS 池与计费链继续沿用。

## 跟进 CPR 更新

先在独立开发副本选定完整新 commit，将 `upstream-lock.json` 的 commit/tag 更新为固定版本。比对 transport、TLS、代理与公开客户端改动，在新基线上重新整理最小补丁及 `patch_sha256`；其余 CPR 管理、调度和协议编码改动按本 preset 的复用边界评估，不能直接当作已继承能力。

使用 clean checkout 执行 prepare 脚本，确认固定 commit 与补丁核验成功，再以锁定工具链和 Cargo.lock 构建。build.rs 从 lock 派生握手的来源 commit，要求完整 40 位十六进制 SHA；带 Git metadata 的 checkout 还会核验实际 HEAD，driver version 则来自 Cargo package version。源码导出不含 `.git` 时仍依赖 prepare 与维护补丁的校验，不以握手字符串代替源码可信校验。

升级必须运行账号执行合同测试和 service 主链测试，并验证真实目标部署平台；涉及 request encoder、凭据结构或 WS 时需增加相应合同与验收。通过后重新计算二进制 SHA256。宿主支持管理员热升级：候选先经过摘要、握手和 transport 合同校验，再切换 generation；旧代次按期限排空，上一代快照可回滚。此流程不自动 fetch、merge 或修改用户账号；账号 driver 切换必须走宿主维护操作。

## 实际集成验证

Windows：

```powershell
$env:SUB2API_CODEX_DRIVER_BINARY = (Resolve-Path target/release/sub2api-codex-account-driver.exe).Path
cd ../../backend
go test -v -count=1 -timeout=90s ./pkg/accountdriverapi/v1 ./internal/accountdriver/runner
```

Linux 容器，从仓库根目录运行：

```powershell
docker build --progress=plain --build-context host-api=backend/pkg/accountdriverapi/v1 --build-context host-go=backend -f drivers/codex-rs/Dockerfile -t sub2api-codex-driver-verification drivers/codex-rs
```

测试实际启动 Rust 进程，用脱敏 fake upstream 核验完整头、raw body、401、代理、5 MiB 请求、发送状态、取消和背压。未指定 `SUB2API_CODEX_DRIVER_BINARY` 的普通 Go 测试会明确跳过这些外部进程测试。
