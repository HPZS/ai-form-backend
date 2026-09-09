package ai

import "testing"

func TestInteractionContract(t *testing.T) {
	req := validAgentReq()
	req.InteractionVersion = 1
	req.AllowedNodeIDs = []string{"n1"}
	req.ValueSemantics = &GoalValueSemantics{Version: 1, Kind: "datetime", Precision: "second", Parts: []GoalValuePart{{ID: "month", Ref: "part-month", Hint: "9"}, {ID: "day", Ref: "part-day", Hint: "9"}}}
	req.History = []AgentHistoryItem{{Action: "run-skill:legacy-fill-current-field", Verdict: "失败后面板已展开", Dispatch: "dispatched", Effect: "not-met", GoalVerdict: "unsatisfied", BeforeState: "a", AfterState: "b", FailureCode: "no-progress"}}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	prefix := `{"schemaVersion":"v1","snapshotId":"s1","contextDigest":"ctx1","goalId":"g1","status":"act","explanation":"当前目标交互","action":`
	for _, test := range []struct {
		action string
		valid  bool
	}{
		{`{"op":"replace-text","targetNodeId":"n1","valueRef":"part-month","transform":"identity"}`, true},
		{`{"op":"click","targetNodeId":"n1","valuePart":"day"}`, true},
		{`{"op":"click","targetNodeId":"n2"}`, false},
		{`{"op":"click","targetNodeId":"n1","valuePart":"foreign-source"}`, false},
		{`{"op":"replace-text","targetNodeId":"n1","valueRef":"foreign-goal"}`, false},
		{`{"op":"click","targetNodeId":"n1","key":"Space"}`, false},
		{`{"op":"click","targetNodeId":"n1","valuePart":null}`, false},
	} {
		_, err := validateAgentStepOutput(req, prefix+test.action+"}")
		if (err == nil) != test.valid {
			t.Fatalf("action=%s err=%v", test.action, err)
		}
	}
	req.History[0].Dispatch = "success"
	if req.Validate() == nil {
		t.Fatal("不得把分发事实伪装成完成状态")
	}
	req.History = nil
	req.ValueSemantics.Parts[1].Ref = "part-month"
	if req.Validate() == nil {
		t.Fatal("子值引用必须唯一")
	}
	req.ValueSemantics = nil
	req.InteractionVersion = 0
	if req.Validate() == nil {
		t.Fatal("不能缺少版本声明却传新范围")
	}
}
