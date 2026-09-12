package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSharedAdapterContractVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/adapter-contract-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	// 与插件 tests/fixtures 的固定向量相同，单仓 CI 不依赖另一 checkout。
	if fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))) != "faa365a77ea42a637fb2e65aafe008b80e060be3b672cc687bf4601557b3123d" {
		t.Fatal("共享协议向量变化，必须同步前后端版本与两侧测试")
	}
	var vectors struct {
		Request          CompileAdapterReq `json:"request"`
		Response         json.RawMessage   `json:"response"`
		InvalidResponses []struct {
			Name  string `json:"name"`
			Path  []any  `json:"path"`
			Value any    `json:"value"`
		} `json:"invalidResponses"`
		Interaction struct {
			SourceRef      string            `json:"sourceRef"`
			Hint           string            `json:"hint"`
			ValidActions   []json.RawMessage `json:"validActions"`
			InvalidActions []struct {
				Name  string          `json:"name"`
				Value json.RawMessage `json:"value"`
			} `json:"invalidActions"`
		} `json:"interaction"`
		SemanticSelection struct {
			Contract      AdapterHandoffSelection `json:"contract"`
			Claim         json.RawMessage         `json:"claim"`
			InvalidClaims []struct {
				Name  string          `json:"name"`
				Value json.RawMessage `json:"value"`
			} `json:"invalidClaims"`
		} `json:"semanticSelection"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if err := vectors.Request.Validate(); err != nil {
		t.Fatal(err)
	}
	valid, err := validateCompileAdapterOutput(&vectors.Request, string(vectors.Response))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(valid.Modules[0].Source, `kind:"tool"`) || valid.Effects[0].Progress == nil {
		t.Fatal("共享向量未覆盖业务工具和数值进展")
	}
	for _, test := range vectors.InvalidResponses {
		t.Run(test.Name, func(t *testing.T) {
			var response any
			if err := json.Unmarshal(vectors.Response, &response); err != nil {
				t.Fatal(err)
			}
			current := response
			for _, part := range test.Path[:len(test.Path)-1] {
				switch key := part.(type) {
				case string:
					current = current.(map[string]any)[key]
				case float64:
					current = current.([]any)[int(key)]
				}
			}
			switch key := test.Path[len(test.Path)-1].(type) {
			case string:
				current.(map[string]any)[key] = test.Value
			case float64:
				current.([]any)[int(key)] = test.Value
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateCompileAdapterOutput(&vectors.Request, string(encoded)); err == nil {
				t.Fatal("接受共享非法向量")
			}
		})
	}
	for _, phase := range []string{"", "runtime", "source-analysis"} {
		meta := Meta{RequestID: vectors.Request.RequestID, CallPhase: phase}
		if err := meta.validateMeta(); err != nil {
			t.Fatalf("旧客户端或正常调用阶段不兼容: %v", err)
		}
	}
	interaction := validAgentReq()
	interaction.InteractionVersion = 1
	interaction.AllowedNodeIDs = []string{"n1", "n2"}
	interaction.ProjectedNodeIDs = []string{"n1", "n2"}
	interaction.ValueRef = vectors.Interaction.SourceRef
	interaction.ValueHint = vectors.Interaction.Hint
	checkAction := func(action json.RawMessage) error {
		body, err := json.Marshal(map[string]any{"schemaVersion": "v1", "snapshotId": interaction.SnapshotID, "contextDigest": interaction.ContextDigest, "goalId": interaction.GoalID, "status": "act", "explanation": "当前来源片段授权", "action": action})
		if err != nil {
			t.Fatal(err)
		}
		_, err = validateAgentStepOutput(interaction, string(body))
		return err
	}
	for _, action := range vectors.Interaction.ValidActions {
		if err := checkAction(action); err != nil {
			t.Fatal("共享来源动作应合法", err)
		}
	}
	for _, invalid := range vectors.Interaction.InvalidActions {
		t.Run(invalid.Name, func(t *testing.T) {
			if checkAction(invalid.Value) == nil {
				t.Fatal("接受非法来源动作")
			}
		})
	}
	interaction.CallPhase, interaction.HandoffID, interaction.GoalKind = "runtime-handoff", "choose", "set-field-value"
	interaction.RuntimeHandoff = &AdapterHandoffContext{ProgramID: strings.Repeat("a", 64), Revision: strings.Repeat("b", 64), ModuleID: "fill", Goal: "选择当前候选", Selection: &vectors.SemanticSelection.Contract}
	checkSelection := func(claim json.RawMessage) error {
		body, err := json.Marshal(map[string]any{"schemaVersion": "v1", "snapshotId": interaction.SnapshotID, "contextDigest": interaction.ContextDigest, "goalId": interaction.GoalID, "status": "act", "explanation": "当前语义选择", "action": map[string]string{"op": "click", "targetNodeId": "n2"}, "semanticSelection": claim})
		if err != nil {
			t.Fatal(err)
		}
		_, err = validateAgentStepOutput(interaction, string(body))
		return err
	}
	if err := checkSelection(vectors.SemanticSelection.Claim); err != nil {
		t.Fatal("共享语义候选声明应合法", err)
	}
	for _, invalid := range vectors.SemanticSelection.InvalidClaims {
		t.Run(invalid.Name, func(t *testing.T) {
			if checkSelection(invalid.Value) == nil {
				t.Fatal("接受非法语义候选声明")
			}
		})
	}
	vectors.Request.Parameters = make([]AdapterParameter, 65)
	for i := range vectors.Request.Parameters {
		vectors.Request.Parameters[i] = AdapterParameter{ID: fmt.Sprintf("parameter%d", i), Type: "string"}
	}
	if vectors.Request.Validate() == nil {
		t.Fatal("参数上限必须与宿主64一致")
	}
}

func TestSharedAdapterSystemPromptDigest(t *testing.T) {
	expected, err := os.ReadFile("testdata/compile-adapter-prompt.sha256")
	if err != nil {
		t.Fatal(err)
	}
	store, err := LoadPrompts("../../prompts/private", []string{"compile_adapter"})
	if err != nil {
		t.Fatal(err)
	}
	system, _, _, err := store.Render("compile_adapter", adapterRequest())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(system))) != strings.TrimSpace(string(expected)) {
		t.Fatal("正式system已变化，请同步插件adapterPrompt.ts与共享SHA256")
	}
}
