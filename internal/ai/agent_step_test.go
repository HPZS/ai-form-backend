package ai

import (
	"strings"
	"testing"
)

func validAgentReq() *AgentStepReq {
	return &AgentStepReq{
		Meta:       Meta{RequestID: "11111111-1111-4111-8111-111111111111"},
		SnapshotID: "s1", ContextDigest: "ctx1", GoalID: "g1", GoalKind: "set-field-value",
		ValueRef: "value:g1:opaque", ValueShape: "date", AllowedTransforms: []string{"identity"},
		RiskLimit: "L1", Projection: "n1 input readonly\nn2 button 日期", ProjectedNodeIDs: []string{"n1", "n2"},
	}
}

func TestAgentStepStrictValidation(t *testing.T) {
	req := validAgentReq()
	valid := `{"schemaVersion":"v1","snapshotId":"s1","contextDigest":"ctx1","goalId":"g1","status":"act","explanation":"打开日期面板","action":{"op":"click","targetNodeId":"n2"}}`
	out, err := validateAgentStepOutput(req, valid)
	if err != nil || out.Status != "act" {
		t.Fatalf("合法单步被拒绝: out=%+v err=%v", out, err)
	}

	cases := []struct{ name, body, want string }{
		{"幻觉节点", strings.Replace(valid, `"n2"`, `"n999"`, 1), "未投影"},
		{"上下文错", strings.Replace(valid, `"ctx1"`, `"ctx2"`, 1), "上下文"},
		{"未知动作", strings.Replace(valid, `"click"`, `"eval-script"`, 1), "未知 action"},
		{"非act夹动作", strings.Replace(valid, `"status":"act"`, `"status":"done"`, 1), "夹带 action"},
		{"输出多键", strings.Replace(valid, `"targetNodeId":"n2"`, `"targetNodeId":"n2","selector":"#x"`, 1), "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateAgentStepOutput(req, tc.body); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应拒绝并包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

func TestAgentStepRejectsValueAndCommitEscapes(t *testing.T) {
	req := validAgentReq()
	badValue := `{"schemaVersion":"v1","snapshotId":"s1","contextDigest":"ctx1","goalId":"g1","status":"act","explanation":"写入","action":{"op":"replace-text","targetNodeId":"n1","valueRef":"原始业务值","transform":"identity"}}`
	if _, err := validateAgentStepOutput(req, badValue); err == nil || !strings.Contains(err.Error(), "未授权") {
		t.Fatalf("原值逃逸未拒绝: %v", err)
	}

	req.KnownSubmitNodeIDs = []string{"n2"}
	clickSubmit := `{"schemaVersion":"v1","snapshotId":"s1","contextDigest":"ctx1","goalId":"g1","status":"act","explanation":"点击","action":{"op":"click","targetNodeId":"n2"}}`
	if _, err := validateAgentStepOutput(req, clickSubmit); err == nil || !strings.Contains(err.Error(), "commit-form") {
		t.Fatalf("普通 click 提交未拒绝: %v", err)
	}
}

func TestAgentStepRequestLimits(t *testing.T) {
	req := validAgentReq()
	req.Projection = strings.Repeat("x", 512*1024+1)
	if err := req.Validate(); err == nil || !strings.Contains(err.Error(), "512 KiB") {
		t.Fatalf("超大投影未拒绝: %v", err)
	}
}
