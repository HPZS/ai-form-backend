package credits

import (
	"fmt"
	"strings"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Refund 只通过关联流水返还，不改原消费；同一返还 ID 的重放必须保持请求和金额一致。
func Refund(db *gorm.DB, userID, actorID int64, requestID, refundID, reason string, amount int64) error {
	if _, err := uuid.Parse(refundID); err != nil {
		return fmt.Errorf("返还 ID 必须是 UUID")
	}
	if amount <= 0 || len(strings.TrimSpace(reason)) == 0 || len(reason) > 300 {
		return fmt.Errorf("返还金额或原因不合法")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, userID); err != nil {
			return err
		}
		var repeated []model.CreditLedger
		if err := tx.Where("user_id = ? AND refund_id = ?", userID, refundID).Find(&repeated).Error; err != nil {
			return err
		}
		if len(repeated) > 0 {
			var total int64
			for _, r := range repeated {
				if r.RefundOfRequestID != requestID || r.Reason != reason {
					return ErrConflict
				}
				total += r.Delta
			}
			if total != amount {
				return ErrConflict
			}
			return nil
		}
		var original []model.CreditLedger
		if err := tx.Where("user_id = ? AND request_id = ? AND delta < 0", userID, requestID).Order("id asc").Find(&original).Error; err != nil {
			return err
		}
		if len(original) == 0 {
			return gorm.ErrRecordNotFound
		}
		var previous []model.CreditLedger
		if err := tx.Where("user_id = ? AND refund_of_request_id = ? AND delta > 0", userID, requestID).Find(&previous).Error; err != nil {
			return err
		}
		var spent, returned int64
		for _, d := range original {
			spent -= d.Delta
		}
		for _, r := range previous {
			returned += r.Delta
		}
		if amount > spent-returned {
			return ErrRefundAmount
		}
		// 原分桶先后顺序固定；累计返还按该顺序消耗原扣款，不依赖补偿桶的新 ID。
		skip := returned
		for _, debit := range original {
			refundable := -debit.Delta
			taken := min(skip, refundable)
			refundable -= taken
			skip -= taken
			take := min(amount, refundable)
			if take == 0 {
				continue
			}
			var bucket model.UserSubscription
			if err := tx.Where("id = ? AND user_id = ?", debit.SubscriptionID, userID).First(&bucket).Error; err != nil {
				return err
			}
			if bucket.Status == model.SubStatusActive && time.Now().Before(bucket.EndsAt) {
				if bucket.AmountUsed < take {
					return fmt.Errorf("原桶已使用额度与返还不符")
				}
				if err := tx.Model(&bucket).Update("amount_used", gorm.Expr("amount_used - ?", take)).Error; err != nil {
					return err
				}
			} else {
				bucket = model.UserSubscription{UserID: userID, PlanType: model.PlanTypeBonus, AmountTotal: take, StartsAt: time.Now(), EndsAt: time.Now().Add(30 * 24 * time.Hour), Status: model.SubStatusActive}
				if err := tx.Create(&bucket).Error; err != nil {
					return err
				}
			}
			balance, err := Available(tx, userID)
			if err != nil {
				return err
			}
			row := model.CreditLedger{UserID: userID, SubscriptionID: bucket.ID, TaskID: debit.TaskID, Capability: debit.Capability, PolicyVersion: PolicyV2,
				PriceVersion: debit.PriceVersion, PriceSnapshot: debit.PriceSnapshot, RefundID: refundID, RefundOfRequestID: requestID, ActorID: actorID, Reason: reason, Delta: take, BalanceAfter: balance, CreatedAt: time.Now()}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			amount -= take
			if amount == 0 {
				break
			}
		}
		if amount != 0 {
			return fmt.Errorf("返还分桶未完成")
		}
		return nil
	})
}
