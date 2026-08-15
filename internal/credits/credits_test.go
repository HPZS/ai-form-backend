// ai-form-backend - AGPL-3.0
package credits

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/internal/model"
)

func setup(t *testing.T) (*gorm.DB, int64) {
	t.Helper()
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	u := model.User{Email: "t@example.com", Role: "user", Status: "active"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return db, u.ID
}

func addBucket(t *testing.T, db *gorm.DB, userID, total int64, endsIn time.Duration) int64 {
	t.Helper()
	sub := model.UserSubscription{
		UserID: userID, PlanID: 1, AmountTotal: total,
		StartsAt: time.Now(), EndsAt: time.Now().Add(endsIn), Status: model.SubStatusActive,
	}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	return sub.ID
}

// 外部复审的验算用例:余额 1000、预占 530、消费 10 → available 470
func TestAvailableWithHold(t *testing.T) {
	db, uid := setup(t)
	addBucket(t, db, uid, 1000, time.Hour)

	hold, err := CreateHold(db, uid, "task-1", 530)
	if err != nil {
		t.Fatalf("预占失败: %v", err)
	}
	res, err := Charge(db, ChargeArgs{UserID: uid, RequestID: "r1", TaskID: "task-1", Capability: "match_columns", Price: 10})
	if err != nil {
		t.Fatalf("扣费失败: %v", err)
	}
	if res.Charged != 10 {
		t.Fatalf("应扣 10,实际 %d", res.Charged)
	}
	avail, err := Available(db, uid)
	if err != nil {
		t.Fatal(err)
	}
	if avail != 470 {
		t.Fatalf("available 应为 470(990 - 冻结 520),实际 %d", avail)
	}
	var h model.CreditHold
	db.First(&h, "id = ?", hold.ID)
	if h.Consumed != 10 {
		t.Fatalf("hold.consumed 应为 10,实际 %d", h.Consumed)
	}
}

// 跨桶扣费:先扣最早到期的桶,不足部分落到下一个桶,两条流水
func TestChargeAcrossBuckets(t *testing.T) {
	db, uid := setup(t)
	early := addBucket(t, db, uid, 3, time.Hour)      // 先到期,只剩 3
	late := addBucket(t, db, uid, 100, 48*time.Hour)  // 后到期

	res, err := Charge(db, ChargeArgs{UserID: uid, RequestID: "r1", Capability: "analyze_form", Price: 5})
	if err != nil {
		t.Fatalf("扣费失败: %v", err)
	}
	if res.Charged != 5 {
		t.Fatalf("应扣 5,实际 %d", res.Charged)
	}
	var rows []model.CreditLedger
	db.Order("id asc").Find(&rows, "user_id = ?", uid)
	if len(rows) != 2 {
		t.Fatalf("应有 2 条流水,实际 %d", len(rows))
	}
	if rows[0].SubscriptionID != early || rows[0].Delta != -3 {
		t.Fatalf("第一条应从早到期桶扣 3,实际桶 %d delta %d", rows[0].SubscriptionID, rows[0].Delta)
	}
	if rows[1].SubscriptionID != late || rows[1].Delta != -2 {
		t.Fatalf("第二条应从晚到期桶扣 2,实际桶 %d delta %d", rows[1].SubscriptionID, rows[1].Delta)
	}
}

// 计费组:同组第二次计费不再扣
func TestBillingGroupChargesOnce(t *testing.T) {
	db, uid := setup(t)
	addBucket(t, db, uid, 100, time.Hour)

	r1, err := Charge(db, ChargeArgs{UserID: uid, RequestID: "r1", BillingGroupID: "g1", Capability: "analyze_form", Price: 5})
	if err != nil || r1.Charged != 5 {
		t.Fatalf("首次应扣 5: %v %+v", err, r1)
	}
	r2, err := Charge(db, ChargeArgs{UserID: uid, RequestID: "r2", BillingGroupID: "g1", Capability: "analyze_form", Price: 5})
	if err != nil {
		t.Fatalf("同组第二次不应报错: %v", err)
	}
	if r2.Charged != 0 {
		t.Fatalf("同组第二次应扣 0,实际 %d", r2.Charged)
	}
	if avail, _ := Available(db, uid); avail != 95 {
		t.Fatalf("available 应为 95,实际 %d", avail)
	}
}

// 跨桶 + 计费组:两者组合曾因"每桶一条流水"撞上计费组防重唯一索引导致整笔扣费失败
// (模型已调用、成本已发生却回滚 → 重试再撞 → 无限白烧上游费用),防重语义必须与流水行数解耦
func TestChargeAcrossBucketsWithBillingGroup(t *testing.T) {
	db, uid := setup(t)
	addBucket(t, db, uid, 3, time.Hour)     // 先到期,只剩 3
	addBucket(t, db, uid, 47, 48*time.Hour) // 后到期

	res, err := Charge(db, ChargeArgs{UserID: uid, RequestID: "r1", BillingGroupID: "g1", Capability: "match_columns", Price: 50})
	if err != nil {
		t.Fatalf("跨桶带计费组扣费不应失败: %v", err)
	}
	if res.Charged != 50 {
		t.Fatalf("应扣 50,实际 %d", res.Charged)
	}
	var rows []model.CreditLedger
	db.Order("id asc").Find(&rows, "user_id = ?", uid)
	if len(rows) != 2 {
		t.Fatalf("应有 2 条流水(每桶一条),实际 %d", len(rows))
	}
	if avail, _ := Available(db, uid); avail != 0 {
		t.Fatalf("扣完应为 0,实际 %d", avail)
	}
	// 防重仍须生效:同组再扣不收费
	r2, err := Charge(db, ChargeArgs{UserID: uid, RequestID: "r2", BillingGroupID: "g1", Capability: "match_columns", Price: 50})
	if err != nil {
		t.Fatalf("同组第二次不应报错: %v", err)
	}
	if r2.Charged != 0 {
		t.Fatalf("同组第二次应扣 0,实际 %d", r2.Charged)
	}
}

// 余额不足与无订阅
func TestInsufficientAndNoSub(t *testing.T) {
	db, uid := setup(t)
	if _, err := Charge(db, ChargeArgs{UserID: uid, RequestID: "r0", Capability: "x", Price: 1}); !errors.Is(err, ErrNoActiveSub) {
		t.Fatalf("无订阅应报 ErrNoActiveSub,实际 %v", err)
	}
	addBucket(t, db, uid, 3, time.Hour)
	if _, err := Charge(db, ChargeArgs{UserID: uid, RequestID: "r1", Capability: "x", Price: 5}); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("不足应报 ErrInsufficient,实际 %v", err)
	}
}

// 过期桶不计入;过期任务只改状态不清历史
func TestExpiredBucketExcluded(t *testing.T) {
	db, uid := setup(t)
	sub := model.UserSubscription{
		UserID: uid, PlanID: 1, AmountTotal: 100, AmountUsed: 40,
		StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(-time.Minute),
		Status: model.SubStatusActive,
	}
	db.Create(&sub)
	if avail, _ := Available(db, uid); avail != 0 {
		t.Fatalf("过期桶不应计入,available 应为 0,实际 %d", avail)
	}
}

// 预占生命周期:幂等创建、结算释放、过期释放
func TestHoldLifecycle(t *testing.T) {
	db, uid := setup(t)
	addBucket(t, db, uid, 100, time.Hour)

	h1, err := CreateHold(db, uid, "task-1", 60)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := CreateHold(db, uid, "task-1", 60) // 幂等:返回同一个
	if err != nil || h2.ID != h1.ID {
		t.Fatalf("重复创建应返回既有 hold: %v", err)
	}
	if _, err := CreateHold(db, uid, "task-2", 50); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("超出可用应报不足,实际 %v", err)
	}
	if err := Settle(db, uid, h1.ID); err != nil {
		t.Fatal(err)
	}
	if err := Settle(db, uid, h1.ID); err != nil { // 幂等
		t.Fatal(err)
	}
	if avail, _ := Available(db, uid); avail != 100 {
		t.Fatalf("结算后应释放冻结,available 应为 100,实际 %d", avail)
	}
	// 过期释放
	h3, _ := CreateHold(db, uid, "task-3", 30)
	db.Model(&model.CreditHold{}).Where("id = ?", h3.ID).Update("expires_at", time.Now().Add(-time.Second))
	n, err := ExpireHolds(db)
	if err != nil || n != 1 {
		t.Fatalf("应释放 1 个过期 hold: %v %d", err, n)
	}
	if avail, _ := Available(db, uid); avail != 100 {
		t.Fatalf("过期释放后 available 应为 100,实际 %d", avail)
	}
	// 过期后同任务可重新预占
	if _, err := CreateHold(db, uid, "task-3", 30); err != nil {
		t.Fatalf("过期后重新预占应成功: %v", err)
	}
}

// 超出预占:自己 hold 耗尽后等同散调,余额够就继续扣
func TestOverconsumeHold(t *testing.T) {
	db, uid := setup(t)
	addBucket(t, db, uid, 100, time.Hour)
	if _, err := CreateHold(db, uid, "task-1", 10); err != nil {
		t.Fatal(err)
	}
	// 连扣 3 次 5 分,第三次已超预占(10),仍应成功
	for i, rid := range []string{"r1", "r2", "r3"} {
		if _, err := Charge(db, ChargeArgs{UserID: uid, RequestID: rid, TaskID: "task-1", Capability: "x", Price: 5}); err != nil {
			t.Fatalf("第 %d 次扣费失败: %v", i+1, err)
		}
	}
	if avail, _ := Available(db, uid); avail != 85 {
		t.Fatalf("100-15=85,实际 %d", avail)
	}
}
