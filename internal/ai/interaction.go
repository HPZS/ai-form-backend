package ai

import (
	"encoding/json"
	"fmt"
	"unicode/utf16"
)

// 新客户端显式声明版本，旧客户端保持 v1 动作协议；状态摘要不产生网页权限。
func (r *AgentStepReq) validateInteraction() error {
	if r.InteractionVersion != 0 && r.InteractionVersion != 1 {
		return fmt.Errorf("interactionVersion 不支持")
	}
	if r.InteractionVersion == 0 && (r.ValueSemantics != nil || r.AllowedNodeIDs != nil) {
		return fmt.Errorf("交互语义缺少版本声明")
	}
	if err := capStr("sourceColumnRef", r.SourceColumnRef, 300); err != nil {
		return err
	}
	if err := capStr("scopeRevision", r.ScopeRevision, 200); err != nil {
		return err
	}
	if err := checkStringList("allowedNodeIds", r.AllowedNodeIDs, 20000, 100); err != nil {
		return err
	}
	for _, id := range r.AllowedNodeIDs {
		if !inStrings(r.ProjectedNodeIDs, id) || inStrings(r.ForbiddenNodeIDs, id) {
			return fmt.Errorf("allowedNodeIds 必须属于当前投影且未禁止")
		}
	}
	for _, item := range r.History {
		if err := validateInteractionHistory(item); err != nil {
			return err
		}
	}
	s := r.ValueSemantics
	if len(r.ValueObservations) > 200 {
		return fmt.Errorf("valueObservations 数量超限")
	}
	for _, observed := range r.ValueObservations {
		if r.InteractionVersion != 1 || s == nil || !inStrings(r.ProjectedNodeIDs, observed.NodeID) || !inStrings([]string{"field", "control"}, observed.Kind) {
			return fmt.Errorf("局部比较必须绑定当前节点与来源")
		}
		ids := []string{}
		for _, part := range s.Parts {
			ids = append(ids, part.ID)
		}
		for _, id := range append(append([]string{}, observed.MatchingParts...), observed.MissingParts...) {
			if !inStrings(ids, id) {
				return fmt.Errorf("局部比较引用未知来源参数")
			}
		}
	}
	if s == nil {
		return nil
	}
	if s.Version != 1 || !inStrings([]string{"text", "date", "datetime", "time", "set"}, s.Kind) || !inStrings([]string{"", "year", "month", "day", "hour", "minute", "second"}, s.Precision) {
		return fmt.Errorf("valueSemantics 版本、类型或精度非法")
	}
	if len(s.Parts) == 0 || len(s.Parts) > 200 {
		return fmt.Errorf("valueSemantics.parts 数量必须为 1..200")
	}
	ids, refs := map[string]bool{}, map[string]bool{}
	for _, part := range s.Parts {
		if part.ID == "" || part.Ref == "" || ids[part.ID] || refs[part.Ref] {
			return fmt.Errorf("来源子值标识和句柄必须非空且唯一")
		}
		ids[part.ID], refs[part.Ref] = true, true
		for name, spec := range map[string]struct {
			value string
			limit int
		}{"part.id": {part.ID, 100}, "part.ref": {part.Ref, 300}, "part.hint": {part.Hint, 160}} {
			if err := capStr(name, spec.value, spec.limit); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateInteractionHistory(item AgentHistoryItem) error {
	if !inStrings([]string{"", "not-dispatched", "dispatched", "unknown"}, item.Dispatch) || !inStrings([]string{"", "met", "not-met", "unknown"}, item.Effect) || !inStrings([]string{"", "satisfied", "unsatisfied", "unknown"}, item.GoalVerdict) {
		return fmt.Errorf("history 交互结果枚举非法")
	}
	for name, value := range map[string]string{"beforeState": item.BeforeState, "afterState": item.AfterState, "failureCode": item.FailureCode} {
		if err := capStr(name, value, 100); err != nil {
			return err
		}
	}
	return nil
}

func (r *AgentStepReq) hasValueRef(ref string) bool {
	if ref == r.ValueRef {
		return true
	}
	if r.InteractionVersion == 1 && r.ValueSemantics != nil {
		for _, part := range r.ValueSemantics.Parts {
			if part.Ref == ref {
				return true
			}
		}
	}
	return false
}

func validateInteractionAction(req *AgentStepReq, out agentStepOutput, action agentAction) error {
	keys := map[string][]string{
		"click": {"targetNodeId", "valuePart"}, "focus": {"targetNodeId"}, "replace-text": {"targetNodeId", "valueRef", "transform", "valueSlice"},
		"press-key": {"targetNodeId", "key"}, "select-native": {"targetNodeId", "optionNodeId", "selected"},
		"run-skill": {"targetNodeId", "skillId", "inputRefs"}, "scroll-node": {"targetNodeId", "block"}, "scroll-page": {"direction", "amount"},
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Action, &raw); err != nil {
		return err
	}
	for key, value := range raw {
		if key != "op" && !inStrings(keys[action.Op], key) || string(value) == "null" {
			return fmt.Errorf("当前 action 包含不允许或空字段 %s", key)
		}
	}
	if action.ValuePart != "" {
		found := false
		if req.ValueSemantics != nil {
			for _, part := range req.ValueSemantics.Parts {
				if part.ID == action.ValuePart {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("valuePart 未绑定当前来源")
		}
	}
	if len(out.ExpectedEvidence) > 8 {
		return fmt.Errorf("expectedEvidence 数量超限")
	}
	if slice := action.ValueSlice; slice != nil {
		hint := ""
		if req.ValueSemantics != nil {
			for _, part := range req.ValueSemantics.Parts {
				if part.Ref == action.ValueRef || action.ValueRef == req.ValueRef && part.ID == "value" {
					hint = part.Hint
				}
			}
		}
		if slice.Start < 0 || slice.End <= slice.Start || slice.End > 160 || slice.End > len(utf16.Encode([]rune(hint))) || action.Transform != "" && action.Transform != "identity" {
			return fmt.Errorf("来源切片超出已披露范围")
		}
	}
	for _, evidence := range out.ExpectedEvidence {
		kind, _ := evidence["kind"].(string)
		if !inStrings([]string{"field-value-match", "state-change", "visible", "hidden", "expanded", "collapsed", "selected", "ready"}, kind) {
			return fmt.Errorf("预期效果类型非法")
		}
		for key, value := range evidence {
			if key == "nodeId" {
				id, ok := value.(string)
				if !ok || !inStrings(req.ProjectedNodeIDs, id) {
					return fmt.Errorf("效果引用未投影节点")
				}
			} else if key != "kind" {
				return fmt.Errorf("预期效果包含未知字段")
			}
		}
	}
	return nil
}
