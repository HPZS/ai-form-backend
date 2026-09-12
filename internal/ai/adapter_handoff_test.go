package ai

import (
	"strings"
	"testing"
)

func TestRuntimeHandoffGoalIsBoundedAndRendered(t *testing.T) {
	context := &AdapterHandoffContext{ProgramID: strings.Repeat("a", 64), Revision: strings.Repeat("b", 64), ModuleID: "select", Goal: "核对当前候选的配送说明", ReturnContractRef: "selected"}
	field := validAgentReq()
	field.CallPhase = "runtime-handoff"
	field.HandoffID = "handoff-1"
	field.RuntimeHandoff = context
	if err := field.Validate(); err != nil {
		t.Fatal(err)
	}
	task := &TaskAgentReq{Meta: field.Meta, SchemaVersion: "v1", RunID: "run", SnapshotID: "page", ContextDigest: "context", CallIndex: 1, SaveMode: "automatic", Tools: []TaskToolDescription{{Name: "observe", Effect: "read"}}, RuntimeHandoff: context}
	task.TaskID = "task"
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	prompts, err := LoadPrompts("../../prompts/private", []string{"agent_step", "agent_task_step"})
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]Request{"agent_step": field, "agent_task_step": task} {
		_, user, _, err := prompts.Render(name, request)
		if err != nil || !strings.Contains(user, context.Goal) || !strings.Contains(user, context.Revision) {
			t.Fatalf("子目标未进入正式模型上下文：%s %v", name, err)
		}
	}
	field.CallPhase = "runtime"
	if field.Validate() == nil {
		t.Fatal("接受了非移交阶段的子目标")
	}
	field.CallPhase = "runtime-handoff"
	context.Goal = strings.Repeat("字", 2001)
	if field.Validate() == nil || task.Validate() == nil {
		t.Fatal("接受了超大子目标")
	}
	context.Goal = "继续当前选择"
	context.Revision = "old"
	if field.Validate() == nil {
		t.Fatal("接受了未绑定版本的子目标")
	}
}
