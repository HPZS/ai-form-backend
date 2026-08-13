// ai-form-backend - AGPL-3.0
package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/HPZS/ai-form-backend/config"
)

func chatOK(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":` + jsonQuote(content) + `}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}
}

func jsonQuote(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for _, c := range []byte(s) {
		if c == '"' || c == '\\' {
			b = append(b, '\\')
		}
		b = append(b, c)
	}
	return string(append(b, '"'))
}

func testAIConfig(cap string, servers ...*httptest.Server) *config.AIConfig {
	ups := map[string]*config.AIUpstream{}
	cands := []config.AICandidate{}
	for i, s := range servers {
		name := string(rune('a' + i))
		u := &config.AIUpstream{BaseURL: s.URL}
		u.SetAPIKey("test")
		ups[name] = u
		cands = append(cands, config.AICandidate{Upstream: name, Model: "m"})
	}
	return &config.AIConfig{
		Upstreams:    ups,
		Capabilities: map[string]config.AICapability{cap: {Candidates: cands, MaxTokens: 100}},
	}
}

// 首选挂掉 → 自动走次选
func TestFailover(t *testing.T) {
	var firstHits, secondHits int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstHits, 1)
		w.WriteHeader(500)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&secondHits, 1)
		chatOK("hello")(w, r)
	}))
	defer good.Close()

	caller := NewCaller(testAIConfig("test_cap", bad, good))
	res, err := caller.Call(context.Background(), "test_cap", []ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("应切换到次选成功: %v", err)
	}
	if res.Content != "hello" || res.Upstream != "b" {
		t.Fatalf("应由次选服务,实际 %+v", res)
	}
	if firstHits != 1 || secondHits != 1 {
		t.Fatalf("命中次数异常: first=%d second=%d", firstHits, secondHits)
	}
}

// 连续失败 3 次后进入冷却,冷却期内请求直接跳过死上游
func TestCooldown(t *testing.T) {
	var badHits int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&badHits, 1)
		w.WriteHeader(500)
	}))
	defer bad.Close()
	good := httptest.NewServer(chatOK("ok"))
	defer good.Close()

	caller := NewCaller(testAIConfig("test_cap", bad, good))
	for i := 0; i < 5; i++ {
		if _, err := caller.Call(context.Background(), "test_cap", nil); err != nil {
			t.Fatalf("第 %d 次调用失败: %v", i+1, err)
		}
	}
	// 前 3 次每次都会先打一下坏上游,之后进入冷却被跳过
	if badHits != 3 {
		t.Fatalf("坏上游应只被打 3 次后冷却,实际 %d", badHits)
	}
}

// 全部上游都挂 → ErrAllUpstreamsDown
func TestAllDown(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
	}))
	defer bad.Close()
	caller := NewCaller(testAIConfig("test_cap", bad))
	if _, err := caller.Call(context.Background(), "test_cap", nil); err == nil {
		t.Fatal("应返回错误")
	}
}
