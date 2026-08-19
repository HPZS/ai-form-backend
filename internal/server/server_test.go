// ai-form-backend - AGPL-3.0
package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/auth"
	"github.com/HPZS/ai-form-backend/internal/email"
	"github.com/HPZS/ai-form-backend/internal/model"
)

// setupServer 组一个完整路由(含认证中间件),返回路由、库与一个可用的 access token。
// gateway 传 nil:AI 路由只在注册时取闭包,本文件不打这些路径。
func setupServer(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	authSvc := auth.New(db, "test-secret", "test-pepper", nil)
	code, err := authSvc.IssueCode("u@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, pair, err := authSvc.Login("u@example.com", code)
	if err != nil {
		t.Fatal(err)
	}
	r := New(db, &config.Config{}, authSvc, email.New(config.SMTPConfig{}), nil, nil)
	return r, db, pair.AccessToken
}

func do(t *testing.T, r *gin.Engine, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	return w
}

func userID(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var u model.User
	if err := db.First(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func giveBucket(t *testing.T, db *gorm.DB, uid, amount int64) {
	t.Helper()
	err := db.Create(&model.UserSubscription{
		UserID: uid, PlanID: 1, PlanType: model.PlanTypeBase, AmountTotal: amount,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	}).Error
	if err != nil {
		t.Fatal(err)
	}
}

// 预占端点全链路:创建 → 幂等重复创建 → 心跳 → 结算 → 重复结算幂等
func TestHoldEndpoints(t *testing.T) {
	r, db, token := setupServer(t)
	giveBucket(t, db, userID(t, db), 100)

	w := do(t, r, token, "POST", "/v1/credits/holds", `{"taskId":"t-1","amount":60}`)
	if w.Code != 200 {
		t.Fatalf("创建预占应 200,实际 %d: %s", w.Code, w.Body.String())
	}
	var hold model.CreditHold
	if err := db.First(&hold).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), hold.ID) {
		t.Fatalf("响应应含 holdId: %s", w.Body.String())
	}
	// 同任务重复创建返回同一个 hold(幂等)
	w = do(t, r, token, "POST", "/v1/credits/holds", `{"taskId":"t-1","amount":60}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), hold.ID) {
		t.Fatalf("重复创建应返回同一 hold,实际 %d: %s", w.Code, w.Body.String())
	}

	w = do(t, r, token, "PATCH", "/v1/credits/holds/"+hold.ID+"/heartbeat", "")
	if w.Code != 200 {
		t.Fatalf("心跳应 200,实际 %d: %s", w.Code, w.Body.String())
	}
	w = do(t, r, token, "POST", "/v1/credits/holds/"+hold.ID+"/settle", "")
	if w.Code != 204 {
		t.Fatalf("结算应 204,实际 %d: %s", w.Code, w.Body.String())
	}
	// 重复结算:已结算 = 幂等成功
	w = do(t, r, token, "POST", "/v1/credits/holds/"+hold.ID+"/settle", "")
	if w.Code != 204 {
		t.Fatalf("重复结算应幂等 204,实际 %d: %s", w.Code, w.Body.String())
	}
}

// 结算一个从不存在的预占必须 404:一律 204 会把 holdId 串号/传错当成结算成功,
// 冻结额挂到过期为止都没人知道
func TestSettleUnknownHoldIs404(t *testing.T) {
	r, _, token := setupServer(t)
	w := do(t, r, token, "POST", "/v1/credits/holds/根本没有这个单号/settle", "")
	if w.Code != 404 {
		t.Fatalf("不存在的预占应 404,实际 %d: %s", w.Code, w.Body.String())
	}
}

// 预占额超过可用余额 → 402
func TestCreateHoldInsufficient(t *testing.T) {
	r, db, token := setupServer(t)
	giveBucket(t, db, userID(t, db), 10)
	w := do(t, r, token, "POST", "/v1/credits/holds", `{"taskId":"t-2","amount":50}`)
	if w.Code != 402 {
		t.Fatalf("余额不足应 402,实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "INSUFFICIENT_CREDITS") {
		t.Fatalf("应给出 INSUFFICIENT_CREDITS: %s", w.Body.String())
	}
}

// 预估:公式 = 新方案数 × 方案费 + AI 格数 × 格费
func TestEstimate(t *testing.T) {
	r, db, token := setupServer(t)
	uid := userID(t, db)
	giveBucket(t, db, uid, 300)
	db.Create(&model.CapabilityPrice{Capability: "match_columns", Credits: 50, Enabled: true})
	db.Create(&model.CapabilityPrice{Capability: "generate_field", Credits: 1, Enabled: true})

	w := do(t, r, token, "POST", "/v1/credits/estimate", `{"taskId":"t","newPlans":1,"aiCells":20}`)
	if w.Code != 200 {
		t.Fatalf("应 200,实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"estimated":70`) {
		t.Fatalf("50 + 20 = 70,实际 %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"available":300`) {
		t.Fatalf("应带上可用余额,实际 %s", w.Body.String())
	}
}

// 预估入参必须非负:负数会算出负预估,前端据此显示"这次不花钱"
func TestEstimateRejectsNegative(t *testing.T) {
	r, db, token := setupServer(t)
	giveBucket(t, db, userID(t, db), 300)
	db.Create(&model.CapabilityPrice{Capability: "match_columns", Credits: 50, Enabled: true})

	for _, body := range []string{
		`{"taskId":"t","newPlans":-1,"aiCells":0}`,
		`{"taskId":"t","newPlans":0,"aiCells":-100}`,
	} {
		w := do(t, r, token, "POST", "/v1/credits/estimate", body)
		if w.Code != 400 {
			t.Fatalf("%s 应 400,实际 %d: %s", body, w.Code, w.Body.String())
		}
	}
}

// 单价查询出错不能静默按 0 算:用户会被告知"这次不花钱",随后真扣
func TestEstimateDBErrorNotZero(t *testing.T) {
	r, db, token := setupServer(t)
	giveBucket(t, db, userID(t, db), 300)
	if err := db.Exec(`DROP TABLE capability_prices`).Error; err != nil {
		t.Fatal(err)
	}
	w := do(t, r, token, "POST", "/v1/credits/estimate", `{"taskId":"t","newPlans":1,"aiCells":0}`)
	if w.Code != 500 {
		t.Fatalf("单价查询失败应 500,实际 %d: %s", w.Code, w.Body.String())
	}
}

// 封禁用户全站 403(认证中间件出口)
func TestBannedUserForbidden(t *testing.T) {
	r, db, token := setupServer(t)
	db.Model(&model.User{}).Where("id = ?", userID(t, db)).Update("status", "banned")
	w := do(t, r, token, "GET", "/v1/me", "")
	if w.Code != 403 {
		t.Fatalf("封禁用户应 403,实际 %d: %s", w.Code, w.Body.String())
	}
}

// 非管理员访问管理端点 403
func TestAdminRequired(t *testing.T) {
	r, _, token := setupServer(t)
	w := do(t, r, token, "GET", "/v1/admin/plans", "")
	if w.Code != 403 {
		t.Fatalf("普通用户访问管理端点应 403,实际 %d: %s", w.Code, w.Body.String())
	}
}

// 流水页码分页:total 准确、按 id 倒序、page/pageSize 生效、越界页返回空而不是报错
func TestLedgerPagination(t *testing.T) {
	r, db, token := setupServer(t)
	uid := userID(t, db)
	for i := int64(1); i <= 25; i++ {
		if err := db.Create(&model.CreditLedger{UserID: uid, SubscriptionID: 1, Capability: "match_columns", Delta: -i, BalanceAfter: 1000 - i, CreatedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 别人的流水不该混进来
	if err := db.Create(&model.CreditLedger{UserID: uid + 999, SubscriptionID: 1, Delta: -1, BalanceAfter: 1, CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	w := do(t, r, token, "GET", "/v1/credits/ledger?page=1&pageSize=10", "")
	if w.Code != 200 {
		t.Fatalf("第 1 页应 200,实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"total":25`) || !strings.Contains(body, `"page":1`) || !strings.Contains(body, `"pageSize":10`) {
		t.Fatalf("分页元信息不对: %s", body)
	}
	if strings.Count(body, `"Capability"`) != 10 {
		t.Fatalf("第 1 页应 10 条: %s", body)
	}
	// 倒序:第 1 页首条是最后插入的(Delta=-25)
	if !strings.Contains(body, `"Delta":-25`) || strings.Contains(body, `"Delta":-15`) {
		t.Fatalf("第 1 页应是最新 10 条(-25..-16): %s", body)
	}

	w = do(t, r, token, "GET", "/v1/credits/ledger?page=3&pageSize=10", "")
	if strings.Count(w.Body.String(), `"Capability"`) != 5 {
		t.Fatalf("第 3 页应余 5 条: %s", w.Body.String())
	}
	w = do(t, r, token, "GET", "/v1/credits/ledger?page=9", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"entries":[]`) {
		t.Fatalf("越界页应 200 + 空数组: %d %s", w.Code, w.Body.String())
	}
	// 非法参数回退默认值,不 400
	w = do(t, r, token, "GET", "/v1/credits/ledger?page=abc&pageSize=-5", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"page":1`) || !strings.Contains(w.Body.String(), `"pageSize":20`) {
		t.Fatalf("非法分页参数应回退默认: %d %s", w.Code, w.Body.String())
	}
}

// 在售套餐接口:只返回 for_sale 套餐,并附带首充礼额度(未配置时为 null)
func TestPlansIncludeFirstPurchaseBonus(t *testing.T) {
	r, db, token := setupServer(t)
	w := do(t, r, token, "GET", "/v1/subscription/plans", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"firstPurchaseBonus":null`) {
		t.Fatalf("未配置首充礼应返回 null: %d %s", w.Code, w.Body.String())
	}
	plans := []model.SubscriptionPlan{
		{Name: "个人版", PlanType: model.PlanTypeBase, PriceCents: 1990, TotalCredits: 500, DurationDays: 30, ForSale: true},
		{Name: "首充礼", PlanType: model.PlanTypeBonus, TotalCredits: 1500, DurationDays: 60, ForSale: false},
	}
	if err := db.Create(&plans).Error; err != nil {
		t.Fatal(err)
	}
	// ForSale 列带 default:true,零值会被 gorm 当成"未设置"——和播种代码一样显式改回不可售
	if err := db.Model(&model.SubscriptionPlan{}).Where("plan_type = ?", model.PlanTypeBonus).Update("for_sale", false).Error; err != nil {
		t.Fatal(err)
	}
	w = do(t, r, token, "GET", "/v1/subscription/plans", "")
	body := w.Body.String()
	if !strings.Contains(body, `"firstPurchaseBonus":{"credits":1500,"durationDays":60}`) {
		t.Fatalf("应带首充礼额度: %s", body)
	}
	if strings.Contains(body, `"name":"首充礼"`) {
		t.Fatalf("不可售的首充礼不该出现在套餐列表里: %s", body)
	}
}
