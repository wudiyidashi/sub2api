# 一套面板与 Rust HTTP/SSE、WS 驱动的 Linux 镜像

本镜像把 Sub2API 宿主、嵌入的原有面板、定价资源和受管 Rust 驱动放在同一容器。面板、账号、授权、刷新、调度和计费由 Sub2API 管理，增强方式提供 Codex HTTP/SSE 与 WS transport。驱动不启动 CPR 网关。

Rust 驱动当前使用 Debian/glibc 构建，无法直接放进原默认 Alpine/musl 镜像运行。`Dockerfile.account-drivers` 使用 `postgres:18-bookworm` 的 glibc、pg_dump、psql 和 gosu；其 entrypoint 被替换为 Sub2API，容器不会启动 PostgreSQL 服务，数据库与 Redis 仍是外部依赖。

## 构建

从仓库根目录执行，先准备固定 CPR 来源和维护补丁，再构建原宿主 builder 与统一镜像：

```powershell
./drivers/codex-rs/prepare-upstream.ps1
docker build --target backend-builder -t sub2api-account-driver-builder .
docker build -f deploy/Dockerfile.account-drivers -t sub2api-account-drivers .
```

Linux 使用 `sh drivers/codex-rs/prepare-upstream.sh`，需要 Git 和 Python 3；可以给脚本第一个参数传入本地 CPR 仓库路径。其固定提交、补丁摘要与幂等检查和 PowerShell 入口一致。

`SUB2API_BUILDER_IMAGE` 可指定同架构的已构建宿主 builder。`RUST_IMAGE` 与 `RUNTIME_IMAGE` 可固定为经过验证的镜像 digest；Rust 工具链必须满足来源版本要求。Rust stage 不 clone 或追踪上游 main，只构建本地已锁定、已应用维护补丁的来源。

```powershell
docker build -f deploy/Dockerfile.account-drivers --build-arg SUB2API_BUILDER_IMAGE=sub2api-account-driver-builder -t sub2api-account-drivers .
```

两次镜像构建需使用相同目标平台；当前实测以 Linux amd64 为准。其他架构需要对应 Rust 原生构建和合同验证，不能只给宿主 Go builder 换 GOARCH。

## 运行配置

使用与原 Sub2API 相同的数据库、Redis、管理员、JWT 与数据卷配置，只把应用镜像替换为本地 `sub2api-account-drivers`。原 compose 文件和已有 Codex bridge 草稿不需要改写；部署时自行选择应用镜像覆盖。

镜像默认设置：

```text
ACCOUNT_DRIVERS_CODEX_RS_HTTP_ENABLED=true
ACCOUNT_DRIVERS_CODEX_RS_HTTP_EXECUTABLE_PATH=/app/drivers/sub2api-codex-account-driver
ACCOUNT_DRIVERS_CODEX_RS_HTTP_START_TIMEOUT_SECONDS=10
```

驱动 SHA256 在镜像构建时由该 Linux 二进制计算，entrypoint 从专用只读文件加载到 `ACCOUNT_DRIVERS_CODEX_RS_HTTP_SHA256`，无需复制 Windows hash。显式设置该环境变量时使用传入值；驱动 binary 与摘要不匹配会导致能力不可用。配置、宿主进程和 driver stdout 握手均不输出真实 Token。

WS 能力由每次启动握手协商。更新到带 `websocket` 能力的驱动后，管理员目录与账号高级设置才开放 WS；旧 HTTP-only 二进制仍可运行 HTTP/SSE。运行中升级会保留旧 generation 排空并保存 SHA256 快照，配置键 `CODEX_RS_HTTP` 和已保存的 `codex-rs-http` 绑定保持兼容。WS 的连接本地续接只在所属宿主进程可用，暂按单宿主部署；多副本路由需要另行验收。

默认 entrypoint 以 root 仅修复 `/app/data` 权限，然后经 gosu 使用 uid/gid 1000 运行宿主与 Rust 子进程。`--user 1000:1000` 也可直接运行，但数据卷需要预先允许该用户写入。driver 与摘要文件不位于可写数据卷，发布新版本应重新构建镜像，并在维护窗口完成宿主重启。

## 本地检查

```powershell
docker run --rm sub2api-account-drivers id
docker run --rm sub2api-account-drivers /app/sub2api --version
docker run --rm sub2api-account-drivers pg_dump --version
docker run --rm --user 1000:1000 sub2api-account-drivers id
```

以上检查不启动业务服务。完整启动还需要独立 PostgreSQL/Redis、一次性测试管理员与数据目录，随后应核验 `/health`、管理员能力 API 与真实受管 RPC。原生账号保持原路径，新增账号才能显式选择经运行验证可用的增强 transport。
