// ai-form-backend - AGPL-3.0
package payment

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/model"
)

func setupPay(t *testing.T) (*gin.Engine, *gorm.DB, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)
	// 易支付故意不配置:套餐校验与 pack 门禁在拉起支付之前,403/400 与 503 可区分门禁是否放行
	h := New(db, config.EpayConfig{}, "http://cb", "http://console")
	r := gin.New()
	r.POST("/pay", func(c *gin.Context) {
		c.Set("userID", u.ID)
		c.Next()
	}, h.CreateOrder)
	return r, db, u.ID
}

func postPay(r *gin.Engine, planID int64) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	body := `{"planId":` + strconv.FormatInt(planID, 10) + `,"method":"alipay"}`
	req := httptest.NewRequest("POST", "/pay", strings.NewReader(body))
	r.ServeHTTP(w, req)
	return w
}

// 无有效 base 订阅时不允许购买加油包
func TestPackRequiresActiveBase(t *testing.T) {
	r, db, uid := setupPay(t)
	pack := model.SubscriptionPlan{Name: "加油包", PlanType: model.PlanTypePack, PriceCents: 1000, TotalCredits: 1000, DurationDays: 365, ForSale: true}
	db.Create(&pack)

	w := postPay(r, pack.ID)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "BASE_REQUIRED") {
		t.Fatalf("无底座应 403 BASE_REQUIRED,实际 %d: %s", w.Code, w.Body.String())
	}

	// 试用桶不算底座:仍拒绝
	db.Create(&model.UserSubscription{
		UserID: uid, PlanID: 99, PlanType: model.PlanTypeTrial, AmountTotal: 500,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	w = postPay(r, pack.ID)
	if w.Code != 403 {
		t.Fatalf("仅试用桶应仍 403,实际 %d: %s", w.Code, w.Body.String())
	}

	// 有 active base 桶:门禁放行(随后因支付未配置 503,证明已越过门禁)
	db.Create(&model.UserSubscription{
		UserID: uid, PlanID: 98, PlanType: model.PlanTypeBase, AmountTotal: 500,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	w = postPay(r, pack.ID)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "PAYMENT_UNCONFIGURED") {
		t.Fatalf("有底座应越过门禁到支付配置检查(503),实际 %d: %s", w.Code, w.Body.String())
	}
}

// 试用与赠送套餐不可购买
func TestTrialAndBonusNotSellable(t *testing.T) {
	r, db, _ := setupPay(t)
	trial := model.SubscriptionPlan{Name: "试用", PlanType: model.PlanTypeTrial, PriceCents: 100, TotalCredits: 500, DurationDays: 14, ForSale: true}
	bonus := model.SubscriptionPlan{Name: "首充礼", PlanType: model.PlanTypeBonus, PriceCents: 100, TotalCredits: 1500, DurationDays: 60, ForSale: true}
	db.Create(&trial)
	db.Create(&bonus)
	for _, id := range []int64{trial.ID, bonus.ID} {
		w := postPay(r, id)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "PLAN_NOT_FOR_SALE") {
			t.Fatalf("套餐 %d 应不可购买,实际 %d: %s", id, w.Code, w.Body.String())
		}
	}
}
