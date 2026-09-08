package server

import (
	"encoding/json"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/subscription"
	"github.com/google/uuid"
	"testing"
)

func TestBillingQuoteAuthorizationAndPermissions(t *testing.T) {
	router, db, token := setupServer(t)
	uid := userID(t, db)
	giveBucket(t, db, uid, 100)
	if err := subscription.SeedDefaults(db); err != nil {
		t.Fatal(err)
	}
	w := do(t, router, token, "GET", "/v1/billing/catalog", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var catalog struct {
		Capabilities []struct {
			Mode string `json:"mode"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	var paid, included int
	for _, r := range catalog.Capabilities {
		if r.Mode == "per_call" {
			paid++
		} else if r.Mode == "included" {
			included++
		}
	}
	if paid != 2 || included != 21 {
		t.Fatalf("分类 paid=%d included=%d", paid, included)
	}
	task := uuid.NewString()
	w = do(t, router, token, "POST", "/v1/billing/quotes", `{"taskId":"`+task+`","matchCalls":1,"generateCalls":2}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var quote struct {
		QuoteID   string `json:"quoteId"`
		Estimated int    `json:"estimated"`
	}
	json.Unmarshal(w.Body.Bytes(), &quote)
	if quote.Estimated != 52 {
		t.Fatal(w.Body.String())
	}
	w = do(t, router, token, "POST", "/v1/billing/authorizations", `{"quoteId":"`+quote.QuoteID+`","maxTotal":52}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = do(t, router, token, "POST", "/v1/billing/quotes", `{"taskId":"`+task+`","matchCalls":-1}`)
	if w.Code != 400 {
		t.Fatal("无效报价必须 400", w.Body.String())
	}
	w = do(t, router, token, "POST", "/v1/admin/billing/refunds", `{}`)
	if w.Code != 403 {
		t.Fatal("普通用户不能返还积分", w.Body.String())
	}
	if err := db.Model(&model.User{}).Where("id = ?", uid).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	w = do(t, router, token, "PUT", "/v1/admin/capability-prices/match_columns", `{"credits":0,"enabled":true}`)
	if w.Code != 400 {
		t.Fatal("付费能力不能静默调为 0", w.Body.String())
	}
	w = do(t, router, token, "PUT", "/v1/admin/capability-prices/agent_step", `{"credits":1,"enabled":true}`)
	if w.Code != 400 {
		t.Fatal("订阅包含能力不能意外收费", w.Body.String())
	}
}
