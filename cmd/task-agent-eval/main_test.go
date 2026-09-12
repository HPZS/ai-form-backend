package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 验收CLI必须把生产requestId与真实尝试数一起回传，前端才能消除同一请求的unknown计数。
func TestEvaluationMetadataRetainsRequestIdentityAcrossProtocolRepair(t *testing.T) {
	requestID := "11111111-1111-4111-8111-111111111111"
	sourceHash := strings.Repeat("a", 64)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := `{}`
		if calls == 2 {
			content = `{"schemaVersion":"v1","sourceHash":"` + sourceHash + `","sourceKind":"text","entityName":"记录","records":[{"recordId":"1","fields":[{"name":"值","sourceRefs":[{"quote":"hello","occurrence":0}],"confidence":1,"explanation":"原文"}]}],"exclusions":[],"confidence":1,"explanation":"原文"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	oldClient := http.DefaultClient
	http.DefaultClient = server.Client()
	defer func() { http.DefaultClient = oldClient }()
	t.Setenv("AIFORM_UPSTREAM_URL", server.URL)
	t.Setenv("AIFORM_UPSTREAM_KEY", "synthetic-local-test")
	t.Setenv("AIFORM_EVAL_MODEL", "fixture-model")
	t.Setenv("PROMPTS_DIR", "../../prompts/private")
	t.Setenv("AIFORM_EVAL_TRACE_DIR", t.TempDir())
	dir := t.TempDir()
	in, err := os.Create(filepath.Join(dir, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "output.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := in.WriteString(`{"capability":"compile_input","payload":{"requestId":"` + requestID + `","sourceKind":"text","sourceHash":"` + sourceHash + `","sourceText":"hello","callPhase":"source-analysis"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, out
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Meta struct {
			RequestID  string `json:"requestId"`
			ModelCalls int    `json:"modelCalls"`
			CallPhase  string `json:"callPhase"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(out).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Meta.RequestID != requestID || response.Meta.ModelCalls != 2 || response.Meta.CallPhase != "source-analysis" || calls != 2 {
		t.Fatalf("调用身份或真实尝试计数未保留：%+v calls=%d", response.Meta, calls)
	}
}
