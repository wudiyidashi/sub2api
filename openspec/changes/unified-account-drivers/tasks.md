# 实施任务与阶段验收

状态：2026-10-09 已完成 P0、持久化绑定与首个可用 preset（原生生命周期 + Codex HTTP/SSE、WS transport），同面板可选择两种方式。WS 能力按握手协商；维护切换和运行时热升级已完成，独立 CPR 生命周期仍待实施。证据见 verification-p0.md、drivers/codex-rs/verification.md 与 implementation-status.md。按依赖顺序推进，不以 UI 可见代替后台能力完成。

## P0：概念验证与合同冻结

- [x] 定位 CPR credential、transport 与 WS owner 的可复用入口，记录内部 Store/Core 依赖；独立 SDK 抽离仍未完成
- [x] Rust PoC 使用宿主指定账号完成一次 HTTP/SSE 请求，不创建 CPR 账号表、Client Key、选号器或账本
- [x] 证明实际发送后的进程断连保留 ambiguous，取消与响应背压测试通过
- [x] Go/Rust runner 在 Windows、Linux/容器验证握手、双向流、宿主回调和退出；受管 gRPC 可行，现有 go-plugin 兼容未验证
- [x] 验证最小 WS 会话与 connection-local response ID 可以保留，新增独立 OpenWebSocket 双向合同
- [ ] 冻结 capability、credential envelope、ExecuteAttempt 与 usage/error/continuation 的版本草案

验收：独立 fake upstream 和脱敏真实协议 fixture 可证明边界；若依赖大规模模拟 CPR 宿主，则调整模块范围，不能进入大规模集成。

## P1：宿主驱动门面与兼容默认

- [x] 建立 ready 驱动元数据 registry 与原生响应默认，缺少 binding 的账号保持既有路径
- [ ] 将生命周期与执行服务抽成真正可分派的原生 adapter
- [x] 首个固定 preset 的绑定采用账号两列 id/revision 原子保存，新增 243/244 迁移与全量读投影
- [ ] 独立 credential revision 收口（独立生命周期/维护切换前置条件），当前固定 preset WS 按完整 AT header 摘要隔离，不以该快照代替全局 CAS 目标
- [x] 梳理凭据写入与读取投影入口，形成 backend/internal/accountdriver/README.md 审计
- [ ] 收口创建、编辑、导入、批量、重新授权、后台/请求刷新、恢复与影子账号的凭据版本
- [ ] 将现有 OAuthRefreshAPI/executor 接入绑定解析，原生刷新锁与缓存语义不改变
- [x] 增加受管理员授权保护的能力列表，创建/编辑/筛选拒绝未知驱动及非空 driver settings
- [x] 创建/导入持久化绑定、普通编辑保持绑定、旧数据默认原生；当前拒绝切换
- [x] 维护切换 API、排空与幂等恢复
- [x] 保留旧 transport v1 与现有灰度配置，明确 native 与增强绑定的优先关系，禁止叠加转发

验收：不安装增强驱动时，原有平台创建、刷新、调用、计费和既有插件回归通过；存量账号无需重导或批量回写。

## P2：同一面板与账号生命周期

- [x] 新增“管理方式”，与平台、接入方式分别建模；增加原生列表列和筛选
- [ ] 共用分组、代理、并发、优先级、倍率和模型权限表单，提取能力驱动的专属字段
- [x] 首个 preset 在同一面板复用原生授权/RT/Session导入、额度、目录与测试；新增模式操作携带绑定
- [x] 首个 preset 复用原生唯一刷新调度，不向模块提供 RT 或新增刷新 owner
- [ ] 独立生命周期 driver 的 CAS 与格式迁移
- [x] 实现维护切换任务：阻止新准入、排空、刷新门、事务提交、旧连接失效和中断恢复
- [x] 能力缺失或运行模块不可用时显示原因、禁止新建；已有绑定保持且可筛选/普通编辑

验收：账号在面板内完成接入和管理，无需登录 CPR；兼容切换保留 ID、分组及历史，不兼容切换明确拒绝或要求重新授权。

## P3：HTTP/SSE 与既有业务继承

- [x] 受管模块接入 OpenAI 原有 HTTP/SSE 请求与账号测试，指定账号执行、绕过旧 transport 插件叠加
- [x] prepared wire 由宿主拥有，模块不二次编码；保留多值 headers/raw body，复用 CPR client/TLS/代理/连接隔离
- [x] 返回标准 http.Response，复用原有 usage/限流/服务档位/用户扣费；真实 TLS 主链 usage 已验证
- [ ] 验证分组、组合分组、模型映射、白名单、用户预算、订阅和倍率的增强路径
- [x] Chat Completions/Messages 继续使用现有 Responses 转换；新增 tracker 保持同一发送安全判断
- [x] 覆盖许可前取消、发送后断连/进程故障、背压、首输出超时；不明失败不换号，明确额度拒绝仅新链可轮换

验收：可明确启用增强 HTTP/SSE 能力；原生和增强同组时授权范围一致，一次客户端请求不会出现两份用户账单。

## P4：WebSocket 与专属端点

- [x] 实现单连接 WS driver session，按 Key/账号/完整 Authorization 摘要/绑定/运行代次隔离句柄；无全局 credential CAS
- [x] 固定 preset 复用现有逐轮准入/完成 hooks；真实 passthrough 两轮回调与独立用量、取消/关闭/进程退出已验收；业务限额与扣费服务仍属于宿主
- [ ] 验证 native continuation 与客户端完整历史重放，禁止把原增量发送给另一账号
- [ ] 验证模型目录/客户端画像/Fast 的缓存与策略归属
- [ ] 按功能分别接入 Images、Search、compact、额度重置等，增加明确 capabilities 与权限
- [ ] 验证备份恢复、影子账号、数据导出与缺失驱动的可恢复行为

验收：已测端点才能启用；不存在“Responses 可用所以全部 Codex 功能可用”的泛化声明。

## P5：上游更新与版本发布

- [ ] 建立 Sub2API 维护分支与小提交组织，记录主系统 patch 入口
- [x] 固定 CPR commit、维护补丁与摘要，编译时派生并核验来源；提供准备脚本及更新验收步骤
- [ ] 编写差异分类/能力变化报告工具与脱敏协议 fixture 合同测试
- [ ] 验证一次 CPR 升级和一次 Sub2API 升级，记录实际冲突点及适配量
- [x] 实现准备、发布、旧代次排空、兼容性检测与受限回滚流程
- [ ] 补充操作文档和前端实际截图/交互验收；设计完成不算 UI 已验收

验收：一次边界内 CPR 更新只改驱动内部和版本锁；一次 Sub2API 更新可证明原生和增强功能都完成回归。跨合同变化必须明确列出主系统修改。
