package credits

import (
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 独立 PostgreSQL 16 验收库；每次保留独立 schema，绝不接生产库或删历史数据。
func TestPostgresBillingConcurrency(t *testing.T) {
	dsn := os.Getenv("AIFORM_BILLING_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式设置隔离 PostgreSQL 验收库")
	}
	if !strings.Contains(dsn, "aiform_billing_test") {
		t.Fatal("只允许专用验收库")
	}
	initial, err := model.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "billing_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = initial.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	db, err := model.Open(dsn + " search_path=" + schema)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sql, _ := db.DB(); sql.Close(); sql, _ = initial.DB(); sql.Close() }()
	u := model.User{Email: uuid.NewString() + "@test.local", Status: "active"}
	if err = db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	for _, amount := range []int64{40, 60} {
		b := model.UserSubscription{UserID: u.ID, PlanType: model.PlanTypeBase, AmountTotal: amount, StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive}
		if err = db.Create(&b).Error; err != nil {
			t.Fatal(err)
		}
	}
	for cap, price := range map[string]int64{"match_columns": 50, "generate_field": 1} {
		if err = db.Create(&model.CapabilityPrice{Capability: cap, Credits: price, BillingMode: ModePerCall, PriceVersion: 1, Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	task := uuid.NewString()
	q, err := Quote(db, u.ID, task, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Confirm(db, u.ID, q.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var accepted []*model.AIRequest
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := reserveFixture(db, u.ID, task, h)
			if e == nil {
				mu.Lock()
				accepted = append(accepted, r)
				mu.Unlock()
			} else if e != ErrBudget {
				t.Errorf("并发准入错误 %v", e)
			}
		}()
	}
	wg.Wait()
	if len(accepted) != 2 {
		t.Fatalf("预算只允许 2 次，实际 %d", len(accepted))
	}
	if err = Settle(db, u.ID, h.ID); err != nil {
		t.Fatal(err)
	}
	if n, e := Available(db, u.ID); e != nil || n != 0 {
		t.Fatalf("关闭任务不能释放在途资金 %d %v", n, e)
	}
	if err = db.Model(&model.UserSubscription{}).Where("user_id = ?", u.ID).Update("ends_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range accepted {
		wg.Add(1)
		go func(r *model.AIRequest) {
			defer wg.Done()
			e := db.Transaction(func(tx *gorm.DB) error {
				if e := model.LockUser(tx, u.ID); e != nil {
					return e
				}
				if e := DebitReserved(tx, r); e != nil {
					return e
				}
				r.Status = SettlementPending
				if e := tx.Save(r).Error; e != nil {
					return e
				}
				r.Status = model.AIReqOK
				return tx.Save(r).Error
			})
			if e != nil {
				t.Errorf("跨桶自然过期结算 %v", e)
			}
		}(r)
	}
	wg.Wait()
	totals, e := Totals(db, u.ID, task)
	if e != nil || totals.Charged != 100 || totals.InFlight != 0 {
		t.Fatalf("并发结算 %+v %v", totals, e)
	}
	var succeeded atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := Refund(db, u.ID, u.ID, accepted[0].RequestID, uuid.NewString(), "并发退还", 10)
			if e == nil {
				succeeded.Add(1)
			} else if e != ErrRefundAmount {
				t.Errorf("退款 %v", e)
			}
		}()
	}
	wg.Wait()
	if succeeded.Load() != 5 {
		t.Fatalf("最多只能返还 50，成功 %d 次", succeeded.Load())
	}
	totals, e = Totals(db, u.ID, task)
	if e != nil || totals.Charged != 100 || totals.Refunded != 50 || totals.Net != 50 {
		t.Fatalf("并发退款账本 %+v %v", totals, e)
	}
	if err = model.Migrate(db); err != nil {
		t.Fatal("重复迁移", err)
	}
	t.Logf("PostgreSQL 并发、跨桶、过期、退款和重复迁移通过；证据 schema=%s", schema)
}
