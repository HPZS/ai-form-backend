package credits

import (
	"errors"
	"testing"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func budgetFixture(t *testing.T, amount int64) (*gorm.DB, int64, string, *model.CreditHold) {
	t.Helper()
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: uuid.NewString() + "@test.local", Status: "active"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	b := model.UserSubscription{UserID: u.ID, PlanType: model.PlanTypeBase, AmountTotal: amount, StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	for cap, price := range map[string]int64{"match_columns": 50, "generate_field": 1} {
		if err := db.Create(&model.CapabilityPrice{Capability: cap, Credits: price, Enabled: true, BillingMode: ModePerCall, PriceVersion: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	taskID := uuid.NewString()
	q, err := Quote(db, u.ID, taskID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Confirm(db, u.ID, q.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	return db, u.ID, taskID, h
}

func reserveFixture(db *gorm.DB, uid int64, task string, h *model.CreditHold) (*model.AIRequest, error) {
	r := &model.AIRequest{UserID: uid, TaskID: task, RequestID: uuid.NewString(), AuthorizationVersion: h.ID, Capability: "match_columns", PolicyVersion: PolicyV2, Status: model.AIReqPending}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, uid); err != nil {
			return err
		}
		if err := ReserveRequest(tx, r); err != nil {
			return err
		}
		return tx.Create(r).Error
	})
	return r, err
}

func TestBudgetOwnHoldAndHardLimit(t *testing.T) {
	db, uid, task, h := budgetFixture(t, 50)
	if n, err := Available(db, uid); err != nil || n != 0 {
		t.Fatalf("应全额预占: %d %v", n, err)
	}
	r, err := reserveFixture(db, uid, task, h)
	if err != nil {
		t.Fatalf("本任务应能使用自己预占: %v", err)
	}
	if n, err := Available(db, uid); err != nil || n != 0 {
		t.Fatalf("在途不能双重冻结: %d %v", n, err)
	}
	if _, err := reserveFixture(db, uid, task, h); !errors.Is(err, ErrBudget) {
		t.Fatalf("同任务必须有硬上限: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, uid); err != nil {
			return err
		}
		if err := DebitReserved(tx, r); err != nil {
			return err
		}
		r.Status = model.AIReqOK
		return tx.Save(r).Error
	}); err != nil {
		t.Fatal(err)
	}
	totals, err := Totals(db, uid, task)
	if err != nil || totals.Charged != 50 || totals.InFlight != 0 {
		t.Fatalf("汇总错误: %+v %v", totals, err)
	}
}

func TestBudgetExpiryKeepsAcceptedFunds(t *testing.T) {
	db, uid, task, h := budgetFixture(t, 100)
	r, err := reserveFixture(db, uid, task, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.UserSubscription{}).Where("user_id = ?", uid).Updates(map[string]any{"ends_at": time.Now().Add(-time.Minute), "status": model.SubStatusExpired}).Error; err != nil {
		t.Fatal(err)
	}
	if err := Settle(db, uid, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, uid); err != nil {
			return err
		}
		if err := DebitReserved(tx, r); err != nil {
			return err
		}
		r.Status = model.AIReqOK
		return tx.Save(r).Error
	}); err != nil {
		t.Fatalf("自然到期不撤销已接受承诺: %v", err)
	}
	if r.Credits != 50 {
		t.Fatal(r.Credits)
	}
}

func TestBudgetFailureReleasesAndVersionRejects(t *testing.T) {
	db, uid, task, h := budgetFixture(t, 100)
	r, err := reserveFixture(db, uid, task, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, uid); err != nil {
			return err
		}
		if err := ReleaseRequest(tx, r); err != nil {
			return err
		}
		r.Status = RequestFailed
		return tx.Save(r).Error
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveFixture(db, uid, task, h); err != nil {
		t.Fatalf("失败释放预算: %v", err)
	}
	q, err := Quote(db, uid, task, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CapabilityPrice{}).Where("capability = ?", "match_columns").Updates(map[string]any{"credits": 60, "price_version": 2}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := Confirm(db, uid, q.ID, q.Amount); !errors.Is(err, ErrQuoteExpired) {
		t.Fatalf("旧报价不能按新价确认: %v", err)
	}
}
