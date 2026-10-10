# 统一镜像验证记录

本文件以下为 HTTP/SSE 阶段的统一组装镜像验收，所列 0.1.0 镜像和 hash 不代表 WS 版本。0.2.0 的 Linux driver RPC/runner 验证见 [驱动验证](../drivers/codex-rs/verification.md)，应按最新源码重新构建部署镜像；当前任务没有替换既有部署。

验证日期：2026-10-09，Linux amd64。统一镜像为 `sub2api-account-drivers-fixture` / `sha256:5712502d38e24e6c9043e5d3ec15fe389ebdcd3ffd98fc218617ccb83a5d39e9`。

本次使用当前源码交叉编译的 Go 宿主与 `backend/resources` 创建隔离 builder fixture，验证 `deploy/Dockerfile.account-drivers` 的最终组装、glibc 依赖和真实进程运行。没有重复构建原前端/backend-builder 链；完整发行构建仍须使用操作文档中的原 `backend-builder` 镜像，确保实际前端资源已嵌入。验证未替换任何已有部署或 compose。

Rust stage 从固定 CPR commit 和维护补丁实际执行 `cargo build --release --locked`。镜像内 driver 的 Linux SHA256 为 `41e7a9e3a82f381667cf14596324ce6a9230e8d720d7ae8713f3b786931b02d9`，与构建时只读摘要文件完全一致；entrypoint 据此设置宿主配置，不使用 Windows hash。

| 检查 | 实际结果 |
| --- | --- |
| 默认及显式非 root 启动 | `id` 均为 uid/gid 1000 |
| 宿主与备份工具 | Sub2API 0.2.15 与 pg_dump 18.6 可执行 |
| glibc 动态依赖 | driver 的 libgcc、libm、libc 与 ELF loader 全部可解析，没有缺失库 |
| 真实应用启动 | 使用本任务专用 PostgreSQL/Redis 和临时数据目录启动 `/app/sub2api`，`/health` 返回 ok |
| 管理能力与进程监督 | 管理员能力 API 显示 `codex-rs-http` available、`0.1.0+d875a626`；Go 宿主和 Rust 子进程均以 uid 1000 运行，未启动 PostgreSQL server |
| Release driver 的 RPC 合同 | 镜像内以 uid 1000 执行全部 9 项合同测试，通过完整 headers、raw body、401、代理、5 MiB、取消、背压、崩溃发送状态及单次发送 |
| Linux Go service 主链 | 镜像内以 uid 1000 执行 5 项真实 driver 主链测试全部通过，包含 CONNECT/custom CA、原生 usage、503 停止换号与首输出超时取消 |
| Linux prepare 脚本 | 在隔离 Linux 容器从 fresh clone 应用固定补丁成功，重复执行幂等成功；shell 语法检查通过 |

测试应用仅监听本机 loopback 临时端口，使用任务合成管理员和凭据，没有访问 OpenAI。完成后停止并移除了本次应用容器与临时网络；原任务 PostgreSQL/Redis 保留，镜像保留供复查。

尚未覆盖其他 CPU 架构、完整发行前端镜像构建、真实 AI 账号、SOCKS 和线上更新。PostgreSQL/glibc runtime 的体积大于原 Alpine 镜像，换取现成的客户端工具与已验证的 driver ABI；镜像内包含 PostgreSQL 程序包，但 entrypoint 只启动 Sub2API。
