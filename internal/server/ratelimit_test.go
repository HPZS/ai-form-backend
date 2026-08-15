// ai-form-backend - AGPL-3.0
package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/ratelimit"
)

func TestHasActiveBase(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)

	if got, _ := hasActiveBase(db, u.ID); got {
		t.Fatal("无桶用户不应视为有底座")
	}
	// 试用桶不算底座
	db.Create(&model.UserSubscription{
		UserID: u.ID, PlanID: 1, PlanType: model.PlanTypeTrial, AmountTotal: 500,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	if got, _ := hasActiveBase(db, u.ID); got {
		t.Fatal("试用桶不应视为有底座")
	}
	// 过期的 base 桶不算
	db.Create(&model.UserSubscription{
		UserID: u.ID, PlanID: 2, PlanType: model.PlanTypeBase, AmountTotal: 500,
		StartsAt: time.Now().Add(-2 * time.Hour), EndsAt: time.Now().Add(-time.Hour), Status: model.SubStatusActive,
	})
	if got, _ := hasActiveBase(db, u.ID); got {
		t.Fatal("已过期 base 桶不应视为有底座")
	}
	db.Create(&model.UserSubscription{
		UserID: u.ID, PlanID: 2, PlanType: model.PlanTypeBase, AmountTotal: 500,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	if got, _ := hasActiveBase(db, u.ID); !got {
		t.Fatal("有效 base 桶应视为有底座")
	}
}

// 每能力每分钟限流:60 次内放行,第 61 次 429
func TestAiRateLimitPerMinute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)
	s := &Server{db: db, limiter: ratelimit.New()}

	r := gin.New()
	r.POST("/x", func(c *gin.Context) {
		c.Set("userID", u.ID)
		c.Set("email", u.Email)
		c.Next()
	}, s.aiRateLimit("assess_page"), func(c *gin.Context) { c.Status(204) })

	for i := 0; i < 60; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
		if w.Code != 204 {
			t.Fatalf("第 %d 次应放行,实际 %d: %s", i+1, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
	if w.Code != 429 {
		t.Fatalf("第 61 次应 429,实际 %d", w.Code)
	}
}

// aiRouter 组一个只带限流中间件的路由,身份由测试直接注入。
func aiRouter(t *testing.T, s *Server, u model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/x", func(c *gin.Context) {
		c.Set("userID", u.ID)
		c.Set("email", u.Email)
		c.Next()
	}, s.aiRateLimit("assess_page"), func(c *gin.Context) { c.Status(204) })
	return r
}

// 试用档(无付费底座)日限:额度用完后 429,并给出"开通个人版"的引导文案
func TestAiTrialDailyLimit(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "trial@example.com"}
	db.Create(&u)
	s := &Server{db: db, limiter: ratelimit.New()}
	// 直接把试用日窗口吃满(走中间件要 1000 次请求,且会先撞每分钟 60 的闸门)
	for i := 0; i < aiTrialPerDay; i++ {
		s.limiter.Allow("ai-day-trial:"+u.Email, aiTrialPerDay, 24*time.Hour)
	}
	w := httptest.NewRecorder()
	aiRouter(t, s, u).ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
	if w.Code != 429 {
		t.Fatalf("试用日额度用完应 429,实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "开通个人版") {
		t.Fatalf("应给出试用档专属提示,实际 %s", w.Body.String())
	}
}

// 有付费底座的用户不受试用日限约束(同样吃满试用窗口,仍应放行)
func TestAiTrialLimitSkippedWithBase(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "paid@example.com"}
	db.Create(&u)
	db.Create(&model.UserSubscription{
		UserID: u.ID, PlanID: 1, PlanType: model.PlanTypeBase, AmountTotal: 500,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	s := &Server{db: db, limiter: ratelimit.New()}
	for i := 0; i < aiTrialPerDay; i++ {
		s.limiter.Allow("ai-day-trial:"+u.Email, aiTrialPerDay, 24*time.Hour)
	}
	w := httptest.NewRecorder()
	aiRouter(t, s, u).ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
	if w.Code != 204 {
		t.Fatalf("付费底座用户不应受试用日限约束,实际 %d: %s", w.Code, w.Body.String())
	}
}

// 被试用日限拒绝的请求不得消耗每分钟配额:否则用户被拒 60 次后连分钟窗口也被吃光
func TestAiRateLimitDeniedDoesNotConsumeOthers(t *testing.T) {
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "trial2@example.com"}
	db.Create(&u)
	s := &Server{db: db, limiter: ratelimit.New()}
	for i := 0; i < aiTrialPerDay; i++ {
		s.limiter.Allow("ai-day-trial:"+u.Email, aiTrialPerDay, 24*time.Hour)
	}
	r := aiRouter(t, s, u)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
		if w.Code != 429 {
			t.Fatalf("第 %d 次应 429,实际 %d", i+1, w.Code)
		}
	}
	// 分钟窗口应仍是满的 60 次
	for i := 0; i < aiPerCapPerMinute; i++ {
		if !s.limiter.Allow("ai-min:assess_page:"+u.Email, aiPerCapPerMinute, time.Minute) {
			t.Fatalf("分钟配额第 %d 次就没了:被拒的请求吃掉了它", i+1)
		}
	}
}

// 写接口闸门:超出后 429(下单/上报无限流时脚本可无限刷单、灌统计表)
func TestUserRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "w@example.com"}
	db.Create(&u)
	s := &Server{db: db, limiter: ratelimit.New()}
	r := gin.New()
	r.POST("/x", func(c *gin.Context) {
		c.Set("userID", u.ID)
		c.Set("email", u.Email)
		c.Next()
	}, s.userRateLimit("t", 3, time.Minute), func(c *gin.Context) { c.Status(204) })

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
		if w.Code != 204 {
			t.Fatalf("第 %d 次应放行,实际 %d", i+1, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
	if w.Code != 429 {
		t.Fatalf("第 4 次应 429,实际 %d", w.Code)
	}
}

// 心跳:DB 故障与"预占不存在"必须可区分——前者若被伪装成 404,
// 插件会永久清掉 holdId 不再结算,冻结额白挂到过期
func TestHeartbeatDistinguishesDBError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)
	s := &Server{db: db, limiter: ratelimit.New()}
	r := gin.New()
	r.PATCH("/holds/:id/heartbeat", func(c *gin.Context) {
		c.Set("userID", u.ID)
		c.Next()
	}, s.heartbeat)

	// 预占不存在 → 404
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PATCH", "/holds/nope/heartbeat", nil))
	if w.Code != 404 {
		t.Fatalf("不存在的预占应 404,实际 %d: %s", w.Code, w.Body.String())
	}

	// DB 故障 → 500(不是 404)
	if err := db.Exec(`DROP TABLE credit_holds`).Error; err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PATCH", "/holds/any/heartbeat", nil))
	if w.Code != 500 {
		t.Fatalf("DB 故障应 500 而不是伪装成 404,实际 %d: %s", w.Code, w.Body.String())
	}
}
