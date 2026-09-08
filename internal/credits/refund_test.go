package credits

import (
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestRefundIdempotentExpiredAndGrossBudget(t *testing.T) {
	db, uid, task, h := budgetFixture(t, 50)
	r, err := reserveFixture(db, uid, task, h)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Transaction(func(tx *gorm.DB) error {
		if e := DebitReserved(tx, r); e != nil {
			return e
		}
		r.Status = model.AIReqOK
		return tx.Save(r).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&model.UserSubscription{}).Where("user_id = ?", uid).Update("ends_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err = Refund(db, uid, uid, r.RequestID, id, "结果争议返还", 30); err != nil {
		t.Fatal(err)
	}
	if err = Refund(db, uid, uid, r.RequestID, id, "结果争议返还", 30); err != nil {
		t.Fatal(err)
	}
	if err = Refund(db, uid, uid, r.RequestID, id, "结果争议返还", 31); err == nil {
		t.Fatal("同 ID 换金额应拒绝")
	}
	if err = Refund(db, uid, uid, r.RequestID, uuid.NewString(), "超额", 21); err == nil {
		t.Fatal("超额返还")
	}
	totals, e := Totals(db, uid, task)
	if e != nil || totals.Charged != 50 || totals.Refunded != 30 || totals.Net != 20 {
		t.Fatalf("账单 %+v %v", totals, e)
	}
	var buckets []model.UserSubscription
	if e = db.Where("user_id = ? AND plan_type = ?", uid, model.PlanTypeBonus).Find(&buckets).Error; e != nil {
		t.Fatal(e)
	}
	if len(buckets) != 1 || buckets[0].AmountTotal != 30 || time.Until(buckets[0].EndsAt) < 29*24*time.Hour {
		t.Fatal("过期源应创建 30 天补偿桶", buckets)
	}
	if _, e = reserveFixture(db, uid, task, h); e != ErrBudget {
		t.Fatalf("返还不能重新打开原预算: %v", e)
	}
}
