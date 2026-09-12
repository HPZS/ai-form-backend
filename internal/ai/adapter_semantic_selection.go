package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

func validateAgentSemanticSelection(req *AgentStepReq, out agentStepOutput, action agentAction, projected func(string) bool) error {
	if len(out.SemanticSelection) == 0 {
		return nil
	}
	if req.InteractionVersion != 1 || req.GoalKind != "set-field-value" || req.CallPhase != "runtime-handoff" || req.HandoffID == "" || req.RuntimeHandoff == nil || req.RuntimeHandoff.Selection == nil || out.Status != "act" {
		return fmt.Errorf("语义选择必须绑定当前显式字段移交")
	}
	if err := validateAdapterHandoff(req.Meta, req.RuntimeHandoff); err != nil {
		return err
	}
	var selection struct {
		HandoffID       string `json:"handoffId"`
		CandidateNodeID string `json:"candidateNodeId"`
		Reason          string `json:"reason"`
	}
	decoder := json.NewDecoder(bytes.NewReader(out.SemanticSelection))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&selection); err != nil {
		return fmt.Errorf("语义选择声明非法: %w", err)
	}
	if selection.HandoffID != req.HandoffID || len(selection.HandoffID) > 80 || selection.CandidateNodeID == "" || len(selection.CandidateNodeID) > 80 || !projected(selection.CandidateNodeID) || strings.TrimSpace(selection.Reason) == "" || len(utf16.Encode([]rune(selection.Reason))) > 1000 {
		return fmt.Errorf("语义选择身份、候选节点或理由非法")
	}
	if action.Op != "click" && action.Op != "select-native" || action.Op == "select-native" && (action.OptionNodeID != selection.CandidateNodeID || action.Selected != nil && !*action.Selected) || action.ValueRef != "" || action.ValuePart != "" || action.ValueSlice != nil {
		return fmt.Errorf("语义选择只能点击候选或选择其option，不得混用来源值授权")
	}
	// 服务器只见已授权投影；候选与点击子节点的真实包含关系、当前读取值及最终选中事实由宿主检查。
	return nil
}
