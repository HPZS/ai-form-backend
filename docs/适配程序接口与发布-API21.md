# 适配程序接口与发布（API21）

本次基于 `54ced2c` / API20 增加受限适配程序编译，不替换现有计费 v2。本文是代码契约和发布检查，尚不代表生产或浏览器真实适配验收通过。

- `POST /v1/ai/compile-adapter`：`mode=create|repair`。请求结构见 `internal/ai/adapter.go`，正式提示词 `prompts/private/compile_adapter.yaml` v1。每次生成绑定 compilationId、baseRevision（无基线为 null）、sourceContractDigest、formContractDigest、userInstructionRevision、runtimeVersion、toolVersion；响应必须原样回显。
- `schemaVersion=1`、`runtimeVersion=adapter-runtime-v1`。请求传当前目标、局部 scope/sourceContract/observation、参数定义、相关旧模块、失败上下文及全部 11 项执行预算。响应只含候选 modules/queries/effects/entrypoints/aiHandoffs、诊断及待验证条件；禁止 coverage、active 状态和成功回执。浏览器侧受限 AST、单次决策、实际回读与条件发布才决定候选是否能使用。
- `/v1/about` 返回 `apiRevision=21`、`adapterProtocolVersion=1`、`adapterRuntimeVersions=["adapter-runtime-v1"]`。既有计费/交互协议保持 2/1。验证码、密码、SSO 登录的 `user.userId` 与 `/v1/me.userId` 都为稳定十进制字符串，插件还应绑定后端地址以隔离不同服务主体。
- 所有 AI 请求可携带 `modelPolicy=adaptive|deterministic-only`；仅程序策略在模型和账务准入前返回 HTTP409、`ai-required`。旧客户端省略该字段时继续现有行为。
- `callPhase=source-analysis|runtime|adapter-create|adapter-repair|runtime-handoff`；旧客户端可省略阶段。运行时移交必须携带 handoffId，编译阶段必须与 mode 一致。阶段、移交和编译身份写入 AIRequest；响应 meta 与请求账务查询返回真实 modelCalls（含上游和协议修复尝试）。历史无逐次用量记录返回缺失/未知，不伪造零次。

新能力独立播种 `included/0/priceVersion=1`，模型为空时继承现有全局默认，temperature=0、maxTokens=16000。它不复制 `generate_rule` 的管理员覆盖。已有能力模式、单价、模型与历史账务不改；管理员后续可按现有 v2 配置新能力价格。

统一 Spec 协议修复回调供生产 v2、历史网关和 `cmd/task-agent-eval` 使用。新语义生成/局部修复使用新 requestId，技术重试保持原 ID。既有 80 秒执行截止、90 秒租约、HMAC 请求摘要、实际用量、结算恢复全部保留。准入前取消不执行；准入后即使客户端停止等待，也继续生成并结算原请求，客户端不得据此发布已过期候选。

发布前运行 `go test ./...` 与 `go build ./...`。新增回归覆盖严格绑定/坏引用/父查询环/资源限制、默认配置隔离、协议修复和用量、同 ID 重放/冲突、动态收费准入、失败不收费、稳定用户身份与保留旧在途请求的增量迁移。真实模型评测继续使用 `cmd/task-agent-eval` 的 `capability=compile_adapter`，必须连同浏览器冻结底座、不同值回放和独立结果证据记录，不能把接口返回候选当作业务成功。

发布执行顺序：

1. 备份生产数据库、旧镜像标识、Compose/环境配置和全部挂载提示词，记录既有价格目录与历史账务摘要。
2. 仅在开发机构建并推送提交标签和 digest；服务器只拉取。单独同步新增提示词，核对 24 份配置及新提示词 SHA256，不能只更新镜像。
3. 启动时仅新增三项 AIRequest 关联列并补齐新能力配置；不清理旧任务、预占或 settlement_pending。核验健康状态、API21/1/2 能力协商、旧请求可恢复及价格目录仅新增一行。
4. 先确认后端可用，再发布依赖 API21 的插件；使用已授权验收记录检查编译、局部修复、仅程序模式拒绝和同 ID 恢复。回退只切回旧镜像及提示词，保留增量列和所有账务事实。

生产部署与真实用户页面验证由统一发布任务记录具体提交、镜像摘要、验收结果及未覆盖边界。
