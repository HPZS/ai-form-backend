package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAdapterSemanticSelectionRequiresCurrentFieldHandoff(t *testing.T) {
	req := validAgentReq()
	req.InteractionVersion = 1
	req.GoalKind = "set-field-value"
	req.AllowedNodeIDs = []string{"n1", "n2", "n3"}
	req.ProjectedNodeIDs = req.AllowedNodeIDs
	req.CallPhase = "runtime-handoff"
	req.HandoffID = "choose"
	req.RuntimeHandoff = &AdapterHandoffContext{ProgramID: strings.Repeat("a", 64), Revision: strings.Repeat("b", 64), ModuleID: "fill", Goal: "依据当前需求选择", ReturnContractRef: "selected", Selection: &AdapterHandoffSelection{CandidateQueryRef: "candidates", ValueQueryRef: "labels", Read: "text"}}
	check := func(status string, action any, selection any) error {
		body, err := json.Marshal(map[string]any{"schemaVersion": "v1", "snapshotId": req.SnapshotID, "contextDigest": req.ContextDigest, "goalId": req.GoalID, "status": status, "explanation": "当前候选选择", "action": action, "semanticSelection": selection})
		if err != nil {
			t.Fatal(err)
		}
		_, err = validateAgentStepOutput(req, string(body))
		return err
	}
	selection := map[string]any{"handoffId": "choose", "candidateNodeId": "n2", "reason": "当前描述符合需求"}
	for _, action := range []any{map[string]any{"op": "click", "targetNodeId": "n2"}, map[string]any{"op": "click", "targetNodeId": "n3"}, map[string]any{"op": "select-native", "targetNodeId": "n1", "optionNodeId": "n2"}} {
		if err := check("act", action, selection); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []any{map[string]any{"op": "replace-text", "targetNodeId": "n1", "valueRef": req.ValueRef}, map[string]any{"op": "select-native", "targetNodeId": "n1", "optionNodeId": "n3"}, map[string]any{"op": "click", "targetNodeId": "n2", "valuePart": "year"}, map[string]any{"op": "select-native", "targetNodeId": "n1", "optionNodeId": "n2", "selected": false}} {
		if check("act", action, selection) == nil {
			t.Fatal("语义选择接受了无关动作或混合来源授权")
		}
	}
	click := map[string]any{"op": "click", "targetNodeId": "n2"}
	for _, claim := range []any{nil, map[string]any{"handoffId": "other", "candidateNodeId": "n2", "reason": "依据"}, map[string]any{"handoffId": "choose", "candidateNodeId": "n99", "reason": "依据"}, map[string]any{"handoffId": "choose", "candidateNodeId": "n2", "reason": "依据", "value": "模型新值"}} {
		if check("act", click, claim) == nil {
			t.Fatal("接受了伪造或未投影语义选择")
		}
	}
	if check("done", nil, selection) == nil {
		t.Fatal("done不能建立派生值凭据")
	}
	req.CallPhase = "runtime"
	if check("act", click, selection) == nil {
		t.Fatal("非移交阶段不能声明语义选择")
	}
	req.CallPhase = "runtime-handoff"
	req.RuntimeHandoff = nil
	if check("act", click, selection) == nil {
		t.Fatal("无显式契约不能声明语义选择")
	}
}

func TestAdapterSemanticSelectionIsRenderedOnlyForExplicitFieldContract(t *testing.T) {
	store, err := LoadPrompts("../../prompts/private", []string{"agent_step"})
	if err != nil {
		t.Fatal(err)
	}
	req := validAgentReq()
	_, ordinary, _, err := store.Render("agent_step", req)
	if err != nil || strings.Contains(ordinary, `"semanticSelection"`) {
		t.Fatalf("普通字段不得获得语义选择声明: %v", err)
	}
	req.CallPhase, req.HandoffID = "runtime-handoff", "choose-current"
	req.RuntimeHandoff = &AdapterHandoffContext{ProgramID: strings.Repeat("a", 64), Revision: strings.Repeat("b", 64), ModuleID: "fill", Goal: "选择满足当前文字沟通需求的服务台", Selection: &AdapterHandoffSelection{CandidateQueryRef: "candidates", ValueQueryRef: "labels", Read: "text"}}
	_, current, version, err := store.Render("agent_step", req)
	if err != nil || version != "v7" || !strings.Contains(current, `"semanticSelection":{"handoffId":"choose-current"`) || !strings.Contains(current, req.RuntimeHandoff.Goal) || !strings.Contains(current, `"valueQueryRef":"labels"`) {
		t.Fatalf("显式选择契约未完整进入正式上下文: version=%s err=%v", version, err)
	}
}
