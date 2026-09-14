package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestActionRecoveryOptInReturnsKnownRejectionWithoutProtocolRetry(t *testing.T) {
	for _, test := range []struct {
		name       string
		version    int
		invalidRef bool
		wantCalls  int32
		wantStatus int
		wantCode   string
	}{
		{"新版本地恢复", 1, false, 1, 422, "AI_ACTION_NOT_APPLICABLE"},
		{"旧客户端保留修复", 0, false, 2, 422, "AI_OUTPUT_INVALID"},
		{"句柄不合法仍属协议错误", 1, true, 2, 422, "AI_OUTPUT_INVALID"},
		{"未知恢复版本零分发", 2, false, 0, 400, "BAD_REQUEST"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			ref := "value:g1:opaque"
			if test.invalidRef {
				ref = "untrusted-value"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				chatOK(`{"schemaVersion":"v1","snapshotId":"s1","contextDigest":"ctx1","goalId":"g1","status":"act","explanation":"写入当前字段","action":{"op":"replace-text","targetNodeId":"n1","valueRef":"`+ref+`"}}`)(w, r)
			}))
			defer upstream.Close()
			router, _, _, task, auth := v2Fixture(t, upstream)
			req := validAgentReq()
			req.ReadOnlyNodeIDs = []string{"n1"}
			req.AdapterLearningAvailable = true
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if test.version != 0 {
				body["actionRecoveryVersion"] = test.version
			}
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			payload := v2Body(t, string(raw), task, auth)
			for i := 0; i < 2; i++ {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/agent-step", strings.NewReader(payload)))
				if w.Code != test.wantStatus || !strings.Contains(w.Body.String(), test.wantCode) {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if calls.Load() != test.wantCalls {
					t.Fatalf("实际调用=%d，预期=%d；重复发送不得新增尝试", calls.Load(), test.wantCalls)
				}
			}
		})
	}
}

func TestActionRecoveryRequiresCurrentHostLearningCapability(t *testing.T) {
	for _, mode := range []string{"unavailable", "other-goal", "handoff"} {
		t.Run(mode, func(t *testing.T) {
			req := validAgentReq()
			req.ActionRecoveryVersion = 1
			req.AdapterLearningAvailable = true
			switch mode {
			case "unavailable":
				req.AdapterLearningAvailable = false
			case "other-goal":
				req.GoalKind = "open-form"
			case "handoff":
				// 通过 JSON 构造真实请求形状，现有程序接管不得走首次学习。
				if err := json.Unmarshal([]byte(`{"runtimeHandoff":{}}`), &req); err != nil {
					t.Fatal(err)
				}
			}
			if err := req.Validate(); err == nil || !strings.Contains(err.Error(), "动作恢复版本或当前学习能力无效") {
				t.Fatalf("不满足首次学习条件时应拒绝动作恢复：%v", err)
			}
		})
	}
}
