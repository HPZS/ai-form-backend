package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"
)

const literalFieldsVersion = "literal-fields-v1"

// 这是候选字面边界；当前原文完整覆盖和逐字引用一致性仍由宿主逐块核验。
type textStructureCandidate struct {
	SchemaVersion   int                  `json:"schemaVersion"`
	Kind            string               `json:"kind"`
	RecordSeparator string               `json:"recordSeparator"`
	Fields          []textStructureField `json:"fields"`
}
type textStructureField struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Delimiter string `json:"delimiter"`
	Suffix    string `json:"suffix"`
}

func (v *textStructureCandidate) UnmarshalJSON(data []byte) error {
	type plain textStructureCandidate
	return strictTextObject(data, (*plain)(v), "schemaVersion", "kind", "recordSeparator", "fields")
}
func (v *textStructureField) UnmarshalJSON(data []byte) error {
	type plain textStructureField
	return strictTextObject(data, (*plain)(v), "name", "label", "delimiter", "suffix")
}
func strictTextObject(data []byte, out any, keys ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	if len(object) != len(keys) {
		return fmt.Errorf("textStructure 字段缺失或包含未支持字段")
	}
	for _, key := range keys {
		if len(object[key]) == 0 || bytes.Equal(bytes.TrimSpace(object[key]), []byte("null")) {
			return fmt.Errorf("textStructure 缺少非空字段 %s", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}
func validateStructureLearning(sourceKind, version string) error {
	if version != "" && (version != literalFieldsVersion || sourceKind != "text") {
		return fmt.Errorf("structureLearning 仅支持 text 的 literal-fields-v1")
	}
	return nil
}
func textBoundary(value string) bool {
	if len(utf16.Encode([]rune(value))) > 16 {
		return false
	}
	for _, r := range value {
		if !unicode.IsPunct(r) && !unicode.IsSymbol(r) && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
func validateTextStructureOutput(req *CompileInputReq, out *compiledInputPlan) error {
	if req.StructureLearning == "" {
		if out.TextStructure != nil || out.StructureDiagnostic != "" {
			return fmt.Errorf("未请求文本结构学习，不能夹带 textStructure")
		}
		return nil
	}
	if err := validateStructureLearning(req.SourceKind, req.StructureLearning); err != nil {
		return err
	}
	if err := capStr("structureDiagnostic", out.StructureDiagnostic, 1000); err != nil {
		return err
	}
	v := out.TextStructure
	if v == nil {
		if strings.TrimSpace(out.StructureDiagnostic) == "" {
			return fmt.Errorf("缺少 textStructure 时必须说明不可复用的 structureDiagnostic")
		}
		return nil
	}
	if v.SchemaVersion != 1 || v.Kind != literalFieldsVersion || !textBoundary(v.RecordSeparator) || len(v.Fields) == 0 || len(v.Fields) > SourceColumnLimit {
		return fmt.Errorf("textStructure 版本、记录界符或字段无效")
	}
	seen := map[string]bool{}
	for index, field := range v.Fields {
		if field.Name == "" || field.Name != strings.TrimSpace(field.Name) || field.Name != field.Label || len(utf16.Encode([]rune(field.Name))) > 200 || strings.ContainsAny(field.Name, "\r\n") || seen[field.Name] || field.Delimiter == "" || !textBoundary(field.Delimiter) || !textBoundary(field.Suffix) || (index == len(v.Fields)-1 && field.Suffix != "") || (index < len(v.Fields)-1 && field.Suffix == "") {
			return fmt.Errorf("textStructure 字段必须保留原文唯一标签和有限界符，最后 suffix 为空")
		}
		seen[field.Name] = true
	}
	for _, record := range out.Records {
		for _, field := range record.Fields {
			if !seen[field.Name] {
				return fmt.Errorf("语义字段不属于 textStructure 原始标签")
			}
		}
	}
	return nil
}
