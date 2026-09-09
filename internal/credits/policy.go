package credits

import (
	"errors"
	"fmt"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
	"gorm.io/gorm"
)

const PolicyV2 = "per-call-v2"
const ModeIncluded = "included"
const ModePerCall = "per_call"
const SettlementPending = "settlement_pending"
const RequestFailed = "failed"

var (
	ErrCapabilityDisabled = errors.New("本次报价包含已停用的能力")
	ErrRefundAmount       = errors.New("返还超过原实扣")
	ErrBudget             = errors.New("已达到本任务消费上限")
	ErrAuthorization      = errors.New("需要确认本任务费用")
	ErrQuoteExpired       = errors.New("报价或价格授权已过期，请重新确认")
	ErrConfig             = errors.New("能力计费配置不完整或不一致")
	ErrConflict           = errors.New("请求身份与原始内容不一致")
	ErrFundsRevoked       = errors.New("已预留积分被作废，需要核对账务")
)

func DefaultMode(capability string) string {
	if capability == "match_columns" || capability == "generate_field" {
		return ModePerCall
	}
	return ModeIncluded
}

func ValidPrice(p model.CapabilityPrice) bool {
	return p.PriceVersion > 0 && ((p.BillingMode == ModeIncluded && p.Credits == 0) || (p.BillingMode == ModePerCall && p.Credits > 0 && p.Credits <= 1_000_000))
}

type Price = model.BillingPrice

func Prices(db *gorm.DB) (map[string]Price, error) {
	var rows []model.CapabilityPrice
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]Price{}
	for _, p := range rows {
		if !ValidPrice(p) {
			return nil, fmt.Errorf("%w: %s", ErrConfig, p.Capability)
		}
		out[p.Capability] = Price{p.Credits, p.PriceVersion}
	}
	if len(out) == 0 {
		return nil, ErrConfig
	}
	return out, nil
}

func HasSubscription(db *gorm.DB, userID int64) (bool, error) {
	var count int64
	now := time.Now()
	err := db.Model(&model.UserSubscription{}).Where("user_id = ? AND status = ? AND starts_at <= ? AND ends_at > ? AND plan_type IN ?", userID, model.SubStatusActive, now, now, []string{model.PlanTypeBase, model.PlanTypeTrial}).Count(&count).Error
	return count > 0, err
}
