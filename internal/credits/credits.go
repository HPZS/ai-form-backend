// ai-form-backend - AGPL-3.0
// 积分核心:跨桶扣费、流水、任务预占。规格见技术方案 v3 §5。
// 所有写路径先 LockUser 锁用户行,同用户操作串行化,公式:
//   桶剩余 remaining = AmountTotal - AmountUsed (active 且未到期)
//   hold 冻结 = max(amount - consumed, 0) (open)
//   available = Σ桶剩余 - Σhold冻结
package credits

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/internal/model"
)

var (
	ErrInsufficient = errors.New("积分不足")
	ErrNoActiveSub  = errors.New("无有效订阅")
)

// activeBuckets 返回未到期、有剩余的桶,按到期时间升序(先扣最早到期的)。
func activeBuckets(tx *gorm.DB, userID int64, now time.Time) ([]model.UserSubscription, error) {
	var subs []model.UserSubscription
	err := tx.Where("user_id = ? AND status = ? AND ends_at > ?", userID, model.SubStatusActive, now).
		Order("ends_at asc").Find(&subs).Error
	return subs, err
}

func bucketsRemaining(subs []model.UserSubscription) int64 {
	var sum int64
	for _, s := range subs {
		if r := s.AmountTotal - s.AmountUsed; r > 0 {
			sum += r
		}
	}
	return sum
}

// holdsReserved 统计 open 预占的剩余冻结额。excludeTaskID 非空时排除该任务自己的 hold
// (本任务的冻结额对自己可用)。
func holdsReserved(tx *gorm.DB, userID int64, now time.Time, excludeTaskID string) (int64, error) {
	var holds []model.CreditHold
	q := tx.Where("user_id = ? AND status = ? AND expires_at > ?", userID, model.HoldStatusOpen, now)
	if excludeTaskID != "" {
		q = q.Where("task_id <> ?", excludeTaskID)
	}
	if err := q.Find(&holds).Error; err != nil {
		return 0, err
	}
	var sum int64
	for _, h := range holds {
		if r := h.Amount - h.Consumed; r > 0 {
			sum += r
		}
	}
	return sum, nil
}

// Available 当前可用积分(只读,不加锁,用于展示)。
func Available(db *gorm.DB, userID int64) (int64, error) {
	now := time.Now()
	var avail int64
	err := db.Transaction(func(tx *gorm.DB) error {
		subs, err := activeBuckets(tx, userID, now)
		if err != nil {
			return err
		}
		reserved, err := holdsReserved(tx, userID, now, "")
		if err != nil {
			return err
		}
		avail = bucketsRemaining(subs) - reserved
		if avail < 0 {
			avail = 0
		}
		return nil
	})
	return avail, err
}

type ChargeArgs struct {
	UserID         int64
	RequestID      string
	TaskID         string // 有任务上下文时记入该任务的 hold
	BillingGroupID string // 表单识别计费组;非空时一组只扣一次
	Capability     string
	Price          int64 // 本次应扣积分(能力单价快照)
}

type ChargeResult struct {
	Charged   int64 // 实际扣除(计费组已扣过则为 0)
	Available int64 // 扣后可用余额
}

// Charge 在单事务内完成扣费:锁用户行 → 计费组防重 → 可用性检查 → 跨桶扣减
// → 写流水 → 更新本任务 hold 消耗。Price<=0 时只返回余额不扣费。
func Charge(db *gorm.DB, a ChargeArgs) (ChargeResult, error) {
	now := time.Now()
	var res ChargeResult
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, a.UserID); err != nil {
			return err
		}
		subs, err := activeBuckets(tx, a.UserID, now)
		if err != nil {
			return err
		}
		remaining := bucketsRemaining(subs)
		reservedOther, err := holdsReserved(tx, a.UserID, now, a.TaskID)
		if err != nil {
			return err
		}
		avail := remaining - reservedOther

		if a.Price <= 0 {
			all, err := holdsAll(tx, a.UserID, now)
			if err != nil {
				return err
			}
			res.Available = clampZero(remaining - all)
			return nil
		}
		if len(subs) == 0 {
			return ErrNoActiveSub
		}

		// 计费组防重:锚点表里已有该组即视作已计费(锁内检查,唯一索引兜底并发)。
		// 锚点独立于流水行——跨桶扣费每桶一条流水,绑在流水上会与跨桶互斥。
		if a.BillingGroupID != "" {
			var cnt int64
			if err := tx.Model(&model.BillingGroupCharge{}).
				Where("user_id = ? AND billing_group_id = ?", a.UserID, a.BillingGroupID).
				Count(&cnt).Error; err != nil {
				return err
			}
			if cnt > 0 {
				res.Charged = 0
				avail, err := holdsAll(tx, a.UserID, now)
				if err != nil {
					return err
				}
				res.Available = clampZero(remaining - avail)
				return nil
			}
		}

		if avail < a.Price {
			return ErrInsufficient
		}

		// 先落防重锚点:并发下唯一索引在此拦截,避免同组重复扣费
		if a.BillingGroupID != "" {
			anchor := model.BillingGroupCharge{
				UserID:         a.UserID,
				BillingGroupID: a.BillingGroupID,
				RequestID:      a.RequestID,
				Capability:     a.Capability,
				Price:          a.Price,
				CreatedAt:      now,
			}
			if err := tx.Create(&anchor).Error; err != nil {
				return fmt.Errorf("写计费组锚点失败: %w", err)
			}
		}

		// 跨桶扣减,先扣最早到期的桶;每桶一条流水(同 request_id)
		rest := a.Price
		for i := range subs {
			if rest == 0 {
				break
			}
			r := subs[i].AmountTotal - subs[i].AmountUsed
			if r <= 0 {
				continue
			}
			take := min64(r, rest)
			if err := tx.Model(&model.UserSubscription{}).Where("id = ?", subs[i].ID).
				Update("amount_used", gorm.Expr("amount_used + ?", take)).Error; err != nil {
				return err
			}
			subs[i].AmountUsed += take
			rest -= take
			all, err := holdsAll(tx, a.UserID, now)
			if err != nil {
				return err
			}
			afterAvail := clampZero(bucketsRemaining(subs) - all)
			row := model.CreditLedger{
				UserID:         a.UserID,
				SubscriptionID: subs[i].ID,
				RequestID:      a.RequestID,
				BillingGroupID: a.BillingGroupID,
				Capability:     a.Capability,
				Delta:          -take,
				PriceSnapshot:  a.Price,
				BalanceAfter:   afterAvail,
				CreatedAt:      now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("写流水失败: %w", err)
			}
		}

		// 记入本任务的 hold 消耗(超出预占也照记,remaining 由 max(...,0) 兜底)
		if a.TaskID != "" {
			r := tx.Model(&model.CreditHold{}).
				Where("user_id = ? AND task_id = ? AND status = ?", a.UserID, a.TaskID, model.HoldStatusOpen).
				Update("consumed", gorm.Expr("consumed + ?", a.Price))
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected == 0 {
				// 带着 taskId 扣了费,却没有任何 open hold 承接这笔消耗:多半是租约过期被回收
				// (心跳断了)或 taskId 串号。扣费本身有效(桶已扣、流水已写),但预占口径与
				// 实际消耗从此对不上——不留痕就只能等对账时凭空发现差额。
				log.Printf("[CREDIT] 扣费未记入预占 user=%d task=%s request=%s capability=%s price=%d(无 open hold)",
					a.UserID, a.TaskID, a.RequestID, a.Capability, a.Price)
			}
		}

		res.Charged = a.Price
		all, err := holdsAll(tx, a.UserID, now)
		if err != nil {
			return err
		}
		res.Available = clampZero(bucketsRemaining(subs) - all)
		return nil
	})
	return res, err
}

// holdsAll 计算全部 open hold 冻结额(含本任务),用于对外展示的 available。
// 查询出错必须上抛:按 0 兜底会把冻结额算没,写进流水的余额快照与返回给用户的可用额都是错的。
func holdsAll(tx *gorm.DB, userID int64, now time.Time) (int64, error) {
	return holdsReserved(tx, userID, now, "")
}

// ===== 管理员手动调整(测试发分/补偿/人工退款) =====

// AdminGrant 管理员手动发一个额度桶,不挂套餐(PlanID=0)。
// amount 可为 0(只给订阅资格,如发 base 桶续订阅);amount>0 时写一条正向流水,
// 用户流水页可见"管理员发放 +N"——手动调整必须留痕,不静默。
func AdminGrant(db *gorm.DB, userID int64, planType string, amount int64, days int) (*model.UserSubscription, error) {
	if days <= 0 || days > 3650 {
		return nil, fmt.Errorf("有效期天数必须在 1~3650 之间")
	}
	if amount < 0 {
		return nil, fmt.Errorf("积分不能为负")
	}
	now := time.Now()
	var sub model.UserSubscription
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, userID); err != nil {
			return err
		}
		sub = model.UserSubscription{
			UserID: userID, PlanID: 0, PlanType: planType,
			AmountTotal: amount, StartsAt: now, EndsAt: now.AddDate(0, 0, days),
			Status: model.SubStatusActive, CreatedAt: now,
		}
		if err := tx.Create(&sub).Error; err != nil {
			return err
		}
		if amount > 0 {
			subs, err := activeBuckets(tx, userID, now)
			if err != nil {
				return err
			}
			all, err := holdsAll(tx, userID, now)
			if err != nil {
				return err
			}
			avail := clampZero(bucketsRemaining(subs) - all)
			row := model.CreditLedger{
				UserID: userID, SubscriptionID: sub.ID, Capability: "admin_grant",
				Delta: amount, BalanceAfter: avail, CreatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("写流水失败: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// ExtendBucket 延长某个桶的有效期;延到未来的过期桶自动重新激活。已作废的桶不允许延。
func ExtendBucket(db *gorm.DB, subID int64, days int) (*model.UserSubscription, error) {
	if days <= 0 || days > 3650 {
		return nil, fmt.Errorf("延长天数必须在 1~3650 之间")
	}
	var sub model.UserSubscription
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&sub, subID).Error; err != nil {
			return err
		}
		if sub.Status == model.SubStatusRevoked {
			return fmt.Errorf("已作废的桶不能延期")
		}
		sub.EndsAt = sub.EndsAt.AddDate(0, 0, days)
		if sub.Status == model.SubStatusExpired && sub.EndsAt.After(time.Now()) {
			sub.Status = model.SubStatusActive
		}
		return tx.Model(&model.UserSubscription{}).Where("id = ?", sub.ID).
			Updates(map[string]any{"ends_at": sub.EndsAt, "status": sub.Status}).Error
	})
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// RevokeBucket 作废一个桶(剩余额度立即不可用);历史流水不动。
func RevokeBucket(db *gorm.DB, subID int64) error {
	r := db.Model(&model.UserSubscription{}).
		Where("id = ? AND status <> ?", subID, model.SubStatusRevoked).
		Update("status", model.SubStatusRevoked)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ===== 任务预占 =====

const (
	holdTTL = 2 * time.Hour // 租约;插件每 10 分钟心跳续期
)

// CreateHold 幂等创建任务预占:同任务已有 open hold 直接返回;可用不足报错。
func CreateHold(db *gorm.DB, userID int64, taskID string, amount int64) (*model.CreditHold, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("预占额必须为正")
	}
	now := time.Now()
	var hold model.CreditHold
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, userID); err != nil {
			return err
		}
		var existing model.CreditHold
		err := tx.Where("user_id = ? AND task_id = ? AND status = ?", userID, taskID, model.HoldStatusOpen).
			First(&existing).Error
		if err == nil {
			hold = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		subs, err := activeBuckets(tx, userID, now)
		if err != nil {
			return err
		}
		reserved, err := holdsReserved(tx, userID, now, "")
		if err != nil {
			return err
		}
		if bucketsRemaining(subs)-reserved < amount {
			return ErrInsufficient
		}
		hold = model.CreditHold{
			ID:        uuid.NewString(),
			UserID:    userID,
			TaskID:    taskID,
			Amount:    amount,
			Status:    model.HoldStatusOpen,
			ExpiresAt: now.Add(holdTTL),
			CreatedAt: now,
		}
		return tx.Create(&hold).Error
	})
	if err != nil {
		return nil, err
	}
	return &hold, nil
}

// Heartbeat 续期,返回新到期时间;hold 不存在或非 open 报错。
func Heartbeat(db *gorm.DB, userID int64, holdID string) (time.Time, error) {
	exp := time.Now().Add(holdTTL)
	r := db.Model(&model.CreditHold{}).
		Where("id = ? AND user_id = ? AND status = ?", holdID, userID, model.HoldStatusOpen).
		Update("expires_at", exp)
	if r.Error != nil {
		return time.Time{}, r.Error
	}
	if r.RowsAffected == 0 {
		return time.Time{}, gorm.ErrRecordNotFound
	}
	return exp, nil
}

// Settle 原子幂等结算(open→settled),释放剩余冻结额,无流水动作。
// 已结算/已过期是幂等成功;从不存在的 holdID 返回 ErrRecordNotFound——
// 一律成功会让插件把"服务端根本没有这个预占"当成结算完成,冻结额一直挂到过期为止,
// holdId 串号/传错这类真实故障也被一起吞掉。
func Settle(db *gorm.DB, userID int64, holdID string) error {
	now := time.Now()
	r := db.Model(&model.CreditHold{}).
		Where("id = ? AND user_id = ? AND status = ?", holdID, userID, model.HoldStatusOpen).
		Updates(map[string]any{"status": model.HoldStatusSettled, "settled_at": now})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected > 0 {
		return nil
	}
	var cnt int64
	if err := db.Model(&model.CreditHold{}).
		Where("id = ? AND user_id = ?", holdID, userID).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ExpireHolds 定时任务:释放过期未结算的 hold(浏览器崩溃/任务遗忘兜底)。
func ExpireHolds(db *gorm.DB) (int64, error) {
	r := db.Model(&model.CreditHold{}).
		Where("status = ? AND expires_at <= ?", model.HoldStatusOpen, time.Now()).
		Update("status", model.HoldStatusExpired)
	return r.RowsAffected, r.Error
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func clampZero(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
