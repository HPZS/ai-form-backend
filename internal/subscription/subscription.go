// ai-form-backend - AGPL-3.0
// 订阅套餐与额度桶。桶模型取自 new-api 的 SubscriptionPlan/UserSubscription 设计:
// 一笔购买 = 一个独立有效期的额度桶,到期只改状态,历史账目不动。
package subscription

import (
	"errors"
	"fmt"
	"log"
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
		// SKU 表见《积分与订阅计费方案》§4:1 积分 = 0.01 元
		plans := []model.SubscriptionPlan{
			{Name: "试用", PlanType: model.PlanTypeTrial, PriceCents: 0, TotalCredits: 500, DurationDays: 14, ForSale: false, SortOrder: 0},
			{Name: "个人版", PlanType: model.PlanTypeBase, PriceCents: 1990, TotalCredits: 500, DurationDays: 30, ForSale: true, SortOrder: 1},
			{Name: "加油包·小", PlanType: model.PlanTypePack, PriceCents: 1000, TotalCredits: 1000, DurationDays: 365, ForSale: true, SortOrder: 2},
			{Name: "加油包·大", PlanType: model.PlanTypePack, PriceCents: 4500, TotalCredits: 5000, DurationDays: 365, ForSale: true, SortOrder: 3},
			{Name: "首充礼", PlanType: model.PlanTypeBonus, PriceCents: 0, TotalCredits: 1500, DurationDays: 60, ForSale: false, SortOrder: 99},
		}
		if err := db.Create(&plans).Error; err != nil {
			return fmt.Errorf("播种套餐失败: %w", err)
		}
		// GORM 坑:for_sale 列带 default:true,Create 时零值 false 会被忽略落成 true——
		// 试用/首充礼是系统发放的,必须显式改回不可售,否则会出现在购买页
		if err := db.Model(&model.SubscriptionPlan{}).
			Where("plan_type IN ?", []string{model.PlanTypeTrial, model.PlanTypeBonus}).
			Update("for_sale", false).Error; err != nil {
			return fmt.Errorf("修正套餐可售标记失败: %w", err)
		}
	}
	// 全局默认模型(单行):模型名必须由管理员在管理台设置,不播种任何厂商占位值
	var defCount int64
	if err := db.Model(&model.AIDefault{}).Count(&defCount).Error; err != nil {
		return err
	}
	if defCount == 0 {
		if err := db.Create(&model.AIDefault{ID: 1, Model: ""}).Error; err != nil {
			return fmt.Errorf("播种 AI 默认参数失败: %w", err)
		}
	}
	var priceCount int64
	if err := db.Model(&model.CapabilityPrice{}).Count(&priceCount).Error; err != nil {
		return err
	}
	if priceCount == 0 {
		// 只播种单价;模型一律走全局默认(温度/maxTokens 由代码按能力定死,不是配置项)。
		// 计费事件只有两个(方案文档 §5):方案费挂在初次 match_columns(50 分,服务端派生计费组防重),
		// AI 生成 1 分/格;其余能力 0 分,由底座月费覆盖。
		creditsByCap := map[string]int64{"match_columns": 50, "generate_field": 1}
		var prices []model.CapabilityPrice
		for _, m := range ai.CapabilityMetas() {
			prices = append(prices, model.CapabilityPrice{Capability: m.Key, Credits: creditsByCap[m.Key], Enabled: true})
		}
		if err := db.Create(&prices).Error; err != nil {
			return fmt.Errorf("播种能力单价失败: %w", err)
		}
	}
	return nil
}

// AssignBucket 给用户发一个额度桶(在调用方事务内执行);桶固化套餐类型,后改套餐不影响已发桶。
func AssignBucket(tx *gorm.DB, userID int64, plan *model.SubscriptionPlan, orderID *int64) error {
	now := time.Now()
	sub := model.UserSubscription{
		UserID:      userID,
		PlanID:      plan.ID,
		PlanType:    plan.PlanType,
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
	err := tx.Where("plan_type = ?", model.PlanTypeTrial).First(&plan).Error
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
		// expired 也允许翻转:那只是后台清理任务给"超时未付"下的判断,
		// 而验签通过的支付成功回调是事实——事实优先,否则清理任务会变成资损来源
		if order.Status != model.OrderStatusPending && order.Status != model.OrderStatusExpired {
			return fmt.Errorf("订单状态异常: %s", order.Status)
		}
		if order.Status == model.OrderStatusExpired {
			log.Printf("警告: 已过期订单收到支付成功回调,按已付处理 trade_no=%s user=%d", tradeNo, order.UserID)
		}
		now := time.Now()
		// 状态守卫更新:并发回调只有一个能翻转
		r := tx.Model(&model.PaymentOrder{}).
			Where("id = ? AND status = ?", order.ID, order.Status).
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
		if err := AssignBucket(tx, order.UserID, &plan, &order.ID); err != nil {
			return err
		}
		// 首充礼(方案文档 §6.5):用户的首笔 base 订单额外发放赠送桶(1500 分/60 天)。
		// 在同一事务内判断与发放:重复回调被上方状态守卫挡住,不会重复赠送。
		if plan.PlanType == model.PlanTypeBase {
			var paidBaseBefore int64
			err := tx.Model(&model.PaymentOrder{}).
				Joins("JOIN subscription_plans ON subscription_plans.id = payment_orders.plan_id").
				Where("payment_orders.user_id = ? AND payment_orders.status = ? AND payment_orders.id <> ? AND subscription_plans.plan_type = ?",
					order.UserID, model.OrderStatusPaid, order.ID, model.PlanTypeBase).
				Count(&paidBaseBefore).Error
			if err != nil {
				return err
			}
			if paidBaseBefore == 0 {
				var bonus model.SubscriptionPlan
				err := tx.Where("plan_type = ?", model.PlanTypeBonus).First(&bonus).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					log.Printf("警告: 未配置首充礼套餐(plan_type=bonus),订单 %s 未发放首充赠送", tradeNo)
					return nil
				}
				if err != nil {
					return err
				}
				if err := AssignBucket(tx, order.UserID, &bonus, &order.ID); err != nil {
					return fmt.Errorf("发放首充礼失败: %w", err)
				}
			}
		}
		return nil
	})
}

// ExpireSubscriptions 定时任务:把已过期的 active 桶置为 expired(只改状态)。
func ExpireSubscriptions(db *gorm.DB) (int64, error) {
	r := db.Model(&model.UserSubscription{}).
		Where("status = ? AND ends_at <= ?", model.SubStatusActive, time.Now()).
		Update("status", model.SubStatusExpired)
	return r.RowsAffected, r.Error
}
