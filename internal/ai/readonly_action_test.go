package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadOnlyFactsRejectWritesButAllowOpening(t *testing.T) {
	stepReq := validAgentReq()
	raw, err := json.Marshal(stepReq)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["readOnlyNodeIds"] = []string{"n1"}
	raw, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, stepReq); err != nil {
		t.Fatal(err)
	}
	if err := stepReq.Validate(); err != nil {
		t.Fatal(err)
	}
	write := `{"schemaVersion":"v1","snapshotId":"s1","contextDigest":"ctx1","goalId":"g1","status":"act","explanation":"写入日期","action":{"op":"replace-text","targetNodeId":"n1","valueRef":"value:g1:opaque","transform":"identity"}}`
	if _, err := validateAgentStepOutput(stepReq, write); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("后端必须拒绝已知只读目标的写入: %v", err)
	}
	open := `{"schemaVersion":"v1","snapshotId":"s1","contextDigest":"ctx1","goalId":"g1","status":"act","explanation":"打开日期控件","action":{"op":"click","targetNodeId":"n1"}}`
	if _, err := validateAgentStepOutput(stepReq, open); err != nil {
		t.Fatalf("只读字段仍可打开: %v", err)
	}

	rowReq := validAgentRowPlanReq()
	if err := json.Unmarshal([]byte(`{"readOnlyNodeIds":["n1"]}`), rowReq); err != nil {
		t.Fatal(err)
	}
	row := `{"schemaVersion":"v1","snapshotId":"snapshot-1","contextDigest":"ctx-1","rowPlanId":"row-plan-1","status":"planned","explanation":"规划字段","steps":[{"goalId":"g1","action":{"op":"replace-text","targetNodeId":"n1","valueBinding":"goal-value"}}],"deferredGoalIds":["g2"]}`
	if _, err := validateAgentRowPlanOutput(rowReq, row); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("行计划不能绕过只读: %v", err)
	}
}

func TestReadOnlyFactsMustBelongToProjection(t *testing.T) {
	for _, ids := range [][]string{{"outside"}, {strings.Repeat("n", 101)}} {
		step := validAgentReq()
		step.ReadOnlyNodeIDs = ids
		if step.Validate() == nil {
			t.Fatal("单步不能接受未投影或超长只读身份")
		}
		row := validAgentRowPlanReq()
		row.ReadOnlyNodeIDs = ids
		if row.Validate() == nil {
			t.Fatal("行计划不能接受未投影或超长只读身份")
		}
	}
	step := validAgentReq()
	step.ReadOnlyNodeIDs = []string{"n1"}
	store, err := LoadPrompts("../../prompts/private", []string{"agent_step", "agent_row_plan"})
	if err != nil {
		t.Fatal(err)
	}
	_, user, _, err := store.Render("agent_step", step)
	if err != nil || !strings.Contains(user, `当前原生只读 nodeId: ["n1"]`) {
		t.Fatalf("模型也须收到结构化限制: %v", err)
	}
	row := validAgentRowPlanReq()
	row.ReadOnlyNodeIDs = []string{"n1"}
	_, user, _, err = store.Render("agent_row_plan", row)
	if err != nil || !strings.Contains(user, `当前原生只读 nodeId: ["n1"]`) {
		t.Fatalf("行计划缺少只读限制: %v", err)
	}
}
