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
