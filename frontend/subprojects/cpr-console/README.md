# CPR 账号池子项目

后台入口 `/admin/cpr` 复用 Sub2API 登录、管理员权限、二次验证和管理审计。注册、用户 API Key、分组、套餐、订阅、支付、钱包和团队消耗看板继续使用 Sub2API，其他平台保持原有驱动。

账号、OAuth、导入、令牌刷新、出口代理、客户端身份、额度、账号组、调度和执行记录使用 CPR 原生页面。CPR 管理密钥只保存在后端配置中，内部 Client Key 只由服务端按用户 Key、业务分组和明确的 CPR 账号组生成，不传入浏览器。账号组为空时拒绝授权。

## 构建和开发

`upstream-lock.json` 固定 CPR v3.29.7 和源码提交。`prepare.py` 校验管理合同及依赖基线后应用 `overrides/`，产物和依赖保存在忽略的 `.upstream/` 中。依赖锁包含 follow-redirects 1.16.1、Vite 8.3.4 安全修复。

需要 Node.js 24、Python 3 和 Git。在 `frontend/` 运行：

```sh
pnpm build:cpr-console
pnpm typecheck:cpr-console
```

主项目 `pnpm build` 同时构建主面板、团队统计和账号池，后端嵌入的账号池路径为 `/cpr-console/`。子项目依赖独立安装、锁定和审计。

开发时同时运行 `pnpm dev` 与 `pnpm dev:cpr-console`，主开发服务器将子项目请求转发至本机 3174 端口。可通过 `SUB2API_CPR_SOURCE` 指向具有固定提交的本地 CPR 源码。

## 服务连接与业务绑定

`account_drivers.cpr_managed` 配置见 `deploy/config.example.yaml`。需要运行完整 CPR 服务及其持久化数据库、Redis；现有 `codex_rs_http` 只负责传输，不能代替完整服务。管理和数据请求仅连接配置的内部 origin，不继承环境代理，也不跟随重定向。启动检查同时核对版本、源码标识和管理接口可用性。

在 CPR 中建立账号组后，到“业务分组绑定”选择套餐/API Key 所属的 Sub2API 分组及允许使用的 CPR 账号组。同一业务分组可分别绑定 OpenAI/Codex 和 Grok，CPR 在对应组内选号、轮换和重试。宿主不重放已交付给 CPR 的请求。

绑定保存使用版本校验；每次新执行重新读取有效绑定。停用绑定立即阻止新请求，已打开的 WebSocket 在下一轮重新校验。用户 Key 删除、失效、换组或账号池范围变化后，后台定期删除对应的旧内部授权。

Sub2API 根据返回用量执行原有钱包/订阅结算。CPR 记录账号实际执行情况，其统计不触发第二次用户扣费。新增模型的用户售价仍由 Sub2API 分组及渠道定价决定。

## 当前执行范围

支持 Responses HTTP/SSE/WebSocket、Chat Completions 与 Anthropic Messages 的入站转换、OpenAI Images、Grok Images、OpenAI standalone Search 和按授权账号池发现模型。WebSocket 的上游传输能力沿用 CPR 账号配置；HTTP-only 账号不能处理必须依赖上游 WebSocket 的预热与非持久化续接。

CPR v3.29.7 没有独立 `/responses/compact`，本适配不把该请求转换成另一种有额外生成成本的请求。该端点及当前未对接的 Grok 视频、语音返回明确的不支持错误。原有非托管平台路径不受影响。

## 现有账号和验证

启用配置不会自动转移已有凭据。原生账号与托管账号不能同时拥有同一套刷新令牌；在原生账号尚可调度时，业务绑定不能启用。旧账号的停机导出、令牌交接、迁移恢复以及逐账号用量关联尚未自动化，不能把打开配置视为线上迁移完成。完整 CPR 数据库也需要纳入运维备份。可选部署使用 `deploy/cpr-compose.yml`，独立的 PostgreSQL、Redis 和运行目录不发布公网端口，后端通过 Docker 内网的 `http://cpr-gateway:8080` 连接。

本地验证包含固定管理合同、管理员/二次验证边界、SSE 取消、原始下载、分组授权、条件更新、禁止重复执行及内部授权回收。`cprintegration` 构建标签提供真实 CPR 进程验收，需要 `SUB2API_CPR_INTEGRATION_FILE` 指向隔离测试连接文件，字段为 `base_url`、`admin_key`、`account_group_id`，不可使用生产账号。

已在隔离的完整 Sub2API + CPR 服务中验证用户 API Key 经业务分组转发并完成一次钱包扣费；没有迁移生产账号或部署本模块。
