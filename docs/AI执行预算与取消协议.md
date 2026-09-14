# AI 执行预算与取消协议

2026-09-14，开发中，尚未发布。对应插件 `docs/AI自动适配执行与成本优化方案.md`。本文件描述当前已接入的账号网关协议，完整工作流、BYOK 和最终预算配置的验收仍按插件专项推进。

`GET /v1/about` 增加 `aiExecutionProtocolVersion: 1`，`apiRevision` 为 22，计费协议仍为 2。新插件在启动模型调用前检查此能力；历史同 ID 请求的摘要与账务规则保留。

## 模型请求与执行预算

- 公共请求可携带 `workId`（UUID）及 `recoveryGroupId`（最多 128 字符）。缺少 workId 时按 taskId、再按 requestId 归属；不同账户始终隔离。客户端必须保证同一工作不因恢复或子循环而换身份。
- 每次上游尝试在用户行锁事务内预留次数、token 和等待额度。默认工作上限 32 次、256,000 token、180 秒累计等待；同恢复组包含首次、协议修复、切换上游及外层修复，合计上限 2 次。这些是当前开发验证值，需完整成功样本校准。
- 未知 usage 保留保守 token 预留，已返回用量核销；不能把错误回执中的 0 解释成无成本。仍记录 `costStatus: unknown`，没有可靠账单时不推算确定金额。
- `AI_RECOVERY_BUDGET_EXHAUSTED` / `AI_TASK_BUDGET_EXHAUSTED` 为 409，返回 `executionBudget`，包含当前工作、消耗、预留、上限、恢复组及扩额修订号。未分发的首次超额请求也保留可查询预算，模型尝试与消耗为 0。
- `GET /v1/billing/ai-budgets/:id` 查询当前账户该 workId 的额度。`POST /v1/billing/ai-budgets/:id/extend` 要求 UUID `requestId`、`expectedRevision`、可选 `groupId` 及 `confirmation: "increase-ai-budget"`。每轮增补默认工作额度，指定恢复组加 2 次，最多 3 轮；已有消耗不清零。历史扩额身份保留，重复提交不重复扩额，内容改变返回冲突。

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

新增 `ai_request_cancellations`、`ai_execution_budgets` 两张表；`ai_requests` 增加 `work_id`、`recovery_group_id`、`failure_code`。预算表保留恢复组累计和每次扩额身份，逐次 usage 保留用量完整性与预留信息。历史请求、结果、单价、预占、流水和订阅不重写。

已通过 `go test ./...`、`go build ./...`，并在独立本机 PostgreSQL 16 库运行 `TestPostgresExecutionBudgetMigrationAndConcurrency`：重复迁移保留既有请求与自定义价格，最后一次额度只允许一个并发请求调用上游。测试 schema `execution_1789357451264109600` 保留于专用 `aiform_execution_test` 数据库；它不是生产迁移证明。

部署仍按 [部署指南](部署指南.md)：验收后在开发机构建并推送固定版本镜像，服务器仅 pull。部署前备份数据库、环境、Compose、提示词和旧镜像；保存原账户、价格、请求与流水的历史范围摘要。部署后检查服务健康、API22/执行1/计费2、原范围数据摘要、旧请求恢复和仅程序策略，最后记录实际镜像 digest。提示词当前未改动，若后续修改则按原流程备份、同步并核对摘要。

未取得完整冷启动、换值零 AI、局部修复和取消竞态证据前，不将本协议文档标为全部优化验收完成。
