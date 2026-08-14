// ai-form-backend - AGPL-3.0
package subscription

import (
	"testing"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
)

// 播种符合计费方案 v1 的 SKU 表与计费事件表
func TestSeedDefaults(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(db); err != nil {
		t.Fatal(err)
	}
	var plans []model.SubscriptionPlan
	db.Order("sort_order asc").Find(&plans)
	types := map[string]int{}
	for _, p := range plans {
		types[p.PlanType]++
	}
	if types[model.PlanTypeTrial] != 1 || types[model.PlanTypeBase] != 1 ||
		types[model.PlanTypePack] != 2 || types[model.PlanTypeBonus] != 1 {
		t.Fatalf("套餐类型分布不符: %v", types)
	}
	// 试用/首充礼不可售(防 GORM default:true 吞掉零值 false 的回归)
	for _, p := range plans {
		wantSale := p.PlanType == model.PlanTypeBase || p.PlanType == model.PlanTypePack
		if p.ForSale != wantSale {
			t.Fatalf("套餐 %s(%s) 可售标记应为 %v,实际 %v", p.Name, p.PlanType, wantSale, p.ForSale)
		}
	}
	var prices []model.CapabilityPrice
	db.Find(&prices)
	byCap := map[string]int64{}
	for _, p := range prices {
		byCap[p.Capability] = p.Credits
	}
	if byCap["match_columns"] != 50 || byCap["generate_field"] != 1 {
		t.Fatalf("计费能力单价不符: match_columns=%d generate_field=%d", byCap["match_columns"], byCap["generate_field"])
	}
	for cap, credits := range byCap {
		if cap != "match_columns" && cap != "generate_field" && credits != 0 {
			t.Fatalf("能力 %s 应为 0 分,实际 %d", cap, credits)
		}
	}
}

// 存量库升级:新增能力(如 name_fields)缺行时再次播种自动补上,已有行不被改动
func TestSeedBackfillsNewCapability(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(db); err != nil {
		t.Fatal(err)
	}
	// 模拟旧库:没有 name_fields 行,且管理员改过 match_columns 单价
	if err := db.Where("capability = ?", "name_fields").Delete(&model.CapabilityPrice{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CapabilityPrice{}).Where("capability = ?", "match_columns").
		Update("credits", 80).Error; err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(db); err != nil {
		t.Fatal(err)
	}
	var nf model.CapabilityPrice
	if err := db.Where("capability = ?", "name_fields").First(&nf).Error; err != nil {
		t.Fatalf("name_fields 应被补种: %v", err)
	}
	if nf.Credits != 0 || !nf.Enabled {
		t.Fatalf("name_fields 应为 0 分且启用,实际 credits=%d enabled=%v", nf.Credits, nf.Enabled)
	}
	var mc model.CapabilityPrice
	if err := db.Where("capability = ?", "match_columns").First(&mc).Error; err != nil {
		t.Fatal(err)
	}
	if mc.Credits != 80 {
		t.Fatalf("已有配置不应被播种覆盖,match_columns 应保持 80,实际 %d", mc.Credits)
	}
}

// 首笔 base 订单加赠首充礼桶;第二笔不再赠送
func TestCompleteOrderFirstBaseBonus(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(db); err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)
	var base model.SubscriptionPlan
	if err := db.Where("plan_type = ?", model.PlanTypeBase).First(&base).Error; err != nil {
		t.Fatal(err)
	}

	makeOrder := func(tradeNo string) {
		db.Create(&model.PaymentOrder{
			TradeNo: tradeNo, UserID: u.ID, PlanID: base.ID,
			AmountCents: base.PriceCents, Status: model.OrderStatusPending, CreatedAt: time.Now(),
		})
	}

	makeOrder("T1")
	if err := CompleteOrder(db, "T1", "{}"); err != nil {
		t.Fatal(err)
	}
	var subs []model.UserSubscription
	db.Where("user_id = ?", u.ID).Find(&subs)
	if len(subs) != 2 {
		t.Fatalf("首充应发 base+bonus 两个桶,实际 %d", len(subs))
	}
	byType := map[string]model.UserSubscription{}
	for _, s := range subs {
		byType[s.PlanType] = s
	}
	if byType[model.PlanTypeBase].AmountTotal != 500 {
		t.Fatalf("base 桶额度应 500,实际 %d", byType[model.PlanTypeBase].AmountTotal)
	}
	if byType[model.PlanTypeBonus].AmountTotal != 1500 {
		t.Fatalf("首充礼桶额度应 1500,实际 %d", byType[model.PlanTypeBonus].AmountTotal)
	}
	// 首充礼 60 天有效(不随首月清零)
	bonusDays := time.Until(byType[model.PlanTypeBonus].EndsAt).Hours() / 24
	if bonusDays < 59 || bonusDays > 61 {
		t.Fatalf("首充礼应 60 天有效,实际约 %.1f 天", bonusDays)
	}

	// 重复回调幂等:不重复发桶
	if err := CompleteOrder(db, "T1", "{}"); err != nil {
		t.Fatal(err)
	}
	db.Where("user_id = ?", u.ID).Find(&subs)
	if len(subs) != 2 {
		t.Fatalf("重复回调不应再发桶,实际 %d", len(subs))
	}

	// 第二笔 base 订单:只发 base 桶,不再赠送
	makeOrder("T2")
	if err := CompleteOrder(db, "T2", "{}"); err != nil {
		t.Fatal(err)
	}
	db.Where("user_id = ?", u.ID).Find(&subs)
	if len(subs) != 3 {
		t.Fatalf("第二笔应只加 1 个 base 桶,实际共 %d", len(subs))
	}
	var bonusCnt int64
	db.Model(&model.UserSubscription{}).
		Where("user_id = ? AND plan_type = ?", u.ID, model.PlanTypeBonus).Count(&bonusCnt)
	if bonusCnt != 1 {
		t.Fatalf("首充礼只应发一次,实际 %d", bonusCnt)
	}
}
