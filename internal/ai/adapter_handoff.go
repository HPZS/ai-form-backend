package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// AdapterHandoffContext 只描述宿主冻结版本的局部子目标，不增加工具、节点或来源权限。
type AdapterHandoffContext struct {
	ProgramID         string                   `json:"programId"`
	Revision          string                   `json:"revision"`
	ModuleID          string                   `json:"moduleId"`
	Goal              string                   `json:"goal"`
	ReturnContractRef string                   `json:"returnContractRef,omitempty"`
	Selection         *AdapterHandoffSelection `json:"selection,omitempty"`
}

type AdapterHandoffSelection struct {
	CandidateQueryRef string `json:"candidateQueryRef"`
	ValueQueryRef     string `json:"valueQueryRef,omitempty"`
	Read              string `json:"read"`
	Attribute         string `json:"attribute,omitempty"`
}

func (v *AdapterHandoffSelection) UnmarshalJSON(data []byte) error {
	type plain AdapterHandoffSelection
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"candidateQueryRef", "read"} {
		if len(fields[key]) == 0 {
			return fmt.Errorf("语义选择契约缺少 %s", key)
		}
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) || bytes.Equal(field, []byte(`""`)) {
			return fmt.Errorf("语义选择契约不得携带空值")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode((*plain)(v)); err != nil {
		return err
	}
	return validateAdapterHandoffSelection(v)
}
func validateAdapterHandoffSelection(v *AdapterHandoffSelection) error {
	if v == nil {
		return nil
	}
	if !adapterID.MatchString(v.CandidateQueryRef) || v.ValueQueryRef != "" && !adapterID.MatchString(v.ValueQueryRef) || !inStrings([]string{"text", "value", "attribute"}, v.Read) {
		return fmt.Errorf("语义选择查询或读取方式非法")
	}
	if v.Read == "attribute" {
		if !allowedAdapterAttribute(v.Attribute) {
			return fmt.Errorf("语义选择属性未获允许")
		}
	} else if v.Attribute != "" {
		return fmt.Errorf("非属性读取不得声明attribute")
	}
	return nil
}

func validateAdapterHandoff(meta Meta, value *AdapterHandoffContext) error {
	if value == nil {
		return nil
	} // 旧客户端仍可只传计费移交身份。
	if meta.CallPhase != "runtime-handoff" || meta.HandoffID == "" {
		return fmt.Errorf("局部子目标必须绑定当前移交身份")
	}
	if !adapterDigest.MatchString(value.ProgramID) || !adapterDigest.MatchString(value.Revision) || !adapterID.MatchString(value.ModuleID) || strings.TrimSpace(value.Goal) == "" || len([]rune(value.Goal)) > 2000 || value.ReturnContractRef != "" && !adapterID.MatchString(value.ReturnContractRef) {
		return fmt.Errorf("局部子目标的版本或范围无效")
	}
	return validateAdapterHandoffSelection(value.Selection)
}
