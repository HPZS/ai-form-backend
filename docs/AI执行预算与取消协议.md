# AI 执行预算与取消协议

2026-09-14，开发中，尚未发布。对应插件 `docs/AI自动适配执行与成本优化方案.md`。本文件描述当前已接入的账号网关协议，完整工作流和最终预算配置的验收仍按插件专项推进。产品只支持账号模式，插件须明确拒绝遗留 BYOK 直连入口，不能留下绕过网关的通道。

`GET /v1/about` 增加 `aiExecutionProtocolVersion: 1`，`apiRevision` 为 22，计费协议仍为 2。新插件在启动模型调用前检查此能力；历史同 ID 请求的摘要与账务规则保留。

## 模型请求与执行预算

- 公共请求可携带 `workId`（UUID）及 `recoveryGroupId`（最多 128 字符）。缺少 workId 时按 taskId、再按 requestId 归属；不同账户始终隔离。客户端必须保证同一工作不因恢复或子循环而换身份。
- 每次上游尝试在用户行锁事务内预留次数、token 和等待额度。默认工作上限 32 次、256,000 token、180 秒累计等待；同恢复组包含首次、协议修复、切换上游及外层修复，合计上限 2 次。这些是当前开发验证值，需完整成功样本校准。
- 未知 usage 保留保守 token 预留，已返回用量核销；不能把错误回执中的 0 解释成无成本。仍记录 `costStatus: unknown`，没有可靠账单时不推算确定金额。
- 每条 `usage_details` 增加 `dispatch`：`not-dispatched` 表示进入 HTTP 传输前已确认未发送，释放次数、恢复组及 token 预留；`dispatched` 表示取得完整请求写入或上游响应证据；`unknown` 表示进入传输但是否发送未确认，保留额度。预算 `attempts` 包含仍需占额的预留/未知尝试，不是精确的真实调用统计。
- `modelCalls` 仅在全部逐次分发可确定时返回确切值；未知分发或缺少分发事实的历史失败返回未知。历史 `responded` 可以证明调用，确认未发送计 0；已发送但无 usage 计 1 且 `usageComplete: false`。新增字段位于 JSON 用量记录内，无须重写历史数据。
- `AI_RECOVERY_BUDGET_EXHAUSTED` / `AI_TASK_BUDGET_EXHAUSTED` 为 409，返回 `executionBudget`，包含当前工作、消耗、预留、上限、恢复组及扩额修订号。未分发的首次超额请求也保留可查询预算，模型尝试与消耗为 0。
- `GET /v1/billing/ai-budgets/:id` 查询当前账户该 workId 的额度。`POST /v1/billing/ai-budgets/:id/extend` 要求 UUID `requestId`、`expectedRevision`、可选 `groupId` 及 `confirmation: "increase-ai-budget"`。每轮增补默认工作额度，指定恢复组加 2 次，最多 3 轮；已有消耗不清零。历史扩额身份保留，重复提交不重复扩额，内容改变返回冲突。

## 上游生成设置与用量明细

管理台上游配置新增 `thinkingMode`（空值沿用服务默认，`enabled`/`disabled` 明确发送 `enable_thinking`）和 `tokenLimitParameter`（空值或 `max_tokens` 沿用原输出参数，`max_completion_tokens` 使用总输出参数）。输出上限数值继续由已有能力配置决定，不允许随意注入其他请求字段。设置对该上游承接的所有能力生效，需核对实际服务及各模型的支持范围、质量与计量结果后启用；不按域名或模型名称自动切换。更新请求省略字段保留旧设置，空字符串明确恢复默认；错误值拒绝保存，运行时错误配置零分发且不切换上游绕过。

`usage_details` 保存实际尝试采用的 `thinkingMode`、`tokenLimitParameter`、`maxOutputTokens`。上游返回有效 `completion_tokens_details.reasoning_tokens` 时保存为可选 `reasoningTokens`，它是输出总量的组成部分，不再次加入预算；缺失或 null 保持未知，明确的 0 保留。非法明细标记 `reasoningTokensInvalid` 并记录技术日志，不因可选明细异常重新调用已有有效回答的请求。不保存内部推理原文。

[阿里云兼容接口文档](https://help.aliyun.com/zh/model-studio/qwen-api-via-openai-chat-completions) 说明部分模型的 `max_tokens` 只限制最终回答，`max_completion_tokens` 覆盖推理与回答；具体适用模型及误差以实际服务为准。不能将文档对百炼接口的说明直接等同于任意 MAAS 端点保证。参数配置及请求成功本身不证明输出上界得到执行，隔离测试仍须验证使用量、截断和结果质量。

迁移只新增 `ai_upstreams.thinking_mode`、`token_limit_parameter` 两列，旧配置默认空值，不改变密钥、启停、优先级、模型和价格。生产是否启用新设置须在发布记录中分别注明。

## 显式取消与恢复

`POST /v1/billing/requests/:id/cancel`，body 为原 `taskId`。接口受现有账号鉴权保护，并核对请求/任务归属。

| 响应 | 含义 |
| --- | --- |
| 202 / `cancel-requested` | 取消意图已持久保存，可能仍有在途请求，不能宣称供应商已停止计费 |
| 200 / `cancelled` | 未受理请求已被取消登记阻止，或原请求已形成取消终态 |
| 200 / `completed` | 原执行先完成或已成为其他终态；原结果和用量保留，不因取消重写账务 |

调用方在回执丢失、扩展后台或服务重启后重试同一取消身份。普通 HTTP 断连不构成取消，仍沿原请求 ID 查询/恢复，不创建新的未知付费请求。取消传播到当前进程与持久状态观察器，并在新的上游尝试前检查；跨进程及分发许可竞争的完整整合验收仍未完成。

失败响应保留具体上游/协议/取消/预算错误码与失败账务回执。对已确认失败的请求无需通用 HTTP 层自动重复发送；历史缺少失败码时返回通用失败，不误报为程序协议错误。租约过期且存在未核实尝试时记录 `AI_EXECUTION_UNCERTAIN`；恢复不能抹掉未知 usage 或盲目重新推理。

## 迁移、验证与发布

字段单步请求新增可选 `adapterLearningAvailable`。只有宿主明确开放、目标为 `set-field-value` 且没有程序接管续态时，`agent_step` 才能返回 `need-adapter`；该状态只请求局部候选学习，不夹带网页动作或完成结论。正式提示词当前为 v10，按请求能力条件展示此选项，旧客户端不接收新状态。编译仍经过原 `compile_adapter` 网关、计费及预算；此变更不使每个字段必须先学习。

单步及行计划请求可带 `readOnlyNodeIds`，必须属于本次已投影节点。插件从当前原生控件事实生成集合，不解析页面标签中的 readonly 字样；后端拒绝对这些节点生成 replace-text，仍允许打开控件。错误动作进入原请求既有的有界协议修复，不能开独立重试额度。插件在实际分发前继续检查实时 DOM；旧请求没有该可选集合时继续依赖原客户端保护，不把服务端候选当作操作授权。

新增 `ai_request_cancellations`、`ai_execution_budgets` 两张表；`ai_requests` 增加 `work_id`、`recovery_group_id`、`failure_code`。预算表保留恢复组累计和每次扩额身份，逐次 usage 保留用量完整性与预留信息。历史请求、结果、单价、预占、流水和订阅不重写。

已通过 `go test ./...`、`go build ./...`，并在独立本机 PostgreSQL 16 库运行 `TestPostgresExecutionBudgetMigrationAndConcurrency`：重复迁移保留既有请求与自定义价格，最后一次额度只允许一个并发请求调用上游。测试 schema `execution_1789357451264109600` 保留于专用 `aiform_execution_test` 数据库；它不是生产迁移证明。

部署仍按 [部署指南](部署指南.md)：验收后在开发机构建并推送固定版本镜像，服务器仅 pull。部署前备份数据库、环境、Compose、提示词和旧镜像；保存原账户、价格、请求与流水的历史范围摘要。部署后检查服务健康、API22/执行1/计费2、原范围数据摘要、旧请求恢复和仅程序策略，最后记录实际镜像 digest。当前相关提示词为 `agent_step` v10、`agent_row_plan` v3、`agent_task_step` v6、`compile_adapter` v4，发布时必须同步挂载提示词并核对摘要，不能只更换应用镜像。

未取得完整冷启动、换值零 AI、局部修复和取消竞态证据前，不将本协议文档标为全部优化验收完成。
