// ai-form-backend - AGPL-3.0
package credits

import (
	"testing"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
)

func TestAdminGrantWithLedger(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)

	sub, err := AdminGrant(db, u.ID, model.PlanTypeBonus, 500, 30)
	if err != nil {
		t.Fatal(err)
	}
	if sub.PlanID != 0 || sub.PlanType != model.PlanTypeBonus || sub.AmountTotal != 500 {
		t.Fatalf("桶属性不符: %+v", sub)
	}
	avail, err := Available(db, u.ID)
	if err != nil || avail != 500 {
		t.Fatalf("发放后可用应 500,实际 %d (%v)", avail, err)
	}
	var rows []model.CreditLedger
	db.Where("user_id = ?", u.ID).Find(&rows)
	if len(rows) != 1 || rows[0].Delta != 500 || rows[0].Capability != "admin_grant" {
		t.Fatalf("应有 1 条 +500 的发放流水,实际 %+v", rows)
	}

	// 0 积分的 base 桶:只给订阅资格,不写流水
	if _, err := AdminGrant(db, u.ID, model.PlanTypeBase, 0, 30); err != nil {
		t.Fatal(err)
	}
	db.Where("user_id = ?", u.ID).Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("0 积分发放不应写流水,实际 %d 条", len(rows))
	}

	// 参数校验
	if _, err := AdminGrant(db, u.ID, model.PlanTypeBonus, -1, 30); err == nil {
		t.Fatal("负积分应报错")
	}
	if _, err := AdminGrant(db, u.ID, model.PlanTypeBonus, 100, 0); err == nil {
		t.Fatal("0 天应报错")
	}
}

func TestExtendAndRevokeBucket(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)

	// 过期桶延到未来:自动重新激活
	expired := model.UserSubscription{
		UserID: u.ID, PlanID: 0, PlanType: model.PlanTypeBase, AmountTotal: 100,
		StartsAt: time.Now().Add(-48 * time.Hour), EndsAt: time.Now().Add(-24 * time.Hour),
		Status: model.SubStatusExpired, CreatedAt: time.Now(),
	}
	db.Create(&expired)
	sub, err := ExtendBucket(db, expired.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != model.SubStatusActive || !sub.EndsAt.After(time.Now()) {
		t.Fatalf("延期后应重新激活,实际 %+v", sub)
	}
	if avail, _ := Available(db, u.ID); avail != 100 {
		t.Fatalf("重新激活后额度应可用,实际 %d", avail)
	}

	// 作废后额度立即不可用;已作废不能延期
	if err := RevokeBucket(db, sub.ID); err != nil {
		t.Fatal(err)
	}
	if avail, _ := Available(db, u.ID); avail != 0 {
		t.Fatalf("作废后可用应 0,实际 %d", avail)
	}
	if _, err := ExtendBucket(db, sub.ID, 30); err == nil {
		t.Fatal("已作废的桶延期应报错")
	}
	if err := RevokeBucket(db, 99999); err == nil {
		t.Fatal("不存在的桶作废应报错")
	}
}

// 延期是读-改-写:必须在锁内取当前 ends_at,连续延期要累加而不是互相覆盖。
// (行锁本身只在 postgres 生效——sqlite 写事务天然串行,无法在此复现并发覆盖,
//  这里固化的是"每次都基于最新值累加"的语义)
func TestExtendBucketAccumulates(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)
	base := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	sub := model.UserSubscription{
		UserID: u.ID, PlanType: model.PlanTypeBase, AmountTotal: 100,
		StartsAt: time.Now(), EndsAt: base, Status: model.SubStatusActive, CreatedAt: time.Now(),
	}
	db.Create(&sub)

	if _, err := ExtendBucket(db, sub.ID, 3); err != nil {
		t.Fatal(err)
	}
	got, err := ExtendBucket(db, sub.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := base.AddDate(0, 0, 7)
	if !got.EndsAt.Equal(want) {
		t.Fatalf("两次延期应累加到 %v,实际 %v", want, got.EndsAt)
	}
	// 库里也必须是累加后的值(返回值与落库值不能各说各话)
	var fresh model.UserSubscription
	db.First(&fresh, sub.ID)
	if fresh.EndsAt.Unix() != want.Unix() {
		t.Fatalf("库中到期时间应为 %v,实际 %v", want, fresh.EndsAt)
	}
	if _, err := ExtendBucket(db, 99999, 3); err == nil {
		t.Fatal("不存在的桶延期应报错")
	}
}
