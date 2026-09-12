package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func adapterRequest() *CompileAdapterReq {
	return &CompileAdapterReq{
		Meta:          Meta{RequestID: "00000000-0000-4000-8000-000000000001", CompilationID: "compile-1", CallPhase: "adapter-create", ModelPolicy: "adaptive"},
		SchemaVersion: 1, RuntimeVersion: "adapter-runtime-v1", Mode: "create",
		SourceContractDigest: strings.Repeat("a", 64), FormContractDigest: strings.Repeat("b", 64), UserInstructionRevision: "instruction-1", ToolVersion: "adapter-tools-v1",
		Goal: "按本条参数填写当前字段并回读结果", Scope: json.RawMessage(`{"formId":"form-1"}`),
		Parameters:     []AdapterParameter{{ID: "target", Type: "string", SourcePath: "/date"}},
		SourceContract: json.RawMessage(`{"root":"/rows"}`), Observation: json.RawMessage(`{"pageFacts":[],"parameters":{"target":"2026-09-12"}}`),
		Limits: AdapterLimits{32768, 262144, 65536, 100000, 64, 4096, 100, 256, 256, 8, 300000},
	}
}

func adapterOutput(req *CompileAdapterReq) map[string]any {
	return map[string]any{
		"schemaVersion": 1, "runtimeVersion": req.RuntimeVersion, "compilationId": req.CompilationID, "baseRevision": req.BaseRevision,
		"sourceContractDigest": req.SourceContractDigest, "formContractDigest": req.FormContractDigest, "userInstructionRevision": req.UserInstructionRevision, "toolVersion": req.ToolVersion,
		"modules":     []any{map[string]any{"id": "fill", "source": "function next(parameters, observation, state, lastOutcome) { return {kind: 'verify', contractRef: 'result', state: {}}; }"}},
		"queries":     []any{map[string]any{"id": "field", "locator": map[string]any{"css": "input.date"}, "limit": 2}},
		"effects":     []any{map[string]any{"id": "result", "queryRef": "field", "kind": "value-equals", "parameterId": "target"}},
		"entrypoints": map[string]any{"fillRecord": "fill"}, "aiHandoffs": []any{}, "diagnostics": []string{}, "unverifiedConditions": []string{"尚未独立回放"},
	}
}

func TestCompileAdapterBindingAndCandidateValidation(t *testing.T) {
	req := adapterRequest()
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	sp := Spec{}
	for _, candidate := range Specs() {
		if candidate.Name == "compile_adapter" {
			sp = candidate
		}
	}
	if sp.NewReq == nil {
		t.Fatal("compile_adapter 未注册")
	}
	check := func(out map[string]any) error {
		data, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		_, err = sp.Post(req, string(data))
		return err
	}
	if err := check(adapterOutput(req)); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"过期绑定":    func(out map[string]any) { out["compilationId"] = "old" },
		"伪造覆盖":    func(out map[string]any) { out["coverage"] = []any{} },
		"缺少空移交声明": func(out map[string]any) { delete(out, "aiHandoffs") },
		"未知模块":    func(out map[string]any) { out["entrypoints"] = map[string]any{"fillRecord": "missing"} },
		"未知参数":    func(out map[string]any) { out["effects"].([]any)[0].(map[string]any)["parameterId"] = "other" },
		"不受限源码": func(out map[string]any) {
			out["modules"].([]any)[0].(map[string]any)["source"] = "window.location='https://example.com'"
		},
		"源码超限": func(out map[string]any) {
			out["modules"].([]any)[0].(map[string]any)["source"] = strings.Repeat("x", 32769)
		},
		"非法属性": func(out map[string]any) {
			out["queries"].([]any)[0].(map[string]any)["locator"] = map[string]any{"attributes": map[string]string{"onclick": "submit()"}}
		},
		"父查询循环": func(out map[string]any) { out["queries"].([]any)[0].(map[string]any)["within"] = "field" },
		"伪造移交验收": func(out map[string]any) {
			out["aiHandoffs"] = []any{map[string]any{"id": "review", "goal": "查看当前候选", "returnContractRef": "unknown", "resumeState": map[string]any{}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			out := adapterOutput(req)
			mutate(out)
			if check(out) == nil {
				t.Fatal("接受非法候选")
			}
		})
	}
	req.Mode = "repair"
	if req.Validate() == nil {
		t.Fatal("修复缺少基线和失败证据不应通过")
	}
	base := strings.Repeat("c", 64)
	req.BaseRevision = &base
	req.CallPhase = "adapter-repair"
	req.FailureContext = json.RawMessage(`{"code":"effect-unmet"}`)
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCompileAdapterPromptAndRegistration(t *testing.T) {
	store, err := LoadPrompts("../../prompts/private", []string{"compile_adapter"})
	if err != nil {
		t.Fatal(err)
	}
	system, user, version, err := store.Render("compile_adapter", adapterRequest())
	if err != nil || version != "v1" || !strings.Contains(system, "function next") || !strings.Contains(user, "compile-1") {
		t.Fatalf("提示词未绑定正式协议: %s %v", version, err)
	}
	if temperature, tokens := CapabilityGenerationParams("compile_adapter"); temperature != 0 || tokens < 12000 {
		t.Fatalf("适配程序生成参数不完整: %v %v", temperature, tokens)
	}
}

func TestCompileAdapterRequestRejectsMissingBindingAndUnboundedInput(t *testing.T) {
	data, err := json.Marshal(adapterRequest())
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "baseRevision")
	data, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var req CompileAdapterReq
	if json.Unmarshal(data, &req) == nil {
		t.Fatal("省略基线绑定不应当作明确无基线")
	}
	r := adapterRequest()
	r.Limits.MaxSteps = 0
	if r.Validate() == nil {
		t.Fatal("缺少硬预算不应通过")
	}
	r = adapterRequest()
	r.Observation = json.RawMessage(`{"text":"` + strings.Repeat("a", 524288) + `"}`)
	if r.Validate() == nil {
		t.Fatal("超大局部事实不应通过")
	}
	r = adapterRequest()
	r.ExistingModules = []AdapterModule{{ID: "old", Source: strings.Repeat("a", 32769)}}
	if r.Validate() == nil {
		t.Fatal("修复上下文源码超限不应通过")
	}
}

func TestCompileAdapterNumericProgress(t *testing.T) {
	req := adapterRequest()
	check := func(mutate func(map[string]any)) error {
		out := adapterOutput(req)
		term := map[string]any{"queryRef": "field", "parameterId": "target", "weight": 12, "read": "text", "numberIndex": 0}
		mutate(term)
		out["effects"].([]any)[0].(map[string]any)["progress"] = map[string]any{"kind": "numeric-distance", "terms": []any{term}}
		data, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		_, err = validateCompileAdapterOutput(req, string(data))
		return err
	}
	if err := check(func(map[string]any) {}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"未知查询":   func(term map[string]any) { term["queryRef"] = "missing" },
		"未知参数":   func(term map[string]any) { term["parameterId"] = "missing" },
		"零权重":    func(term map[string]any) { term["weight"] = 0 },
		"权重超限":   func(term map[string]any) { term["weight"] = 1000001 },
		"非法读取":   func(term map[string]any) { term["read"] = "attribute" },
		"负数字索引":  func(term map[string]any) { term["numberIndex"] = -1 },
		"数字索引超限": func(term map[string]any) { term["numberIndex"] = 16 },
		"非整数索引":  func(term map[string]any) { term["numberIndex"] = 0.5 },
		"自报进展":   func(term map[string]any) { term["distance"] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			if check(mutate) == nil {
				t.Fatal("接受非法进展声明")
			}
		})
	}
}
