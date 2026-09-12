package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskAgentDecisionBindingAndTools(t *testing.T) {
	req := &TaskAgentReq{Meta: Meta{RequestID: "00000000-0000-4000-8000-000000000001", TaskID: "task"}, SchemaVersion: "v1", RunID: "run", SnapshotID: "snapshot", ContextDigest: "digest", CallIndex: 1, SaveMode: "automatic", Tools: []TaskToolDescription{{Name: "fill_field", Effect: "write", Parameters: map[string]TaskToolParameter{"fieldIndex": {Type: "number", Required: true}}}}}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	valid := `{"schemaVersion":"v1","runId":"run","snapshotId":"snapshot","contextDigest":"digest","status":"tool","explanation":"填写字段","tool":{"callId":"call1","name":"fill_field","arguments":{"fieldIndex":0}}}`
	if _, err := validateTaskAgentOutput(req, valid); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"stale":        func(o map[string]any) { o["snapshotId"] = "old" },
		"unknown tool": func(o map[string]any) { o["tool"].(map[string]any)["name"] = "eval" },
		"unknown argument": func(o map[string]any) {
			o["tool"].(map[string]any)["arguments"].(map[string]any)["script"] = "alert(1)"
		},
		"wrong type":       func(o map[string]any) { o["tool"].(map[string]any)["arguments"].(map[string]any)["fieldIndex"] = "0" },
		"missing argument": func(o map[string]any) { o["tool"].(map[string]any)["arguments"] = map[string]any{} },
		"mixed states":     func(o map[string]any) { o["status"] = "done" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var o map[string]any
			_ = json.Unmarshal([]byte(valid), &o)
			mutate(o)
			data, _ := json.Marshal(o)
			if _, err := validateTaskAgentOutput(req, string(data)); err == nil {
				t.Fatal("accepted invalid decision")
			}
		})
	}
}

func TestTaskAgentPromptAndRequestLimits(t *testing.T) {
	store, err := LoadPrompts("../../prompts/private", []string{"agent_task_step"})
	if err != nil {
		t.Fatal(err)
	}
	if _, user, version, err := store.Render("agent_task_step", &TaskAgentReq{}); err != nil || version != "v4" || !strings.Contains(user, "read_source") || !strings.Contains(user, "advance_form") {
		t.Fatalf("prompt: %s %v", version, err)
	}
	req := &TaskAgentReq{Meta: Meta{RequestID: "00000000-0000-4000-8000-000000000001", TaskID: "task"}, SchemaVersion: "v1", RunID: "run", SnapshotID: "snapshot", ContextDigest: "digest", CallIndex: 1, SaveMode: "automatic", Tools: []TaskToolDescription{{Name: "read", Effect: "read"}}}
	req.History = make([]json.RawMessage, 25)
	if req.Validate() == nil {
		t.Fatal("accepted unbounded history")
	}
}

func TestTaskToolLimitsMatchRuntime(t *testing.T) {
	for _, test := range []struct {
		kind  string
		value any
	}{
		{"string", strings.Repeat("a", 2001)}, {"strings", make([]string, 101)},
		{"numbers", []int{-1}}, {"numbers", make([]int, 201)},
	} {
		req := &TaskAgentReq{SchemaVersion: "v1", RunID: "run", SnapshotID: "snapshot", ContextDigest: "digest", Tools: []TaskToolDescription{{Name: "read", Parameters: map[string]TaskToolParameter{"value": {Type: test.kind, Required: true}}}}}
		data, _ := json.Marshal(TaskAgentOutput{SchemaVersion: "v1", RunID: "run", SnapshotID: "snapshot", ContextDigest: "digest", Status: "tool", Explanation: "检查", Tool: &TaskToolCall{CallID: "call", Name: "read", Arguments: map[string]json.RawMessage{"value": func() []byte { b, _ := json.Marshal(test.value); return b }()}}})
		if _, err := validateTaskAgentOutput(req, string(data)); err == nil {
			t.Fatalf("accepted oversized %s", test.kind)
		}
	}
	if temperature, tokens := CapabilityGenerationParams("agent_task_step"); temperature != 0 || tokens != 1800 {
		t.Fatalf("unexpected evaluation parameters: %v %v", temperature, tokens)
	}
}
