package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/google/uuid"
)

// 输出总量已经包含推理明细；保留明细只用于解释耗时，不能再次加到费用或预算。
func TestReasoningUsageSurvivesCallerWithoutChangingTotals(t *testing.T) {
	for _, tc := range []struct {
		name, details, choices string
		want                   any
		invalid, failed        bool
	}{
		{name: "有推理用量", details: `{"reasoning_tokens":4}`, want: float64(4)},
		{name: "明确零推理", details: `{"reasoning_tokens":0}`, want: float64(0)},
		{name: "没有明细"},
		{name: "明细为空", details: `null`},
		{name: "未提供推理数", details: `{}`},
		{name: "推理数未知", details: `{"reasoning_tokens":null}`},
		{name: "负数不能冒充零", details: `{"reasoning_tokens":-1}`, invalid: true},
		{name: "明细大于总量", details: `{"reasoning_tokens":6}`, invalid: true},
		{name: "错误类型不破坏有效回答与总量", details: `{"reasoning_tokens":"4"}`, invalid: true},
		{name: "错误明细形状", details: `[]`, invalid: true},
		{name: "空回答仍保留真实用量", details: `{"reasoning_tokens":4}`, want: float64(4), choices: `[]`, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			choices := tc.choices
			if choices == "" {
				choices = `[{"message":{"content":"ok","reasoning_content":"private-reasoning-must-not-be-retained"}}]`
			}
			details := ""
			if tc.details != "" {
				details = `,"completion_tokens_details":` + tc.details
			}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"choices":%s,"usage":{"prompt_tokens":10,"completion_tokens":5%s}}`, choices, details)
			}))
			defer up.Close()
			var attempts []AttemptUsage
			ctx := withUsageObserver(context.Background(), func(item AttemptUsage) { attempts = append(attempts, item) })
			_, err := setupCaller(t, up).Call(ctx, "test_cap", nil)
			if (err != nil) != tc.failed {
				t.Fatalf("调用状态不符: %v", err)
			}
			if len(attempts) != 1 {
				t.Fatalf("不能因明细异常重新付费: %+v", attempts)
			}
			item := attempts[0]
			if item.InputTokens != 10 || item.OutputTokens != 5 || !item.UsageKnown {
				t.Fatalf("明细不得覆盖总量或改变完整性: %+v", item)
			}
			raw, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved["reasoningTokens"] != tc.want {
				t.Fatalf("推理明细应为 %v，实际 %s", tc.want, raw)
			}
			if invalid, _ := saved["reasoningTokensInvalid"].(bool); invalid != tc.invalid {
				t.Fatalf("异常明细必须可诊断: %s", raw)
			}
			if strings.Contains(string(raw), "private-reasoning") {
				t.Fatal("不得保留内部推理原文")
			}
		})
	}
}

func TestReasoningUsagePersistsWithoutDoubleChargingBudget(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":4}}}`)
	}))
	defer up.Close()
	_, db, uid, task, _ := v2Fixture(t, up)
	g := &Gateway{db: db}
	r := model.AIRequest{UserID: uid, TaskID: task, RequestID: uuid.NewString(), Capability: "match_columns", Status: model.AIReqPending, LeaseToken: uuid.NewString(), UsageDetails: "[]"}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := NewCaller(db).Call(g.withExecutionBudget(context.Background(), &r), "match_columns", nil); err != nil {
		t.Fatal(err)
	}
	var saved model.AIRequest
	if err := db.First(&saved, r.ID).Error; err != nil {
		t.Fatal(err)
	}
	var attempts []AttemptUsage
	if err := json.Unmarshal([]byte(saved.UsageDetails), &attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].ReasoningTokens == nil || *attempts[0].ReasoningTokens != 4 {
		t.Fatalf("持久化不能丢失有效明细: %s", saved.UsageDetails)
	}
	var budget model.AIExecutionBudget
	if err := db.Where("user_id = ? AND work_id = ?", uid, task).First(&budget).Error; err != nil {
		t.Fatal(err)
	}
	if saved.InputTokens != 10 || saved.OutputTokens != 5 || budget.KnownTokens != 15 || budget.ReservedTokens != 0 || budget.Attempts != 1 {
		t.Fatalf("推理数不得重复计费: request=%d+%d budget=%+v", saved.InputTokens, saved.OutputTokens, budget)
	}
}
