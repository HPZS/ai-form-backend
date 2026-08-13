# ai-form-backend

AI 智能录入助手（浏览器插件）的用户与计费后端：账号、订阅积分、易支付、统一 AI 网关（12 个业务能力接口）。

架构与状态机规格见插件仓库 `docs/用户系统后端技术方案.md`（v3）。本服务按"搭积木"方式建设：账号/订阅/易支付/邮件的设计与部分实现取自 [new-api](https://github.com/QuantumNous/new-api)，AI 网关、积分流水、任务预占、请求幂等为本项目自建。

## 功能

- 邮箱验证码注册登录，JWT + Refresh Token 旋转（family 泄露检测、并发宽限）
- 订阅套餐 = 月度额度桶，到期清零不结转；试用桶注册自动发放；后台可改价
- 12 个 AI 能力接口：服务端提示词（私有配置）、按能力扣积分（价格快照）、请求幂等、任务预占
- AI 上游：多个 OpenAI 兼容端点按序故障切换 + 冷却熔断
- 易支付下单/异步通知验签/订单幂等发桶

## 运行

```bash
go build ./...
cp config/ai.example.yaml config/private/ai.yaml   # 填入真实上游与模型
cp .env.example .env                               # 填入数据库/密钥/SMTP/易支付
go run .
```

必需环境变量见 `.env.example`。提示词目录通过 `PROMPTS_DIR` 指定（默认 `prompts/private/`，仓库仅含 `prompts/example/` 占位示例）。

## 许可证

本项目以 [AGPL-3.0](./LICENSE) 授权。部分模块移植自 [new-api](https://github.com/QuantumNous/new-api)（AGPL-3.0，基于 [One API](https://github.com/songquanpeng/one-api) MIT 开发）。

Frontend design and development by New API contributors.

依 AGPL-3.0 第 13 条，通过网络使用本服务的用户可从本仓库获取对应源码。详见 [NOTICE.md](./NOTICE.md)。
