# 指定账号 CPR 驱动概念验证

此目录是隔离 P0 工程，不是已安装的生产插件，也不改变 Sub2API 运行请求路径。Go 宿主测试启动一个 Rust 子进程；Rust 直接复用锁定 CPR `v3.22.1` 的 `CodexBackendClient`、请求类型、zstd 编码和 SSE transport，只执行宿主选定账号的一次 HTTP/SSE 请求。不调用 CPR `initialize`，不启动数据库、Admin、账号选择器、后台刷新或费用账本。

## 本机验证

前提：Git、Rust 1.97.0、MSVC C++ 构建工具（Windows）、protoc、Go 1.26+。`upstream/` 与 `target/` 为忽略的本地产物。

```powershell
cd tools/account-driver-poc
./prepare-upstream.ps1
rustup toolchain install 1.97.0 --profile minimal
cargo +1.97.0 build --locked
go test -v -count=1 -timeout=60s ./...
```

首次构建会下载 CPR 的固定 Git 依赖与 Cargo.lock 中的 crates。可给 `prepare-upstream.ps1 -Source` 传本地已有 CPR 仓库；仍核验固定提交。Go 合同生成代码已包含在仓库，修改 proto 后用以下命令重新生成：

```powershell
protoc --proto_path=proto --go_out=. --go_opt=module=example.com/sub2api/account-driver-poc --go-grpc_out=. --go-grpc_opt=module=example.com/sub2api/account-driver-poc proto/driver.proto
```

## Linux 容器验证

先准备固定提交的 `upstream/`，再运行：

```powershell
docker build --progress=plain -t sub2api-account-driver-poc .
```

容器 build 的最后一层实际执行相同 Go 集成测试，不使用真实 Token，也不访问 OpenAI。镜像仅用于验证，无公共业务服务。

## 通信和发送事实

子进程只监听随机 loopback 端口，stdout 输出一次版本化握手；宿主传递随机进程密钥，双向 RPC 与宿主身份回调均检查该密钥。测试传入的 `account_id`、credential revision、binding revision由宿主回调核验，拒绝其他账号。没有枚举账户或改选账户接口。

`ready/not_sent` 表示未收到发送许可。宿主在发送 `permit_send` 之前必须先把 attempt 记为 `ambiguous`；随后即使子进程发送或报告状态时崩溃，宿主也保留不可自动重放的事实。收到 CPR 上游响应头才报告 `sent`。回调拒绝或许可前取消可证明 `not_sent`。当前其他 transport 错误保守报告 `ambiguous`，尚未接入 CPR 精细错误分类。

响应使用容量为 2 的队列，取消由独立输入流和 token 处理；慢宿主不会阻止正在执行的请求取消。stdin EOF 触发进程关闭，进程代次和生产排空策略尚未接入宿主 PluginManager。

## 已证明与剩余边界

Go 测试覆盖真实 CPR 请求的账号头、zstd、未知 JSON 字段、原始未知 SSE 事件、usage 观测、一次执行、取消前无发送、身份回调隔离、发送后进程崩溃、响应背压取消和明确 429 的发送事实。仅使用脱敏 fake upstream。

复用的是公共客户端边界；编译仍传递依赖 `gateway-admin`、`gateway-core`，没有将 transport 抽成独立 SDK。CPR 成功响应公共类型只暴露部分响应 metadata，生产合同需要完整状态和多值 headers adapter。此 PoC 的 usage 仅验证两个 Token 字段，未完成生产去重、成本或扣费。

尚未验证 OAuth/Token 导入、刷新、额度、WebSocket、connection-local continuation、现有 go-plugin 握手/broker、宿主 PluginManager 接入、生产凭据保护及真实上游协议。当前 runner 是独立受管 gRPC PoC，不能当作 go-plugin v1 插件直接安装。完整“Codex 增强管理”仍不可发布；可在此公共 transport 边界继续开发经宿主生命周期管理的 HTTP/SSE preset。
