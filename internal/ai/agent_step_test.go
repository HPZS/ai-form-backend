package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func validAgentReq() *AgentStepReq {
	return &AgentStepReq{
		Meta:       Meta{RequestID: "11111111-1111-4111-8111-111111111111"},
		SnapshotID: "s1", ContextDigest: "ctx1", GoalID: "g1", GoalKind: "set-field-value",
		ValueRef: "value:g1:opaque", ValueShape: "date", AllowedTransforms: []string{"identity"},
		RiskLimit: "L1", Projection: "n1 input readonly\nn2 button 日期", ProjectedNodeIDs: []string{"n1", "n2"},
		ProjectedRegionIDs: []string{"region:n1"},
	}
}

func TestAgentStepContextRequestsAreGroundedAndAuthorized(t *testing.T) {
	req := validAgentReq()
	prefix := `{"schemaVersion":"v1","snapshotId":"s1","contextDigest":"ctx1","goalId":"g1","status":"need-context","explanation":"补充上下文","contextRequest":`
	cases := []struct{ name, request, want string }{
		{"扩展节点未投影", `{"kind":"expand-nodes","nodeIds":["n999"],"reason":"查看周边"}`, "未投影 nodeId"},
		{"区域未投影", `{"kind":"region-detail","regionIds":["region:n999"],"reason":"查看区域"}`, "未投影 regionId"},
		{"截图未授权", `{"kind":"local-screenshot","reason":"需要视觉"}`, "未取得视觉授权"},
		{"上下文未知键", `{"kind":"expand-nodes","nodeIds":["n1"],"selector":"#x","reason":"查看周边"}`, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateAgentStepOutput(req, prefix+tc.request+`}`); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("应拒绝并包含 %q，实际 %v", tc.want, err)
			}
		})
	}

	req.VisualAuthorized = true
	valid := prefix + `{"kind":"local-screenshot","regionIds":["region:n1"],"reason":"需要视觉"}}`
	if out, err := validateAgentStepOutput(req, valid); err != nil || out.Status != "need-context" {
		t.Fatalf("已授权截图请求被拒绝: out=%+v err=%v", out, err)
	}
}

func TestAuditAgentCheckpointOutput(t *testing.T) {
	var post func(Request, string) (any, error)
	for _, spec := range Specs() {
		if spec.Name == "audit_agent_checkpoint" {
			post = spec.Post
			break
		}
	}
	if post == nil {
		t.Fatal("未注册 audit_agent_checkpoint")
	}
	req := &AuditAgentCheckpointReq{}
	valid := `{"consistent":false,"decision":"replan","issues":["本地证据不足"],"explanation":"继续规划"}`
	if _, err := post(req, valid); err != nil {
		t.Fatalf("合法审核结果被拒绝: %v", err)
	}
	bad := `{"consistent":false,"decision":"continue","issues":["证据矛盾"],"explanation":"继续"}`
	if _, err := post(req, bad); err == nil || !strings.Contains(err.Error(), "不得要求 continue") {
		t.Fatalf("矛盾审核未拒绝: %v", err)
	}
}

func TestAgentVisualGroundIsStrictlyCandidateBound(t *testing.T) {
	req := &AgentVisualGroundReq{
		Meta:       Meta{RequestID: "22222222-2222-4222-8222-222222222222"},
		SnapshotID: "s1", ContextDigest: "ctx1", GoalID: "g1", RegionID: "region:n1",
		ImageDataURL: "data:image/jpeg;base64,AAE=", MaskPolicyRef: "sensitive-controls-v1",
		Candidates: []VisualCandidate{{NodeID: "n1", Label: "日期按钮", X: 1, Y: 1, Width: 20, Height: 10}},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("合法视觉请求被拒绝: %v", err)
	}
	var spec *Spec
	for i := range Specs() {
		candidate := Specs()[i]
		if candidate.Name == "agent_visual_ground" {
			spec = &candidate
			break
		}
	}
	if spec == nil || spec.Messages == nil {
		t.Fatal("未注册多模态 agent_visual_ground")
	}
	if _, err := spec.Post(req, `{"candidateNodeId":"n999","confidence":0.9,"reason":"看起来像"}`); err == nil || !strings.Contains(err.Error(), "候选集外") {
		t.Fatalf("候选集外视觉结果未拒绝: %v", err)
	}
	if _, err := spec.Post(req, `{"candidateNodeId":"n1","confidence":0.9,"reason":"标注目标唯一"}`); err != nil {
		t.Fatalf("候选内视觉结果被拒绝: %v", err)
	}
	messages, err := spec.Messages(req, "system", "user")
	if err != nil || len(messages) != 2 || len(messages[1].ContentParts) != 2 || messages[1].ContentParts[1].ImageURL == nil {
		t.Fatalf("视觉消息未携带独立图片 part: %+v err=%v", messages, err)
	}
	body, err := json.Marshal(messages)
	if err != nil || !strings.Contains(string(body), `"type":"image_url"`) || !strings.Contains(string(body), req.ImageDataURL) {
		t.Fatalf("视觉消息序列化后丢失图片 part: %s err=%v", body, err)
	}
	req.MaskPolicyRef = "none"
	if err := req.Validate(); err == nil || !strings.Contains(err.Error(), "maskPolicyRef") {
		t.Fatalf("未遮罩视觉请求未拒绝: %v", err)
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
