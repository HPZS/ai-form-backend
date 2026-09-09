package server

import (
	"encoding/json"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/subscription"
	"github.com/google/uuid"
	"testing"
)

func TestDynamicCapabilityBillingAndLiveCatalog(t *testing.T) {
	router, db, token := setupServer(t)
	uid := userID(t, db)
	giveBucket(t, db, uid, 100)
	if err := subscription.SeedDefaults(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.User{}).Where("id = ?", uid).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	update := func(cap, body string) {
		t.Helper()
		w := do(t, router, token, "PUT", "/v1/admin/capability-prices/"+cap, body)
		if w.Code != 204 {
			t.Fatalf("改计费方式失败: %d %s", w.Code, w.Body.String())
		}
	}
	update("generate_rule", `{"billingMode":"per_call","credits":3,"enabled":true}`)
	check := func(cap, mode string, price int) {
		t.Helper()
		w := do(t, router, token, "GET", "/v1/billing/catalog", "")
		var data struct {
			Capabilities []struct {
				Capability, Name, Mode string
				Credits                int
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("公开价格不能缓存旧配置")
		}
		for _, item := range data.Capabilities {
			if item.Capability == cap {
				if item.Mode != mode || item.Credits != price || item.Name == "" {
					t.Fatalf("目录没有同步: %+v", item)
				}
				return
			}
		}
		t.Fatal("目录缺能力")
	}
	check("generate_rule", "per_call", 3)
	task := uuid.NewString()
	w := do(t, router, token, "POST", "/v1/billing/quotes", `{"taskId":"`+task+`","calls":{"generate_rule":2}}`)
	var q struct {
		QuoteID   string
		Estimated int64
	}
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || q.Estimated != 6 {
		t.Fatalf("通用报价错误 %d %s", w.Code, w.Body.String())
	}
	update("generate_rule", `{"billingMode":"included","credits":0,"enabled":true}`)
	check("generate_rule", "included", 0)
	w = do(t, router, token, "POST", "/v1/billing/authorizations", `{"quoteId":"`+q.QuoteID+`","maxTotal":6}`)
	if w.Code != 409 {
		t.Fatal("切换计费方式后旧报价必须失效", w.Body.String())
	}
	update("match_columns", `{"billingMode":"included","credits":0,"enabled":true}`)
	check("match_columns", "included", 0)
	for _, body := range []string{`{"enabled":false,"billingMode":"included","credits":0}`, `{"billingMode":"included","credits":1}`, `{"billingMode":"per_call","credits":0}`, `{"billingMode":"other","credits":1}`} {
		w = do(t, router, token, "PUT", "/v1/admin/capability-prices/generate_rule", body)
		if w.Code != 400 {
			t.Fatal("非法模式价格组合必须拒绝", w.Body.String())
		}
	}
}
