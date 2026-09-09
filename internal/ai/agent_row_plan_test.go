// ai-form-backend - AGPL-3.0
package ai

import (
	"strings"
	"testing"
)

func validAgentRowPlanReq() *AgentRowPlanReq {
	return &AgentRowPlanReq{
		SnapshotID: "snapshot-1", ContextDigest: "ctx-1", RowPlanID: "row-plan-1", RiskLimit: "L1",
		Projection: "n1 input 姓名\nn2 select 学历\nn3 button 提交", ProjectedNodeIDs: []string{"n1", "n2", "n3"},
		ProjectedRegionIDs: []string{"region-1"}, KnownSubmitNodeIDs: []string{"n3"},
		Goals: []AgentRowPlanningGoalReq{
			{GoalID: "g1", FieldIndex: 0, SemanticName: "姓名", ExpectedValueShape: "opaque", ActionNodeIDs: []string{"n1"}, EvidenceNodeIDs: []string{"n1"}, AllowedTransforms: []string{"identity"}, Skills: []string{"legacy-fill-current-field"}},
			{GoalID: "g2", FieldIndex: 1, SemanticName: "学历", ExpectedValueShape: "opaque", ActionNodeIDs: []string{"n2"}, EvidenceNodeIDs: []string{"n2"}, AllowedTransforms: []string{"identity"}, Skills: []string{"legacy-fill-current-field"}},
		},
	}
}

func TestAgentRowPlanStrictlySeparatesGoalsAndRejectsCommit(t *testing.T) {
	req := validAgentRowPlanReq()
	valid := `{"schemaVersion":"v1","snapshotId":"snapshot-1","contextDigest":"ctx-1","rowPlanId":"row-plan-1","status":"planned","explanation":"规划字段","steps":[{"goalId":"g1","action":{"op":"replace-text","targetNodeId":"n1","valueBinding":"goal-value"}},{"goalId":"g2","action":{"op":"run-skill","skillId":"legacy-fill-current-field","inputBindings":["goal-value"]}}]}`
	if out, err := validateAgentRowPlanOutput(req, valid); err != nil || len(out.Steps) != 2 {
		t.Fatalf("合法行计划被拒绝: out=%+v err=%v", out, err)
	}

	tests := []struct{ name, body, want string }{
		{"跨字段节点", strings.Replace(valid, `{"goalId":"g2","action":{"op":"run-skill","skillId":"legacy-fill-current-field","inputBindings":["goal-value"]}}`, `{"goalId":"g2","action":{"op":"replace-text","targetNodeId":"n1","valueBinding":"goal-value"}}`, 1), "跨 Goal"},
		{"提交节点", strings.Replace(valid, `"targetNodeId":"n1"`, `"targetNodeId":"n3"`, 1), "禁止"},
		{"伪造值句柄", strings.Replace(valid, `"valueBinding":"goal-value"`, `"valueRef":"secret"`, 1), "unknown field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateAgentRowPlanOutput(req, tc.body); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应拒绝，want=%q err=%v", tc.want, err)
			}
		})
	}
}

func TestAgentRowPlanPromptNeverRequestsRawValues(t *testing.T) {
	store, err := LoadPrompts("../../prompts/private", []string{"agent_row_plan"})
	if err != nil {
		t.Fatalf("加载 agent_row_plan 提示词失败: %v", err)
	}
	_, user, version, err := store.Render("agent_row_plan", validAgentRowPlanReq())
	if err != nil {
		t.Fatalf("渲染 agent_row_plan 提示词失败: %v", err)
	}
	if version != "v2" || !strings.Contains(user, "valueBinding") || !strings.Contains(user, "不得生成提交") {
		t.Fatalf("行级提示词缺少本地值绑定或提交边界: version=%s", version)
	}
}
