package ai

import (
	"encoding/json"
	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
)

func TestExplicitCancelBeforeAdmissionPreventsModelCall(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, _, _, task, auth := v2Fixture(t, upstream)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/requests/11111111-1111-4111-8111-111111111111/cancel", strings.NewReader(`{"taskId":"`+task+`"}`)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"executionStatus":"cancelled"`) {
			t.Fatalf("分发前取消未确认: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(v2Body(t, matchBody, task, auth))))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "AI_CANCELLED") || calls.Load() != 0 {
		t.Fatalf("取消后仍可分发: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
	}
}

func TestCancelAfterCompletionPreservesResultAndSettlement(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, uid, task, auth := v2Fixture(t, upstream)
	body := v2Body(t, matchBody, task, auth)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	before, err := credits.Totals(db, uid, task)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/requests/11111111-1111-4111-8111-111111111111/cancel", strings.NewReader(`{"taskId":"`+task+`"}`)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"executionStatus":"completed"`) {
			t.Fatal(w.Body.String())
		}
	}
	after, err := credits.Totals(db, uid, task)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("完成后的取消改变账务: before=%+v after=%+v", before, after)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 200 || calls.Load() != 1 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatalf("结果先到必须重放原结果: %s calls=%d", w.Body.String(), calls.Load())
	}
}

func TestRecoveryPreservesCancelledOrUnknownAttempt(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "cancelled"}[cancelled], func(t *testing.T) {
			upstream := httptest.NewServer(chatOK(`{}`))
			defer upstream.Close()
			_, db, uid, task, _ := v2Fixture(t, upstream)
			attempts, err := json.Marshal([]AttemptUsage{{Status: "started", CostStatus: "unknown", ReservedTokens: 123, ReservedWaitMs: 60000}})
			if err != nil {
				t.Fatal(err)
			}
			r := model.AIRequest{UserID: uid, TaskID: task, RequestID: uuid.NewString(), Capability: "analyze_form", PolicyVersion: credits.PolicyV2, BillingMode: credits.ModeIncluded, Status: model.AIReqPending, LeaseToken: uuid.NewString(), LeaseExpiresAt: time.Now().Add(-11 * time.Minute), UsageDetails: string(attempts)}
			if err := db.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			if cancelled {
				if err := db.Create(&model.AIRequestCancellation{UserID: uid, TaskID: task, RequestID: r.RequestID, CreatedAt: time.Now()}).Error; err != nil {
					t.Fatal(err)
				}
			}
			g := &Gateway{db: db}
			if err := g.RecoverBillingV2(); err != nil {
				t.Fatal(err)
			}
			var actual model.AIRequest
			if err := db.First(&actual, r.ID).Error; err != nil {
				t.Fatal(err)
			}
			want := "AI_EXECUTION_UNCERTAIN"
			if cancelled {
				want = "AI_CANCELLED"
			}
			if actual.FailureCode != want || actual.UsageDetails != r.UsageDetails || actual.Status != credits.RequestFailed {
				t.Fatalf("重启恢复不得抹去取消/未知分发: %+v", actual)
			}
		})
	}
}

func TestExplicitCancelStopsInFlightAndPreservesUnknownUsage(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)
	router, db, _, task, auth := v2Fixture(t, upstream)
	body := v2Body(t, matchBody, task, auth)
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
		finished <- w
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("上游没有开始")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/requests/11111111-1111-4111-8111-111111111111/cancel", strings.NewReader(`{"taskId":"`+task+`"}`)))
	if w.Code != 200 && w.Code != 202 {
		t.Fatalf("取消请求失败: %d %s", w.Code, w.Body.String())
	}
	select {
	case response := <-finished:
		if !strings.Contains(response.Body.String(), "AI_CANCELLED") {
			t.Fatal(response.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消没有传播到上游")
	}
	var saved model.AIRequest
	if err := db.Where("task_id = ?", task).First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.FailureCode != "AI_CANCELLED" || saved.ReservedCredits != 0 || saved.Credits != 0 || requestModelCalls(&saved) == nil || *requestModelCalls(&saved) != 1 || requestUsageComplete(&saved) {
		t.Fatalf("取消账务或用量失真: %+v", saved)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if calls.Load() != 1 {
		t.Fatal("取消后重放重新调用模型")
	}
}
