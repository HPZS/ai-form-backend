package ai

import (
	"encoding/json"
	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDynamicRuleBillingSnapshotReplayAndFailure(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	var change func()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if change != nil {
			change()
			change = nil
		}
		if fail.Load() {
			chatOK("无有效代码")(w, r)
			return
		}
		chatOK("function transform(row) { return row['姓名']; }")(w, r)
	}))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream, true)
	task := uuid.NewString()
	update := func(mode string, amount, version int64) {
		t.Helper()
		if err := db.Model(&model.CapabilityPrice{}).Where("capability = ?", "generate_rule").Updates(map[string]any{"billing_mode": mode, "credits": amount, "price_version": version, "enabled": false}).Error; err != nil {
			t.Fatal(err)
		}
	}
	body := func(auth string) string {
		b, _ := json.Marshal(map[string]any{"requestId": uuid.NewString(), "taskId": task, "billingProtocolVersion": 2, "authorizationVersion": auth, "field": map[string]any{"index": 0, "label": "姓名", "tag": "input", "type": "text"}, "headers": []string{"姓名"}, "requirement": "复制姓名", "sampleRows": []map[string]string{{"姓名": "张三"}}})
		return string(b)
	}
	call := func(b string, status int, price int64) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/generate-rule", strings.NewReader(b)))
		if w.Code != status {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if status == 200 {
			var data struct {
				Billing struct{ SettledCredits int64 }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
			if data.Billing.SettledCredits != price {
				t.Fatalf("错误扣费: %s", w.Body.String())
			}
		}
	}
	authorize := func() string {
		t.Helper()
		q, e := credits.QuoteCapabilities(db, uid, task, map[string]int64{"generate_rule": 1})
		if e != nil {
			t.Fatal(e)
		}
		h, e := credits.Confirm(db, uid, q.ID, q.Amount)
		if e != nil {
			t.Fatal(e)
		}
		return h.ID
	}
	free := body("")
	call(free, 200, 0)
	update("per_call", 3, 2)
	rejected := body("")
	call(rejected, 402, 0)
	if hits.Load() != 1 {
		t.Fatal("无授权请求调用了模型")
	}
	auth := authorize()
	paid := strings.Replace(rejected, `"authorizationVersion":""`, `"authorizationVersion":"`+auth+`"`, 1)
	// 上游已开始执行后修改模式，当前请求仍按准入时价格结算。
	change = func() { update("included", 0, 3) }
	call(paid, 200, 3)
	call(paid, 200, 3)
	call(free, 200, 0)
	call(body(""), 200, 0)
	if hits.Load() != 3 {
		t.Fatal("重放重复调用模型", hits.Load())
	}
	update("per_call", 7, 4)
	auth = authorize()
	fail.Store(true)
	call(body(auth), 422, 0)
	totals, e := credits.Totals(db, uid, task)
	if e != nil || totals.Charged != 3 || totals.InFlight != 0 {
		t.Fatalf("失败应释放预留: %+v %v", totals, e)
	}
}
