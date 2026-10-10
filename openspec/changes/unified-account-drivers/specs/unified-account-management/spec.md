# 统一账号管理规格草案

以下为统一账号管理的目标能力需求。当前固定 preset 已实现面板绑定、HTTP/SSE、WS、账号维护切换与运行时热升级；独立 CPR 生命周期和跨宿主 owner 路由等未交付项以 implementation-status.md 和 tasks.md 为准。

## ADDED Requirements

### Requirement: 管理方式与接入方式分别选择

系统 SHALL 在同一账号管理面板提供平台、管理方式、认证接入方法，展示后端报告并通过验收的有效组合。

#### Scenario: OpenAI 增强接入

- GIVEN 已安装、配置且运行正常的 Codex 增强驱动
- WHEN 管理员选择 OpenAI 与 Codex 增强管理
- THEN 页面展示该驱动支持的 OAuth/Token/API Key 接入方法及专属设置
- AND 分组、代理、并发、优先级、倍率与用量沿用同一宿主账号
- AND 不要求管理员登录另一套 CPR 管理端

#### Scenario: 存量账号

- GIVEN 账号没有驱动绑定
- WHEN 加载、刷新或调用此账号
- THEN 使用已有原生行为，包含原来已配置的 transport 插件规则
- AND 不要求重新导入、不创建第二份账号记录

### Requirement: 账号与凭据具有唯一权威

系统 SHALL 由 Sub2API 持久化账号、绑定与凭据；驱动只在操作期间使用必要凭据，不维护另一套账号池或后台刷新调度。

#### Scenario: 并发刷新

- GIVEN 同一账号同时收到后台刷新与请求侧刷新
- WHEN 两条路径进入驱动
- THEN 使用同一个宿主账号刷新门与锁
- AND 提交时核验 credential revision 和当前 binding
- AND 过期刷新结果不覆盖新授权或新驱动结果

#### Scenario: 凭据更新遗漏防护

- GIVEN 账号凭据通过批量导入、编辑或重新授权更新
- WHEN 事务提交
- THEN 推进相同 credential revision 并失效关联缓存
- AND 驱动连接不能继续按旧凭据版本接纳新请求

### Requirement: 调度与计费由宿主统一负责

系统 SHALL 在鉴权、分组过滤、预算检查、账号选择与准入后，调用选中账号绑定驱动的一次执行。驱动 SHALL NOT 另选账号、另扣用户余额或另建 Client Key。

#### Scenario: 两种管理方式共用分组

- GIVEN 分组包含原生与增强账号
- WHEN 用户调用允许的模型
- THEN 宿主按现有政策选号，再调用对应驱动
- AND 两种路径遵守相同模型权限、并发、倍率和订阅规则

#### Scenario: 驱动不可用

- GIVEN 账号显式绑定增强驱动且该驱动不可用
- WHEN 调度考虑该账号
- THEN 报告其不可用状态，不把同一账号自动改走原生驱动
- AND 只有满足现有安全边界的未发送请求可由宿主选其他可用账号

### Requirement: 发送与交付决定恢复安全

系统 SHALL 记录单调发送事实及不可撤销的响应交付边界。驱动错误不得通过简单 HTTP 状态码推断安全重放。

#### Scenario: 执行进程在上传后退出

- GIVEN 业务请求可能已到上游
- WHEN 驱动未返回确定发送结果就退出
- THEN 宿主按 ambiguous 处理
- AND 不透明重放该业务请求到新账号

#### Scenario: SSE 已交付后失败

- GIVEN 宿主已提交下游业务响应
- WHEN 上游失败或驱动退出
- THEN 终结当前响应并记录已获得的用量
- AND 不切换账号/驱动后继续拼接另一份响应
- AND 在同一执行清理流程释放准入并完成现有计费口径下的结算

### Requirement: 原生续接保留真实作用域

系统 SHALL 将 continuation 绑定到宿主用户/Key、账号、credential revision、binding revision 和驱动运行代次，以及需要时的原上游连接。

#### Scenario: 连接本地增量历史失效

- GIVEN previous_response_id 只能在原连接继续
- WHEN 原连接或驱动代次失效
- THEN 返回可识别的客户端完整重放要求或明确错误
- AND 不把增量输入交给其他账号或另一个用户/Key 的连接

#### Scenario: 既有 WS 下一轮

- GIVEN 同一客户端 WebSocket 已完成一轮响应
- WHEN 客户端开始下一轮
- THEN 宿主复核当前鉴权与授权范围
- AND 续接仍必须满足原账号及会话绑定约束

### Requirement: 切换为有版本约束的维护操作

系统 SHALL 在切换驱动前阻止新准入并排空执行与刷新，通过事务原子提交 binding 与必要凭据迁移。

#### Scenario: 兼容切换成功

- GIVEN 原生与增强驱动声明兼容相同 OpenAI 凭据结构
- WHEN 管理员用当前 binding revision 发起切换且校验成功
- THEN 保留账号 ID、分组、代理、并发、倍率与历史记录
- AND 旧连接及续接句柄失效，新请求使用新绑定

#### Scenario: 切换期间并发修改

- GIVEN 切换任务准备的账号凭据或 binding 已改变
- WHEN 尝试提交切换事务
- THEN 拒绝过期结果，不覆盖并发修改
- AND 恢复到可确认的一致状态并留下脱敏审计

#### Scenario: 维护任务中断

- GIVEN 账号因切换而阻止新准入
- WHEN 主进程重启或任务异常中断
- THEN 根据持久意图恢复或撤销维护任务
- AND 不永久遗留暂停、双绑定或两套刷新 owner

### Requirement: 功能支持按能力验收

系统 SHALL 单独声明 HTTP/SSE、WS、Images、Search、compact 与其他管理操作的支持，不由 Responses 支持自动推导全部端点支持。

#### Scenario: 不支持的协议

- GIVEN 驱动只通过 HTTP/SSE 验收
- WHEN 配置 WS 优先或不支持的端点
- THEN 提供明确不可用原因
- AND 只有已声明并验收的转换路径可以执行

### Requirement: 独立版本与受控更新

系统 SHALL 固定驱动来源 commit、合同版本与能力集合，保留旧代次执行寿命并检查设置/凭据兼容性。

#### Scenario: 驱动内部协议更新

- GIVEN CPR 的变更不需要新增宿主概念
- WHEN 驱动吸收并通过合同与协议回归
- THEN 更新驱动包与版本锁，无需修改用户/支付/订阅或重排账号页面

#### Scenario: 不兼容回滚

- GIVEN 新版本持久化了旧包不能解释的凭据或设置
- WHEN 管理员请求回滚
- THEN 拒绝直接切换或要求经过验证的逆向迁移
- AND 不承诺仅恢复旧二进制即可恢复可用性
