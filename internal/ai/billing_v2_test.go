package ai

import (
	"encoding/json"
	"errors"
	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestV2ReplayConflictAndExpiredResult(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, _, task, auth := v2Fixture(t, upstream)
	body := v2Body(t, matchBody, task, auth)
	run := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
		return w
	}
	if w := run(body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := run(body); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("重放再次执行模型")
	}
	if w := run(strings.ReplaceAll(body, "张三", "李四")); w.Code != 409 || !strings.Contains(w.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatal(w.Body.String())
	}
	if err := db.Model(&model.AIRequest{}).Where("task_id = ?", task).Update("cache_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if w := run(body); w.Code != 410 {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("结果过期重新推理")
	}
}

func TestV2SettlementRecoveryDoesNotCallModelAgain(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, uid, task, auth := v2Fixture(t, upstream)
	if err := db.Callback().Create().Before("gorm:create").Register("fail-ledger", func(tx *gorm.DB) {
		if fail.Load() && tx.Statement.Table == "credit_ledgers" {
			tx.AddError(errors.New("注入落账失败"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	body := v2Body(t, matchBody, task, auth)
	run := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
		return w
	}
	if w := run(); w.Code != 503 {
		t.Fatal(w.Body.String())
	}
	totals, err := credits.Totals(db, uid, task)
	if err != nil || totals.Charged != 0 || totals.InFlight != 5 {
		t.Fatalf("失败不留半笔账: %+v %v", totals, err)
	}
	fail.Store(false)
	if w := run(); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("恢复结算重新推理")
	}
	totals, err = credits.Totals(db, uid, task)
	if err != nil || totals.Charged != 5 || totals.InFlight != 0 {
		t.Fatalf("恢复失败: %+v %v", totals, err)
	}
}

func TestV2OldClientCannotStartPaidCall(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{}`))
	defer upstream.Close()
	router, _, _ := setupGateway(t, upstream, true)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 426 {
		t.Fatal(w.Body.String())
	}
}

func v2Fixture(t *testing.T, upstream *httptest.Server) (*gin.Engine, *gorm.DB, int64, string, string) {
	t.Helper()
	router, db, uid := setupGateway(t, upstream, true)
	task := uuid.NewString()
	q, err := credits.Quote(db, uid, task, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := credits.Confirm(db, uid, q.ID, q.Amount)
	if err != nil {
		t.Fatal(err)
	}
	return router, db, uid, task, h.ID
}

func v2Body(t *testing.T, body, task, authorization string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	m["taskId"], m["authorizationVersion"], m["billingProtocolVersion"] = task, authorization, 2
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 新请求即使内容相同，也不能继承旧方案的永久免单资格。
func TestV2SameContentNewRequestChargesAgain(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`))
	defer upstream.Close()
	router, _, _, task, authorization := v2Fixture(t, upstream)
	for _, id := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		body := v2Body(t, strings.ReplaceAll(matchBody, "11111111-1111-4111-8111-111111111111", id), task, authorization)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ai/match-columns", strings.NewReader(body)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"charged":5`) {
			t.Fatalf("新调用必须按次扣费: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestV2RepairContextIsNotFreePass(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`))
	defer upstream.Close()
	router, _, _, task, authorization := v2Fixture(t, upstream)
	body := v2Body(t, strings.Replace(matchBody, `"fields"`, `"context":"repair","fields"`, 1), task, authorization)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"charged":5`) {
		t.Fatalf("repair 不能免单: %d %s", w.Code, w.Body.String())
	}
}
