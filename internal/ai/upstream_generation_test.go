package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HPZS/ai-form-backend/internal/model"
)

func TestUpstreamGenerationWireAndAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, thinking, limit string
		wantThinking          any
	}{
		{name: "原配置不发送思考扩展", limit: "max_tokens"},
		{name: "明确关闭并使用完整输出参数", thinking: "disabled", limit: "max_completion_tokens", wantThinking: false},
		{name: "明确开启", thinking: "enabled", limit: "max_completion_tokens", wantThinking: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				chatOK("ok")(w, r)
			}))
			defer up.Close()
			caller := setupCaller(t, up)
			if err := caller.db.Model(&model.AIUpstream{}).Where("id = ?", 1).Updates(map[string]any{"thinking_mode": tc.thinking, "token_limit_parameter": tc.limit}).Error; err != nil {
				t.Fatal(err)
			}
			var attempts []AttemptUsage
			ctx := withUsageObserver(context.Background(), func(item AttemptUsage) { attempts = append(attempts, item) })
			if _, err := caller.Call(ctx, "test_cap", nil); err != nil {
				t.Fatal(err)
			}
			if payload["enable_thinking"] != tc.wantThinking || payload[tc.limit] != float64(defaultMaxTokens) {
				t.Fatalf("实际请求参数错误: %+v", payload)
			}
			other := "max_tokens"
			if tc.limit == other {
				other = "max_completion_tokens"
			}
			if _, exists := payload[other]; exists {
				t.Fatal("不能同时发送两个互斥的输出上限")
			}
			if len(attempts) != 1 || attempts[0].ThinkingMode != tc.thinking || attempts[0].TokenLimitParameter != tc.limit || attempts[0].MaxOutputTokens != defaultMaxTokens {
				t.Fatalf("尝试记录必须还原实际参数: %+v", attempts)
			}
		})
	}
}

func TestInvalidGenerationConfigurationCannotDispatchOrFailOver(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; chatOK("ok")(w, r) }))
	defer up.Close()
	caller := setupCaller(t, up, up)
	if err := caller.db.Model(&model.AIUpstream{}).Where("id = ?", 1).Update("thinking_mode", "invalid").Error; err != nil {
		t.Fatal(err)
	}
	_, err := caller.Call(context.Background(), "test_cap", nil)
	if aiFailureCode(err) != "AI_UPSTREAM_CONFIG_INVALID" || calls != 0 {
		t.Fatalf("错误配置不得绕道付费: calls=%d err=%v", calls, err)
	}
}
