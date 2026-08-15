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

// 定时任务:到期的 active 桶置 expired;未到期与已作废的都不动,历史额度不清零
func TestExpireSubscriptions(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)
	mk := func(endsIn time.Duration, status string) int64 {
		sub := model.UserSubscription{
			UserID: u.ID, PlanID: 1, PlanType: model.PlanTypeBase, AmountTotal: 100, AmountUsed: 40,
			StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(endsIn), Status: status,
		}
		db.Create(&sub)
		return sub.ID
	}
	due := mk(-time.Minute, model.SubStatusActive)
	alive := mk(time.Hour, model.SubStatusActive)
	revoked := mk(-time.Minute, model.SubStatusRevoked)

	n, err := ExpireSubscriptions(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应只过期 1 个桶,实际 %d", n)
	}
	statusOf := func(id int64) string {
		var s model.UserSubscription
		db.First(&s, id)
		return s.Status
	}
	if statusOf(due) != model.SubStatusExpired {
		t.Fatalf("到期桶应置 expired,实际 %s", statusOf(due))
	}
	if statusOf(alive) != model.SubStatusActive {
		t.Fatalf("未到期桶不应被动,实际 %s", statusOf(alive))
	}
	if statusOf(revoked) != model.SubStatusRevoked {
		t.Fatalf("已作废桶不应被改写,实际 %s", statusOf(revoked))
	}
	// 只改状态:历史额度一分不动
	var s model.UserSubscription
	db.First(&s, due)
	if s.AmountTotal != 100 || s.AmountUsed != 40 {
		t.Fatalf("过期只改状态,额度不应被改写: %+v", s)
	}
	// 重复执行不再有变更(幂等)
	if n, _ := ExpireSubscriptions(db); n != 0 {
		t.Fatalf("重复执行不应再有变更,实际 %d", n)
	}
}
