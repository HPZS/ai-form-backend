package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
)

func TestModelPolicyBlocksBeforeAdmission(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream, true)
	body := strings.Replace(matchBody, `"requestId":`, `"modelPolicy":"deterministic-only","requestId":`, 1)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "ai-required") {
		t.Fatal(w.Body.String())
	}
	var count int64
	if err := db.Model(&model.AIRequest{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || count != 0 {
		t.Fatal("仅程序策略不得准入计费或请求模型")
	}
}

func TestCompileAdapterGatewayRepairReplayAndInvalidFree(t *testing.T) {
	req := adapterRequest()
	req.BillingProtocolVersion = 2
	valid, err := json.Marshal(adapterOutput(req))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var fail atomic.Bool
	var repaired atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		// ChatMessage 的生产编码器没有自定义解码器，这里直接检查线上的消息文本。
		var wire struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		if n == 2 && strings.Contains(wire.Messages[len(wire.Messages)-1].Content, "compile-1") {
			repaired.Store(true)
		}
		if n == 1 || fail.Load() {
			chatOK(`{"schemaVersion":1}`)(w, r)
			return
		}
		chatOK(string(valid))(w, r)
	}))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream, true)
	run := func() *httptest.ResponseRecorder {
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/compile-adapter", strings.NewReader(string(data))))
		return w
	}
	if w := run(); w.Code != 200 || !strings.Contains(w.Body.String(), `"modelCalls":2`) {
		t.Fatal(w.Body.String())
	}
	if !repaired.Load() {
		t.Fatal("技术修复没有重新绑定当前 compilationId")
	}
	if w := run(); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatal("原请求重放再次请求模型")
	}
	req.SourceContractDigest = strings.Repeat("c", 64)
	if w := run(); w.Code != 409 || !strings.Contains(w.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatal(w.Body.String())
	}
	req.SourceContractDigest = strings.Repeat("a", 64)
	// 相同能力动态改成按次收费，缺授权必须在模型前拒绝，不能沿用 included 的配置。
	if err := db.Model(&model.CapabilityPrice{}).Where("capability = ?", "compile_adapter").Updates(map[string]any{"billing_mode": credits.ModePerCall, "credits": 3, "price_version": 2}).Error; err != nil {
		t.Fatal(err)
	}
	req.RequestID = "00000000-0000-4000-8000-000000000002"
	if w := run(); w.Code != 402 {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatal("未授权请求到达模型")
	}
	if err := db.Model(&model.CapabilityPrice{}).Where("capability = ?", "compile_adapter").Updates(map[string]any{"billing_mode": credits.ModeIncluded, "credits": 0, "price_version": 3}).Error; err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	if w := run(); w.Code != 422 {
		t.Fatal(w.Body.String())
	}
	var failed model.AIRequest
	if err := db.Where("user_id = ? AND request_id = ?", uid, req.RequestID).First(&failed).Error; err != nil {
		t.Fatal(err)
	}
	if failed.Status != credits.RequestFailed || failed.Credits != 0 || failed.CallPhase != "adapter-create" || failed.CompilationID != "compile-1" || requestModelCalls(&failed) == nil || *requestModelCalls(&failed) != 2 {
		t.Fatalf("失败阶段、身份或真实用量不完整: %+v", failed)
	}
}

func TestCallPhasePersistsWithRealAttempts(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			chatOK(`无效输出`)(w, r)
			return
		}
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, _, task, authorization := v2Fixture(t, upstream)
	var payload map[string]any
	if err := json.Unmarshal([]byte(v2Body(t, matchBody, task, authorization)), &payload); err != nil {
		t.Fatal(err)
	}
	payload["callPhase"], payload["handoffId"], payload["modelPolicy"] = "runtime-handoff", "handoff-1", "adaptive"
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(string(data))))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var out struct {
			Meta struct {
				CallPhase  string `json:"callPhase"`
				HandoffID  string `json:"handoffId"`
				ModelCalls int    `json:"modelCalls"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Meta.CallPhase != "runtime-handoff" || out.Meta.HandoffID != "handoff-1" || out.Meta.ModelCalls != 2 {
			t.Fatalf("真实调用元信息丢失: %+v", out)
		}
	}
	var row map[string]any
	if err := db.Model(&model.AIRequest{}).Where("task_id = ?", task).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row["call_phase"] != "runtime-handoff" || row["handoff_id"] != "handoff-1" || calls.Load() != 2 {
		t.Fatalf("重放必须保留阶段及真实用量: %v calls=%d", row, calls.Load())
	}
}
