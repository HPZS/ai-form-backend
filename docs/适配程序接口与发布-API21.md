# 适配程序接口与发布（API21）

本次基于 `54ced2c` / API20 增加受限适配程序编译，不替换现有计费 v2。API21 已按用户后续授权上线，发布核验见文末；浏览器真实适配全流程和用户现场验收仍待完成。

- `POST /v1/ai/compile-adapter`：`mode=create|repair`。请求结构见 `internal/ai/adapter.go`，正式提示词 `prompts/private/compile_adapter.yaml` v3。每次生成绑定 compilationId、baseRevision（无基线为 null）、sourceContractDigest、formContractDigest、userInstructionRevision、runtimeVersion、toolVersion；响应必须原样回显。v3 明确工具的 satisfied/changed/failed/unknown/ready 及移交状态，防止把只读 ready 当作失败或业务完成。
- `schemaVersion=1`、`runtimeVersion=adapter-runtime-v1`。请求传当前目标、局部 scope/sourceContract/observation、参数定义、相关旧模块、失败上下文及全部 11 项执行预算。响应只含候选 modules/queries/effects/entrypoints/aiHandoffs、诊断及待验证条件；禁止 coverage、active 状态和成功回执。浏览器侧受限 AST、单次决策、实际回读与条件发布才决定候选是否能使用。
- `/v1/about` 返回 `apiRevision=21`、`adapterProtocolVersion=1`、`adapterRuntimeVersions=["adapter-runtime-v1"]`。既有计费/交互协议保持 2/1。验证码、密码、SSO 登录的 `user.userId` 与 `/v1/me.userId` 都为稳定十进制字符串，插件还应绑定后端地址以隔离不同服务主体。
- 所有 AI 请求可携带 `modelPolicy=adaptive|deterministic-only`；仅程序策略在模型和账务准入前返回 HTTP409、`ai-required`。旧客户端省略该字段时继续现有行为。
- `callPhase=source-analysis|runtime|adapter-create|adapter-repair|runtime-handoff`；旧客户端可省略阶段。运行时移交必须携带 handoffId，编译阶段必须与 mode 一致。阶段、移交和编译身份写入 AIRequest；响应 meta 与请求账务查询返回真实 modelCalls（含上游和协议修复尝试）。历史无逐次用量记录返回缺失/未知，不伪造零次。
- `agent-step` / `agent-task-step` 可携带 `runtimeHandoff`，描述固定程序版本、模块及具体子目标；必须与 runtime-handoff 阶段和一次性 handoffId 绑定，不增加工具、来源或节点权限。正式提示词分别为 v7/v4；效果和合法后继由客户端宿主核验。仅字段移交可声明 `selection`，模型只返回候选节点和理由；宿主重新读取候选值，独立核验选择结果后形成限于本记录的派生期望。整表程序经字段工具使用该能力。
- `compile_input` / `repair_input_plan` 新可选 `structureLearning=literal-fields-v1` 仅用于文本；旧客户端省略字段保持兼容。正式提示词均为 v3，额外返回有限字面标签/记录界符/字段范围的 `textStructure`，无法证明边界时必须返回具体 `structureDiagnostic`，当轮语义引用仍保留。候选不含业务值、旧引用、源码或正则；浏览器逐块覆盖当前原文并核对当轮语义引用后，才可保存为可复用结构。Word 跨段落模板、混合表格图片及自由文本不据此宣称可复用。
- `cmd/task-agent-eval` 返回生产请求的 `meta.requestId` 与真实 `modelCalls`，用于将前端待完成请求和实际模型次数关联；本地 CLI 不接触生产业务账户或账务。

新能力独立播种 `included/0/priceVersion=1`，模型为空时继承现有全局默认，temperature=0、maxTokens=16000。它不复制 `generate_rule` 的管理员覆盖。已有能力模式、单价、模型与历史账务不改；管理员后续可按现有 v2 配置新能力价格。

统一 Spec 协议修复回调供生产 v2、历史网关和 `cmd/task-agent-eval` 使用。新语义生成/局部修复使用新 requestId，技术重试保持原 ID。既有 80 秒执行截止、90 秒租约、HMAC 请求摘要、实际用量、结算恢复全部保留。准入前取消不执行；准入后即使客户端停止等待，也继续生成并结算原请求，客户端不得据此发布已过期候选。

发布前运行 `go test ./...` 与 `go build ./...`。新增回归覆盖严格绑定/坏引用/父查询环/资源限制、默认配置隔离、协议修复和用量、同 ID 重放/冲突、动态收费准入、失败不收费、稳定用户身份与保留旧在途请求的增量迁移。真实模型评测继续使用 `cmd/task-agent-eval` 的 `capability=compile_adapter`，必须连同浏览器冻结底座、不同值回放和独立结果证据记录，不能把接口返回候选当作业务成功。

发布执行顺序：

1. 备份生产数据库、旧镜像标识、Compose/环境配置和全部挂载提示词，记录既有价格目录与历史账务摘要。
2. 仅在开发机构建并推送提交标签和 digest；服务器只拉取。同步全部新增和修改提示词，核对 24 份配置及提示词 SHA256，不能只更新镜像。
3. 启动时仅新增三项 AIRequest 关联列并补齐新能力配置；不清理旧任务、预占或 settlement_pending。核验健康状态、API21/1/2 能力协商、旧请求可恢复及价格目录仅新增一行。
4. 先确认后端可用，再发布依赖 API21 的插件；使用已授权验收记录检查编译、局部修复、仅程序模式拒绝和同 ID 恢复。回退只切回旧镜像及提示词，保留增量列和所有账务事实。

生产部署与真实用户页面验证由统一发布任务记录具体提交、镜像摘要、验收结果及未覆盖边界。

2026-09-12 本机发布预检：`6f7787a` 镜像通过 PostgreSQL 16 专用库增量迁移、API21/1/2 协商、旧格式登录凭证保留邮箱/角色并返回稳定 userId、新能力 included/0/版本1/全局模型及仅程序请求 HTTP409 验证；仅程序请求未建立 AIRequest。证据 `.artifacts/adapter-api21-final-20260912-readiness.json`，本次测试容器及其卷、网络已清理。该镜像未推送、未部署，后续提示词修正仍需新冻结验收。

真实浏览器运行发现旧评测 CLI 每次尝试 90 秒与生产整链 80 秒不一致；现已共享 `ai.CallChainTimeout`，协议修复不重置截止。此前 CLI 结果只作为探索证据，不能据此宣称线上时限内通过。生产截止和计费租约不变。

最新代码 `a045e6d` 已通过全量 Go 测试和构建；开发机镜像 `hupeng666/form-backend:a045e6d` 构建完成，镜像索引 ID 为 `sha256:e069ece0488ae555624fb9797cf900a3f9d6b5c4e855ddbc2482ca7cd52fcad2`，尚未推送。配套插件 b395564 / 编译提示词 v3 的动态候选与 Ant Design Vue 真实验收均在编译请求遇到 502 或 EOF，保存次数为 0，完整记录见插件 `docs/适配程序真实浏览器验收.md`。本次未部署，生产只读复核仍为 API20/计费2、app running、PostgreSQL healthy；需真实全流程验收通过后再发布。

### 后续用户授权发布结果

2026-09-12 用户明确要求“push + 部署，我去验证”，覆盖前述发布时序条件。两仓 main 已 push，提交标签 a045e6d 和 latest 已推送 Docker Hub，服务器只拉取同一 digest；当前已部署 API21，运行配置镜像 ID `sha256:bc37b6e08636542e4b0263467f5443073e35f577e08f92eed3765c6200e4bd30`。公网能力、旧 JWT claims 格式凭证、稳定 userId、原邮箱/角色、仅程序409且零AIRequest均核验通过，证据 `.artifacts/adapter-api21-live-20260912.json`；没有调用上游模型，凭据未落盘。

完整备份 `/opt/ai-form-backend/releases/20260912-api21-a045e6d/` 含数据库dump及目录校验、旧镜像身份、Compose/环境和原提示词。部署前后原182条流水完整摘要、1560条请求身份/价快照摘要和23项原能力配置摘要均相同；新能力 included/0/版本1/全局模型，24份提示词SHA256与开发机一致。新增提示词首次复制为0600导致容器不可读，已改为公开提示词所需0644，重启后正常；当前 app running、重启计数0。此处记录发布成功，真实AI全流程和用户现场验收仍待完成。
