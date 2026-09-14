package ai

import (
	"encoding/json"
	"github.com/HPZS/ai-form-backend/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUsageCompletenessDoesNotInferZeroCost(t *testing.T) {
	for _, tc := range []struct {
		details  string
		complete bool
	}{
		{"", false}, {`[]`, true}, {`[{"status":"failed","inputTokens":0,"outputTokens":0}]`, false},
		{`[{"status":"responded","inputTokens":10,"outputTokens":5}]`, false},
		{`[{"status":"responded","usageKnown":true,"inputTokens":10,"outputTokens":5}]`, true},
	} {
		if requestUsageComplete(&model.AIRequest{UsageDetails: tc.details}) != tc.complete {
			t.Fatalf("未知用量不能标为完整: %s", tc.details)
		}
	}
}

func TestV2FailurePreservesUpstreamCategoryAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name           string
		upstreamStatus int
		code           string
		attempts       int
	}{
		{"服务不可用", 503, "AI_UPSTREAM_UNAVAILABLE", 1},
		{"上游鉴权", 401, "AI_UPSTREAM_AUTH_FAILED", 1},
		{"上游限流", 429, "AI_UPSTREAM_RATE_LIMITED", 1},
		{"协议修复", 200, "AI_OUTPUT_INVALID", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.upstreamStatus != 200 {
					w.WriteHeader(tc.upstreamStatus)
					return
				}
				chatOK(`{"invalid":true}`)(w, r)
			}))
			defer upstream.Close()
			router, _, _, task, auth := v2Fixture(t, upstream)
			body := v2Body(t, matchBody, task, auth)
			for replay := 0; replay < 2; replay++ {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
				var response struct {
					Error   string `json:"error"`
					Billing struct {
						ModelCalls int    `json:"modelCalls"`
						CostStatus string `json:"costStatus"`
					} `json:"billing"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Error != tc.code || response.Billing.ModelCalls != tc.attempts || response.Billing.CostStatus != "unknown" {
					t.Fatalf("失败分类和用量必须保留，响应=%s", w.Body.String())
				}
			}
			if int(calls.Load()) != tc.attempts {
				t.Fatalf("失败重放重新调用了模型: %d", calls.Load())
			}
		})
	}
}
