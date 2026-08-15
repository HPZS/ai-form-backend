// ai-form-backend - AGPL-3.0
package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/HPZS/ai-form-backend/internal/model"
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

// setupCaller 建内存库,注册能力 test_cap 与按序上游(a、b、…指向给定的 httptest 服务)。
func setupCaller(t *testing.T, servers ...*httptest.Server) *Caller {
	t.Helper()
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	// 能力不带模型覆盖,走全局默认——同时覆盖"默认+覆盖"解析的默认分支
	if err := db.Create(&model.AIDefault{ID: 1, Model: "m"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CapabilityPrice{Capability: "test_cap", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	for i, s := range servers {
		if err := db.Create(&model.AIUpstream{
			Name: fmt.Sprintf("%c", 'a'+i), BaseURL: s.URL, APIKey: "k", Enabled: true, SortOrder: i,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return NewCaller(db)
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

	caller := setupCaller(t, bad, good)
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

	caller := setupCaller(t, bad, good)
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

// 生效参数解析:模型能力覆盖优先、未覆盖用全局默认;温度/maxTokens 由代码按能力定;没模型时报错
func TestResolveParams(t *testing.T) {
	def := model.AIDefault{Model: "base"}

	p, err := resolveParams(def, model.CapabilityPrice{Capability: "match_columns"})
	if err != nil || p.Model != "base" || p.Temperature != 0 || p.MaxTokens != capMaxTokens["match_columns"] {
		t.Fatalf("未覆盖应用默认模型与该能力登记的参数,实际 %+v err=%v", p, err)
	}
	// 只有未登记的能力才落默认值(真实能力必须逐个登记,见 TestCapabilityRegistrationConsistency)
	if p, err := resolveParams(def, model.CapabilityPrice{Capability: "未登记的能力"}); err != nil || p.MaxTokens != defaultMaxTokens {
		t.Fatalf("未登记能力应落默认 maxTokens,实际 %+v err=%v", p, err)
	}

	p, err = resolveParams(def, model.CapabilityPrice{Capability: "generate_field", Model: "special"})
	if err != nil || p.Model != "special" || p.Temperature != 0.3 || p.MaxTokens != 1000 {
		t.Fatalf("模型覆盖应生效,生成字段温度 0.3/maxTokens 1000,实际 %+v err=%v", p, err)
	}

	if _, err := resolveParams(model.AIDefault{}, model.CapabilityPrice{}); err == nil {
		t.Fatal("默认与覆盖都没配模型应报错")
	}
}

// 全部上游都挂 → 错误;没有启用上游 → 明确报错
func TestAllDownAndNoUpstream(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
	}))
	defer bad.Close()
	caller := setupCaller(t, bad)
	if _, err := caller.Call(context.Background(), "test_cap", nil); err == nil {
		t.Fatal("全挂应返回错误")
	}
	empty := setupCaller(t) // 无上游
	if _, err := empty.Call(context.Background(), "test_cap", nil); err == nil {
		t.Fatal("无上游应返回错误")
	}
}
