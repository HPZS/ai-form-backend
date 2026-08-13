// ai-form-backend - AGPL-3.0
// 数据模型。订阅表设计参考 new-api 的 SubscriptionPlan/UserSubscription;
// 积分流水/预占/AI 请求为本项目自建(规格见技术方案 v3 第五、九节)。
package model

import (
	"time"
)

// ===== 用户与认证 =====

type User struct {
	ID           int64  `gorm:"primaryKey"`
	Email        string `gorm:"uniqueIndex;size:255;not null"` // 入库前 trim+lower
	PasswordHash string `gorm:"size:100;not null;default:''"`  // bcrypt;空=未设密码(只能验证码登录)
	Role         string `gorm:"size:16;not null;default:user"` // user | admin
	Status       string `gorm:"size:16;not null;default:active"`
	CreatedAt    time.Time
}

// EmailCode 邮箱验证码。code_hash = HMAC-SHA256(code, pepper),同码验错 5 次作废。
type EmailCode struct {
	ID        int64  `gorm:"primaryKey"`
	Email     string `gorm:"index;size:255;not null"`
	CodeHash  string `gorm:"size:64;not null"`
	Attempts  int    `gorm:"not null;default:0"`
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// SsoCode 一次性登录码:插件已登录 → 换码打开网页控制台免登录。
// 60 秒有效、单次使用、HMAC(pepper) 存储;经 URL fragment 传递不进服务端日志。
type SsoCode struct {
	ID        int64  `gorm:"primaryKey"`
	UserID    int64  `gorm:"index;not null"`
	CodeHash  string `gorm:"uniqueIndex;size:64;not null"`
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// RefreshToken 旋转制:旧记录不删,revoked_at + replaced_by 留作泄露检测。
type RefreshToken struct {
	ID         string `gorm:"primaryKey;size:36"`
	UserID     int64  `gorm:"index;not null"`
	TokenHash  string `gorm:"uniqueIndex;size:64;not null"` // SHA-256
	FamilyID   string `gorm:"index;size:36;not null"`
	ReplacedBy string `gorm:"size:36"`
	RevokedAt  *time.Time
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

// ===== 订阅(设计取自 new-api) =====

type SubscriptionPlan struct {
	ID           int64  `gorm:"primaryKey"`
	Name         string `gorm:"size:64;not null"`
	PriceCents   int64  `gorm:"not null"` // 人民币分,9.90 元 = 990
	TotalCredits int64  `gorm:"not null"` // 套餐总积分
	DurationDays int    `gorm:"not null"` // 30;试用 14
	ForSale      bool   `gorm:"not null;default:true"`
	Trial        bool   `gorm:"not null;default:false"` // 试用套餐:注册自动发放,不可购买
	SortOrder    int    `gorm:"not null;default:0"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const (
	SubStatusActive  = "active"
	SubStatusExpired = "expired"
	SubStatusRevoked = "revoked"
)

// UserSubscription 一笔购买 = 一个独立的月度额度桶,到期清零不结转。
// 历史 AmountTotal/AmountUsed 永不改写,过期只改 status。
type UserSubscription struct {
	ID          int64  `gorm:"primaryKey"`
	UserID      int64  `gorm:"index;not null"`
	PlanID      int64  `gorm:"not null"`
	OrderID     *int64 // 试用桶为空
	AmountTotal int64  `gorm:"not null"`
	AmountUsed  int64  `gorm:"not null;default:0"`
	StartsAt    time.Time
	EndsAt      time.Time `gorm:"index"`
	Status      string    `gorm:"size:16;not null;default:active"`
	CreatedAt   time.Time
}

const (
	OrderStatusPending = "pending"
	OrderStatusPaid    = "paid"
	OrderStatusExpired = "expired"
)

// PaymentOrder 易支付订单。回调按 trade_no 幂等落账。
type PaymentOrder struct {
	ID         int64  `gorm:"primaryKey"`
	TradeNo    string `gorm:"uniqueIndex;size:64;not null"`
	UserID     int64  `gorm:"index;not null"`
	PlanID     int64  `gorm:"not null"`
	AmountCents int64 `gorm:"not null"`
	Method     string `gorm:"size:32"` // alipay / wxpay ...
	Status     string `gorm:"size:16;not null;default:pending"`
	NotifyRaw  string `gorm:"type:text"` // 验签通过的回调参数快照
	PaidAt     *time.Time
	CreatedAt  time.Time
}

// ===== 积分(自建) =====

// AIDefault 全局 AI 默认参数(单行,ID 恒为 1):能力未单独覆盖时统一用这里的值。
type AIDefault struct {
	ID          int64   `gorm:"primaryKey"`
	Model       string  `gorm:"size:64;not null;default:''"` // 所有上游统一 OpenAI 格式,模型名跨上游通用
	Temperature float64 `gorm:"not null;default:0"`
	MaxTokens   int     `gorm:"not null;default:4000"`
	UpdatedAt   time.Time
}

// CapabilityPrice 能力配置:计费单价 + 可选的模型参数覆盖,管理台可改、即时生效。
// Model 为空、Temperature/MaxTokens 为 NULL 时用 AIDefault 的全局默认。
type CapabilityPrice struct {
	Capability  string   `gorm:"primaryKey;size:32"`
	Credits     int64    `gorm:"not null"`
	Enabled     bool     `gorm:"not null;default:true"`
	Model       string   `gorm:"size:64;not null;default:''"`
	Temperature *float64 `gorm:"column:temperature_override"`
	MaxTokens   *int     `gorm:"column:max_tokens_override"`
	UpdatedAt   time.Time
}

// AIUpstream OpenAI 兼容上游,管理台增删改;故障切换按 sort_order 升序尝试。
// 模型密钥常换,放库里管理台即改即生效(可随时吊销轮换,风险与 JWT/pepper 不同类)。
type AIUpstream struct {
	ID        int64  `gorm:"primaryKey"`
	Name      string `gorm:"size:64;not null"`
	BaseURL   string `gorm:"size:255;not null"`
	APIKey    string `gorm:"size:255;not null"`
	Enabled   bool   `gorm:"not null;default:true"`
	SortOrder int    `gorm:"not null;default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreditLedger 只增不改的积分流水。负 delta=消耗,正 delta=冲正。
type CreditLedger struct {
	ID             int64  `gorm:"primaryKey"`
	UserID         int64  `gorm:"index;not null"`
	SubscriptionID int64  `gorm:"not null"` // 扣到哪个桶
	RequestID      string `gorm:"index;size:36"`
	BillingGroupID string `gorm:"size:36"` // 表单识别计费组;部分唯一索引防重
	Capability     string `gorm:"size:32"`
	Delta          int64  `gorm:"not null"`
	PriceSnapshot  int64  // 扣费时刻的能力单价
	BalanceAfter   int64  `gorm:"not null"` // 扣费后 available 快照
	CreatedAt      time.Time
}

const (
	HoldStatusOpen    = "open"
	HoldStatusSettled = "settled"
	HoldStatusExpired = "expired"
)

// CreditHold 任务预占。available = 桶剩余合计 - Σ max(amount-consumed,0)(open)。
type CreditHold struct {
	ID        string `gorm:"primaryKey;size:36"`
	UserID    int64  `gorm:"index;not null"`
	TaskID    string `gorm:"size:36;not null"`
	Amount    int64  `gorm:"not null"`
	Consumed  int64  `gorm:"not null;default:0"`
	Status    string `gorm:"size:16;not null;default:open"`
	ExpiresAt time.Time `gorm:"index"` // 心跳续期;定时任务过期释放
	CreatedAt time.Time
	SettledAt *time.Time
}

// ===== AI 请求(幂等闸门 + 计量;不含业务原文,response_cache 除外且 24h 清除) =====

const (
	AIReqPending       = "pending"
	AIReqOK            = "ok"
	AIReqInvalid       = "ai_invalid"
	AIReqUpstreamError = "upstream_error"
)

type AIRequest struct {
	ID             int64  `gorm:"primaryKey"`
	UserID         int64  `gorm:"index;not null"`
	RequestID      string `gorm:"size:36;not null"` // UNIQUE(user_id, request_id) 迁移时建
	TaskID         string `gorm:"size:36"`
	BillingGroupID string `gorm:"size:36"`
	Capability     string `gorm:"size:32;not null"`
	Upstream       string `gorm:"size:32"`
	Model          string `gorm:"size:64"`
	PromptVersion  string `gorm:"size:16"`
	SchemaVersion  string `gorm:"size:16"`
	InputTokens    int
	OutputTokens   int
	CostMicros     int64 // 人民币微元
	Credits        int64
	Status         string `gorm:"size:16;not null;default:pending"`
	LeaseExpiresAt time.Time
	LatencyMs      int
	ResponseCache  string `gorm:"type:text"` // 幂等重放用,定时清除
	CreatedAt      time.Time `gorm:"index"`
}

// TaskMetric 插件任务结束上报的统计,不含业务原文。
type TaskMetric struct {
	ID            int64  `gorm:"primaryKey"`
	UserID        int64  `gorm:"index;not null"`
	TaskID        string `gorm:"size:36;not null"` // UNIQUE(user_id, task_id) 迁移时建
	TotalRows     int
	Succeeded     int
	Failed        int
	Unknown       int
	Skipped       int
	ReusedForm    bool
	ReusedMapping bool
	AICells       int
	Outcome       string `gorm:"size:16"` // completed | abandoned | interrupted
	DurationMs    int64
	SchemaVersion string `gorm:"size:16"`
	CreatedAt     time.Time
}
