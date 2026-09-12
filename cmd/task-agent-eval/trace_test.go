package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestTracePreservesFailedAndSuccessfulAttempts(t *testing.T) {
	trace, err := newEvalTrace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trace.Capability = "compile_adapter"
	trace.Secret = "synthetic-upstream-key"
	trace.Payload = json.RawMessage(`{"goal":"本地夹具"}`)
	trace.Attempts = append(trace.Attempts, map[string]any{"rawResponse": "首轮原始非法候选 synthetic-upstream-key", "validationError": "缺少绑定"}, map[string]any{"rawResponse": "第二轮原始候选", "validated": true})
	trace.finish(&err)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(trace.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), trace.Secret) {
		t.Fatal("上游回显的密钥不得落入证据")
	}
	for _, want := range []string{"首轮原始非法候选", "缺少绑定", "第二轮原始候选"} {
		if !strings.Contains(string(data), want) {
			t.Fatal("丢失真实尝试证据")
		}
	}
	err = errors.New("模型连接失败")
	trace.finish(&err)
	data, readErr := os.ReadFile(trace.Path)
	if readErr != nil || !strings.Contains(string(data), "模型连接失败") {
		t.Fatal("丢失最终失败根因", readErr)
	}
}
