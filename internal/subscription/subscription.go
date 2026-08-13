// ai-form-backend - AGPL-3.0
// 订阅套餐与额度桶。桶模型取自 new-api 的 SubscriptionPlan/UserSubscription 设计:
// 一笔购买 = 一个独立有效期的额度桶,到期只改状态,历史账目不动。
package subscription

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/internal/ai"
	"github.com/HPZS/ai-form-backend/internal/model"
)

// SeedDefaults 首次启动播种默认套餐与能力单价(已有数据则不动)。
func SeedDefaults(db *gorm.DB) error {
	var planCount int64
	if err := db.Model(&model.SubscriptionPlan{}).Count(&planCount).Error; err != nil {
		return err
	}
	if planCount == 0 {
		plans := []model.SubscriptionPlan{
			{Name: "试用", PriceCents: 0, TotalCredits: 100, DurationDays: 14, ForSale: false, Trial: true, SortOrder: 0},
			{Name: "基础版", PriceCents: 990, TotalCredits: 1000, DurationDays: 30, ForSale: true, SortOrder: 1},
			{Name: "进阶版", PriceCents: 5990, TotalCredits: 10000, DurationDays: 30, ForSale: true, SortOrder: 2},
		}
		if err := db.Create(&plans).Error; err != nil {
			return fmt.Errorf("播种套餐失败: %w", err)
		}
	}
	// 全局 AI 默认参数(单行):模型名必须由管理员在管理台设置,不播种任何厂商占位值
	var defCount int64
	if err := db.Model(&model.AIDefault{}).Count(&defCount).Error; err != nil {
		return err
	}
	if defCount == 0 {
		if err := db.Create(&model.AIDefault{ID: 1, Model: "", Temperature: 0, MaxTokens: 4000}).Error; err != nil {
			return fmt.Errorf("播种 AI 默认参数失败: %w", err)
		}
	}
	var priceCount int64
	if err := db.Model(&model.CapabilityPrice{}).Count(&priceCount).Error; err != nil {
		return err
	}
	if priceCount == 0 {
		// 只播种单价;模型参数一律走全局默认,例外(生成字段要一点随机性)才带覆盖
		creditsByCap := map[string]int64{"analyze_form": 5, "match_columns": 5, "generate_rule": 3, "generate_field": 1}
		genFieldTemp := 0.3
		var prices []model.CapabilityPrice
		for _, m := range ai.CapabilityMetas() {
			p := model.CapabilityPrice{Capability: m.Key, Credits: creditsByCap[m.Key], Enabled: true}
			if m.Key == "generate_field" {
				p.Temperature = &genFieldTemp
			}
			prices = append(prices, p)
		}
		if err := db.Create(&prices).Error; err != nil {
			return fmt.Errorf("播种能力单价失败: %w", err)
		}
	}
	return nil
}

// AssignBucket 给用户发一个额度桶(在调用方事务内执行)。
func AssignBucket(tx *gorm.DB, userID int64, plan *model.SubscriptionPlan, orderID *int64) error {
	now := time.Now()
	sub := model.UserSubscription{
		UserID:      userID,
		PlanID:      plan.ID,
		OrderID:     orderID,
		AmountTotal: plan.TotalCredits,
		StartsAt:    now,
		EndsAt:      now.AddDate(0, 0, plan.DurationDays),
		Status:      model.SubStatusActive,
		CreatedAt:   now,
	}
	return tx.Create(&sub).Error
}

// AssignTrial 注册时发试用桶;未配置试用套餐时跳过(不报错但记录)。
func AssignTrial(tx *gorm.DB, userID int64) error {
	var plan model.SubscriptionPlan
	err := tx.Where("trial = ?", true).First(&plan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return AssignBucket(tx, userID, &plan, nil)
}

// CompleteOrder 支付回调落账:pending→paid 并发桶。幂等——已 paid 的订单直接成功返回。
func CompleteOrder(db *gorm.DB, tradeNo, notifyRaw string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var order model.PaymentOrder
		if err := tx.Where("trade_no = ?", tradeNo).First(&order).Error; err != nil {
			return fmt.Errorf("订单不存在: %w", err)
		}
		if order.Status == model.OrderStatusPaid {
			return nil // 重复回调,幂等成功
		}
		if order.Status != model.OrderStatusPending {
			return fmt.Errorf("订单状态异常: %s", order.Status)
		}
		now := time.Now()
		// 状态守卫更新:并发回调只有一个能从 pending 翻转
		r := tx.Model(&model.PaymentOrder{}).
			Where("id = ? AND status = ?", order.ID, model.OrderStatusPending).
			Updates(map[string]any{"status": model.OrderStatusPaid, "paid_at": now, "notify_raw": notifyRaw})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return nil // 另一个回调已处理
		}
		var plan model.SubscriptionPlan
		if err := tx.First(&plan, order.PlanID).Error; err != nil {
			return err
		}
		return AssignBucket(tx, order.UserID, &plan, &order.ID)
	})
}

// ExpireSubscriptions 定时任务:把已过期的 active 桶置为 expired(只改状态)。
func ExpireSubscriptions(db *gorm.DB) (int64, error) {
	r := db.Model(&model.UserSubscription{}).
		Where("status = ? AND ends_at <= ?", model.SubStatusActive, time.Now()).
		Update("status", model.SubStatusExpired)
	return r.RowsAffected, r.Error
}
