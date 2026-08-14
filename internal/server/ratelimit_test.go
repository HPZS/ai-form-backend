// ai-form-backend - AGPL-3.0
package server

import (
	"net/http/httptest"
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
