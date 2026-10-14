# 账号执行驱动验证记录

## 2026-10-14 上游融合验证

Sub2API 合并 `Wei-Shaw/sub2api` 的 `4d9ac3e48`（79 个新提交）；增强驱动固定 CPR v3.26.1 / `5358de53ddd4bda529cb63c422fd5c6af070f9c9`（56 个新提交）。宿主准入、账号生命周期、原生计费和内容审计继续沿用，CPR 的独立管理端、调度和隐私策略不作为本驱动的继承能力。

维护补丁已在干净 checkout 完整应用，并通过 prepare 重复校验。Cargo.lock 跟进上游 h2 0.4.20 和 provider-openai 的 regex 依赖；`cargo +1.97.0 fmt -- --check`、严格 clippy、locked release 构建通过。

Windows x64 release 二进制 SHA256 为 `105b9cf6db4bbe895f3d9cd7bb98208ab928ed731f4dd3c2e9102c340c05e4cb`。指定该二进制运行的 18 项协议合同和 15 项生产 runner 测试全部通过，耗时分别 2.458、2.592 秒；实际宿主 service 的 AccountDriver / Managed 回归通过（5.551 秒），覆盖 zstd 正文解码后的未知字段保留及原生 usage 解析。

前端 lint、类型检查与生产构建通过；全量 Vitest 的其余 373 个文件通过，旧侧栏断言修正后对应 3 项测试通过。Go vet 与新增 Gemini 缓存、Vertex、Haiku、分组翻译、临时不可调度及 Responses 兼容的定向 service 回归通过。

完整 Go unit 并行运行中有一个进程全局内存断言偶发失败，独立重复 5 次通过；其余包通过，service 全量串行复核通过（190.218 秒）。

本机 Docker Desktop 引擎启动失败，PostgreSQL / Redis 容器集成及 Linux release 镜像尚未复验。全量 integration 命令中的数据库测试被跳过，另有一个异步内容审核测试在并行执行中超时，其独立重复 5 次通过；不把该命令计为全量集成通过。真实上游、界面人工验收和在线管理 API 未执行。以下 2026-10-09 记录为旧版本历史证据。

## 2026-10-09 验证基线

验证日期：2026-10-09。CPR 固定 v3.22.1 / `d875a62652e278eb5beef54356e594d1d1149e37`；wrapper 0.2.0、driver API 1、历史 ID `codex-rs-http`。preset 使用 Sub2API 原生生命周期，Rust 提供 HTTP/SSE 与原生 WebSocket。

## 最终运行证据

Windows x64 release 路径为 `target/release/sub2api-codex-account-driver.exe`，SHA256 `5d01acaa3064fc429b78b80079c6266c78e4c2a46f65d28faef7a5a51b582d66`。实际二进制的 18 项协议合同（HTTP 11、WS 7）与 15 项生产 runner 测试全部通过，最近一次耗时分别 2.132、2.176 秒。`target/debug` 与原 P0 binary 不作为本发行验收依据。

Linux amd64 Debian Bookworm 用 Rust 1.97、Go 1.27.2，从相同最终 patched source 实际 release 构建。Dockerfile verification stage 复制协议测试与最新生产 runner src/tests，18 项协议合同、15 项 runner 测试全部通过，耗时分别 2.497、2.967 秒。镜像 tag `sub2api-codex-driver-verification`，ID `sha256:2e06b3a9d4fc0b2c360e6243a8468740d70139e6c4b3c5ee0c2331782cb638f0`。

`cargo +1.97.0 fmt -- --check`、严格 `cargo +1.97.0 clippy --locked --no-deps -- -D warnings` 与协议 Go vet 通过。原 Temp 基线与 P0 source 未修改；维护补丁仅作用于此 driver 的固定 upstream checkout。

## 合同证据

| 场景 | 实际验证 |
| --- | --- |
| HTTP prepared request | 原 body bytes、未知 JSON、stream 字段、多值请求头未二次改写 |
| HTTP 完整响应 | 原始 201、多值 Set-Cookie/未知头、未知 SSE/注释及 401 error body 保留 |
| HTTP 许可、故障、取消 | 未许可零请求，发送后 kill/drop 保持 ambiguous，背压取消释放连接，单次发送 |
| HTTP 响应头时限 | 200 ms header timeout 返回 ambiguous 且关闭上游；head 后 body 超过时限继续交付；首输出取消的 TLS 主链断言保持 |
| WS 两轮续接 | 相同 socket 两个原始 response.create，未知 JSON/Unicode/previous_response_id 完全保留，连接数为 1 |
| WS 身份隔离 | private session、account、binding、Key scope、AT fingerprint 伪造均在 opening 前拒绝；AT 变化建立独立身份连接 |
| WS 头与错误 | 完整多值握手头、401 原始错误 body、状态保留，握手本身仍 not_sent |
| WS 原消息与 ACK | text/binary 类型和 raw bytes 保留，2 MiB binary 往返，序号 ACK；取消写等待者不制造 sequence gap |
| WS keepalive | 上游 Ping 在 host 不 Read 时自动 Pong；并发 Ping/Pong、control timeout 保留历史业务发送水位 |
| WS 关闭与背压 | 正常 code/reason、读取期限取消、进程退出；200 MiB producer 在 host 停止消费时受背压约束，控制关闭独立；满 host 队列明确 reader_backpressure |
| WS 不重放 | socket/RPC 丢失不会自动重连、重发，历史 may-sent 不被 control failure 或下一轮未发送错误清除 |

关闭后 driver 在有界时间内顺序丢弃已经取消的输出以完成 close ACK；200 MiB bounded 断言因此在发 close 前采样，不能把关闭 drain 的流量误认为内存缓冲。关闭期间不向已取消客户端重放这些消息。

## 复用边界和缺口

维护补丁 `0001-host-owned-transports.patch` 公开 HTTP timeout client/full Response 入口、禁用 reqwest NACK 重试，并公开 CPR 原有 WS opening/dial/raw stream/显式代理边界。WS 继续经过其 TLS/custom CA、proxy、permessage-deflate、TCP/DNS/connection permit，没有另写裸 TLS 或完整网关。

Sub2API 持有 pool、准入、Key scope、account/binding/AT digest、connection-local response owner、重试预算和计费；Rust 每 RPC 只有一条 socket。driver 常驻 read 和有界队列处理原始 wire、keepalive 与资源释放，不运行 CPR selector、Store、Admin、workers 或自己的 pool，不解析或迁移 previous_response_id。

固定 source 和维护补丁在 fresh clone 实际应用及重复幂等验证通过，prepare 核验 patch SHA256 并拒绝其他修改。握手来源 commit 从 lock 编译生成，有 Git metadata 时核验实际 HEAD。

真实 ChatGPT/OAuth 用户、TLS 指纹采样、SOCKS、跨宿主 owner 路由没有验收；测试使用脱敏 loopback/自签 SAN/CONNECT fixture。原生刷新和账户生命周期沿用宿主；维护切换、跨代热升级、WS 排空和回滚已在宿主 service/API 层接入，详细状态与限制见 [实施状态](../../openspec/changes/unified-account-drivers/implementation-status.md)。协议与 runner 测试不能替代完整宿主路由、授权、订阅和扣费回归。
