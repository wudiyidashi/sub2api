# 账号驱动宿主绑定与首个增强 preset

`GET /api/v1/admin/account-drivers` 返回当前可选择的管理方式。原生 `native` 固定可用；`codex-rs-http`（Codex 增强）由受管运行模块注册可用性、版本与协商的传输能力，启动成功才进入列表。未就绪驱动不进入新建选项，显式选择未知或不可用方式失败，不会退回原生。

迁移 `243_account_driver_bindings.sql` 在 `accounts` 增加 `account_driver_id`（默认 native）与 `account_driver_revision`（默认 1）。绑定随账号原子创建；旧记录自动保持原生，旧 Redis 快照缺字段时仍按 native、revision 0 兼容。正常编辑、刷新与批量凭据更新不改绑定。编辑只接受同一个 ID，切换返回 `ACCOUNT_DRIVER_SWITCH_UNSUPPORTED`。非空 `driver_settings` 返回 `ACCOUNT_DRIVER_SETTINGS_UNSUPPORTED`，空对象可接受但不持久化。

迁移 `244_codex_http_binding_compatibility.sql` 只约束增强绑定的标准 OpenAI OAuth 组合。`credentials` 与 `extra` 的 `auth_mode` / `openai_auth_mode` 只接受缺省、null、空字符串或 oauth（忽略前后 ASCII 空格和大小写），布尔值、数字、对象和非标准模式会被拒绝。原生账号不受该约束。若已有增强记录不合规范，迁移会明确失败，需修复或重新授权记录后重试；不会自动修改凭据或降级绑定。

管理服务在修改前验证最终合并状态，包括整批所有目标、AccountService 更新、公共凭据持久化、CRS 同步和 Codex Session 匹配更新。原生与增强混合批量操作不能在拒绝增强目标前先写原生目标。SQL 约束同时覆盖旧快照与直接 SQL 写入。普通导出备份导入只新建账号，不按名称覆盖既有账号；Codex Session 导入省略管理方式时保留匹配账号的原绑定，显式指定另一方式仍拒绝切换。

首个增强 preset 只接受 OpenAI 标准 OAuth 账号。授权、导入、TokenProvider、OAuthRefreshAPI、额度和模型目录由 native 宿主管理，刷新锁仍按同一个账号隔离。增强模块执行宿主准备好的 Responses HTTP/SSE 请求或一条 WS 连接，接收已取得的 Access Token 快照，不接收 refresh token，不选择账号，不启动独立刷新 worker。API Key、setup-token、Personal Access Token、Agent Identity 和 spark 影子暂不支持增强方式；原生流程继续支持其已有组合。

描述明确返回 `lifecycle_driver_id: native`、`execution_driver_id: codex-rs-http`、`transport_protocols: [http, sse]`、`execution_endpoints: [responses]`。Images、Search 与 compact 声明为 `native_delegated_endpoints`，仍由宿主路径执行；绑定增强的这些请求也不叠加旧 transport v1 插件。Chat Completions 继续使用宿主已有转换入口，不能把该声明解释为增强模块直接提供所有 OpenAI 原生端点。

列表与详情返回真实 binding revision、版本和可用性。运行模块停止时，保存的增强 ID 保持不变，`available` 变成 false；普通账号元数据仍能编辑，推理执行和新增增强绑定应失败。`SetAccountDriverRuntimeAvailability(id, available, version)` 为受管模块的启动、退出和健康失败通知入口。

原生驱动的平台列表继承 `domain.ConcretePlatformIDs()`。能力列表描述现有原生操作的集合，每个平台和认证方式仍受其现有接口校验约束；不能根据列表里的 `refresh` 推断 API Key 支持 Token 刷新。

列表、上游倍率快照、导出和按筛选批量编辑使用同一 SQL 条件过滤绑定，count 与分页也保留该条件。已注册但不可用的增强方式仍允许读过滤；未知值返回 `ACCOUNT_DRIVER_UNKNOWN`。备份导出新增显式 `account_driver_id`，恢复导入时验证可用性及平台、认证组合；不会读取 `extra` 内同名字段作为绑定。Codex Session 导入新增显式选择，新建保存绑定，匹配既有账号时拒绝切换。OAuth 账号复制沿用原有旋转凭据禁止复制规则。

## 维护切换、热升级与完整凭据版本

首个 preset 使用账号行上的单一绑定，没有另建 CPR 账号表或空绑定表。它复用原生生命周期，不支持通过普通 PUT 直接切换或写入专属 driver settings。维护 API 会创建持久化 operation，暂停新准入、刷新和编辑，等待业务 lease 完成，清理该账号的 WS response anchor，并在 expected binding revision 和当前凭据快照仍匹配时事务提交新的 driver/revision；UUID operation 支持幂等重试、取消和进程重启后的过期恢复。WS 使用独立双向 RPC，但池、逐轮鉴权/名额、response ID 和计费属于原宿主；旧 HTTP-only 驱动的握手不会开放此能力。

运行时升级由同一个 AccountDriverManager 执行。候选二进制先复制到按 SHA256 命名的只读快照，校验进程认证、API、HTTP/SSE/WS transport 和版本，再原子发布 generation。旧 generation 继续服务已有 HTTP/WS，按 deadline 排空或由管理员强制 drain；新 generation 不支持当前 active 的完整 transport 集合时拒绝发布。发布状态和上一代快照持久化到数据目录，支持 rollback；跨宿主 WS owner 路由仍未开放。

`credential_revision` 需要独立实施与回归。目前凭据持久化有以下入口，不能只修改 `persistAccountCredentials`：

| 边界 | 当前实现 | 后续要求 |
| --- | --- | --- |
| 创建及复制 | `repository/account_repo.go`: `createAccountRecord`、`CreateWithAccountGroups` | 创建账号确定初始版本；账号与绑定原子创建 |
| 常规编辑 | `account_repo.go`: `Update`、`UpdateWithAccountBillingSettings`、`updateLockedAccount` | 锁内比较规范化 JSON，实际改变时原子推进版本 |
| 专用凭据更新 | `account_repo.go`: `UpdateCredentials` | 与凭据、受管 extra 清理、scheduler outbox 同事务推进 |
| Grok OAuth CAS | `account_repo.go`: `UpdateGrokOAuthCredentialsIfUnchanged` | 保留完整凭据和代理 CAS，增加 revision 核验 |
| 批量更新 | `account_repo.go`: `BulkUpdate` 的 JSON 合并 SQL | 按每行实际合并结果决定是否推进，保留批量调度失效语义 |
| 刷新 | `service/oauth_refresh_api.go`、`token_refresh_service.go`、各 TokenProvider | 单一账号刷新锁；在上游操作前冻结版本，提交时 CAS；失效结果不得覆盖新凭据 |
| 导入及重新授权 | `handler/admin/account_data.go`、`account_codex_import.go`、`account_handler.go` 与 `service/crs_sync_service.go` | 覆盖 Create/Update 与专用凭据更新；不能仅覆盖 UI 新建 |
| 影子账号 | `service/account_credentials_persistence.go`、`admin_account.go`、`openai_agent_identity.go` | 继续由母账号持凭据；实际执行快照冻结母账号版本和派生关系 |
| SQL 恢复 | `service/backup_service.go`: `DBDumper.Restore` | 恢复直接写数据库，需核验旧备份缺字段及恢复后调度/Token缓存失效 |

读取投影也必须覆盖 Ent schema/生成代码、`service.Account`、`accountEntityToService`、轻量调度投影、`scheduler_cache.go` 的完整/元数据序列化、Token缓存和驱动回调快照。只添加字段会留下“数据库已变、缓存仍是旧版本”的窗口。

首个增强执行使用原生准备好的 Access Token 与维护 API 的不可切换绑定，不需要引入第二个凭据刷新提交者，因此本批没有修改全局 credential revision。当前 WS 用完整 Authorization 的 SHA256、账号、绑定 revision、用户/Key 范围、运行代次与代理/目标隔离；凭据变更后原增量链不能转给新连接，RPC credential_revision=0 不冒充全局版本。独立生命周期和全局 credential CAS 仍是后续目标。
