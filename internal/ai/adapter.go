package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const AdapterRuntimeVersion = "adapter-runtime-v1"

var adapterID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,79}$`)
var adapterDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var adapterAttribute = regexp.MustCompile(`^(?:id|name|type|role|title|readonly|disabled|checked|selected|aria-[a-z-]+|data-[a-z0-9-]+)$`)
var adapterFunction = regexp.MustCompile(`^\s*function\s+next\s*\(\s*parameters\s*,\s*observation\s*,\s*state\s*,\s*lastOutcome\s*\)\s*\{`)

type AdapterParameter struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	SourcePath  string `json:"sourcePath,omitempty"`
	Description string `json:"description,omitempty"`
}

type AdapterLimits struct {
	MaxSourceBytes    int `json:"maxSourceBytes"`
	MaxInputBytes     int `json:"maxInputBytes"`
	MaxOutputBytes    int `json:"maxOutputBytes"`
	MaxSteps          int `json:"maxSteps"`
	MaxDepth          int `json:"maxDepth"`
	MaxCollectionSize int `json:"maxCollectionSize"`
	MaxTimeMs         int `json:"maxTimeMs"`
	MaxActions        int `json:"maxActions"`
	MaxReads          int `json:"maxReads"`
	MaxCallDepth      int `json:"maxCallDepth"`
	MaxRunMs          int `json:"maxRunMs"`
}

func (l AdapterLimits) Validate() error {
	values := []int{l.MaxSourceBytes, l.MaxInputBytes, l.MaxOutputBytes, l.MaxSteps, l.MaxDepth, l.MaxCollectionSize, l.MaxTimeMs, l.MaxActions, l.MaxReads, l.MaxCallDepth, l.MaxRunMs}
	caps := []int{32768, 262144, 65536, 100000, 64, 4096, 100, 256, 256, 8, 300000}
	for i, n := range values {
		if n < 1 || n > caps[i] {
			return fmt.Errorf("limits 第 %d 项必须为 1..%d", i+1, caps[i])
		}
	}
	return nil
}

type CompileAdapterReq struct {
	Meta
	SchemaVersion           int                `json:"schemaVersion"`
	RuntimeVersion          string             `json:"runtimeVersion"`
	Mode                    string             `json:"mode"`
	BaseRevision            *string            `json:"baseRevision"`
	SourceContractDigest    string             `json:"sourceContractDigest"`
	FormContractDigest      string             `json:"formContractDigest"`
	UserInstructionRevision string             `json:"userInstructionRevision"`
	ToolVersion             string             `json:"toolVersion"`
	Goal                    string             `json:"goal"`
	Scope                   json.RawMessage    `json:"scope"`
	Parameters              []AdapterParameter `json:"parameters"`
	SourceContract          json.RawMessage    `json:"sourceContract"`
	Observation             json.RawMessage    `json:"observation"`
	ExistingModules         []AdapterModule    `json:"existingModules,omitempty"`
	FailureContext          json.RawMessage    `json:"failureContext,omitempty"`
	Limits                  AdapterLimits      `json:"limits"`
}

func (r *CompileAdapterReq) UnmarshalJSON(data []byte) error {
	// baseRevision=null 表达确实没有基线；省略绑定字段不能与它混同。
	type wire CompileAdapterReq
	var decoded wire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, ok := fields["baseRevision"]; !ok {
		return fmt.Errorf("缺少 baseRevision 生成绑定")
	}
	*r = CompileAdapterReq(decoded)
	return nil
}

func (r *CompileAdapterReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if r.SchemaVersion != 1 || r.RuntimeVersion != AdapterRuntimeVersion {
		return fmt.Errorf("适配协议或运行时版本不支持")
	}
	if r.Mode != "create" && r.Mode != "repair" {
		return fmt.Errorf("mode 必须为 create 或 repair")
	}
	if r.CallPhase != "adapter-"+r.Mode {
		return fmt.Errorf("适配调用阶段必须与 mode 一致")
	}
	for name, value := range map[string]string{"compilationId": r.CompilationID, "userInstructionRevision": r.UserInstructionRevision, "toolVersion": r.ToolVersion} {
		if strings.TrimSpace(value) == "" || len(value) > 128 {
			return fmt.Errorf("%s 缺失或超长", name)
		}
	}
	if !adapterDigest.MatchString(r.SourceContractDigest) || !adapterDigest.MatchString(r.FormContractDigest) {
		return fmt.Errorf("资料及表单契约摘要必须为 SHA-256")
	}
	if r.BaseRevision != nil && !adapterDigest.MatchString(*r.BaseRevision) {
		return fmt.Errorf("baseRevision 必须为 null 或 SHA-256")
	}
	if r.Mode == "repair" && (r.BaseRevision == nil || len(r.FailureContext) == 0) {
		return fmt.Errorf("repair 必须携带 baseRevision 和 failureContext")
	}
	if strings.TrimSpace(r.Goal) == "" || len([]rune(r.Goal)) > 2000 {
		return fmt.Errorf("goal 缺失或超长")
	}
	for _, part := range []struct {
		name  string
		raw   json.RawMessage
		limit int
	}{{"scope", r.Scope, 8192}, {"sourceContract", r.SourceContract, 65536}, {"observation", r.Observation, 524288}} {
		if err := validateAdapterJSON(part.name, part.raw, part.limit, true); err != nil {
			return err
		}
	}
	if len(r.FailureContext) > 0 {
		if err := validateAdapterJSON("failureContext", r.FailureContext, 65536, true); err != nil {
			return err
		}
	}
	if r.Parameters == nil || len(r.Parameters) > 128 {
		return fmt.Errorf("parameters 缺失或超过 128 项")
	}
	seen := map[string]bool{}
	for _, p := range r.Parameters {
		if !adapterID.MatchString(p.ID) || seen[p.ID] {
			return fmt.Errorf("参数 id 非法或重复")
		}
		seen[p.ID] = true
		if !inStrings([]string{"string", "number", "boolean", "json"}, p.Type) {
			return fmt.Errorf("参数 type 非法")
		}
		if len(p.SourcePath) > 1024 || len([]rune(p.Description)) > 500 {
			return fmt.Errorf("参数来源或说明超长")
		}
	}
	if err := r.Limits.Validate(); err != nil {
		return err
	}
	return validateAdapterModules(r.ExistingModules, false, r.Limits.MaxSourceBytes)
}

type AdapterModule struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}
type AdapterLocator struct {
	CSS        string            `json:"css,omitempty"`
	Text       string            `json:"text,omitempty"`
	Tag        string            `json:"tag,omitempty"`
	Role       string            `json:"role,omitempty"`
	Name       string            `json:"name,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}
type AdapterQuery struct {
	ID      string         `json:"id"`
	Within  string         `json:"within,omitempty"`
	Locator AdapterLocator `json:"locator"`
	Limit   int            `json:"limit"`
}
type AdapterEffect struct {
	ID          string          `json:"id"`
	QueryRef    string          `json:"queryRef"`
	Kind        string          `json:"kind"`
	ParameterID string          `json:"parameterId,omitempty"`
	Attribute   string          `json:"attribute,omitempty"`
	Value       json.RawMessage `json:"value,omitempty"`
}
type AdapterEntrypoints struct {
	Prepare    string `json:"prepare,omitempty"`
	FillRecord string `json:"fillRecord"`
}
type AdapterHandoff struct {
	ID                string          `json:"id"`
	Goal              string          `json:"goal"`
	ReturnContractRef string          `json:"returnContractRef"`
	ResumeState       json.RawMessage `json:"resumeState"`
	ParameterIDs      []string        `json:"parameterIds,omitempty"`
}
type CompileAdapterOutput struct {
	SchemaVersion           int                `json:"schemaVersion"`
	RuntimeVersion          string             `json:"runtimeVersion"`
	CompilationID           string             `json:"compilationId"`
	BaseRevision            *string            `json:"baseRevision"`
	SourceContractDigest    string             `json:"sourceContractDigest"`
	FormContractDigest      string             `json:"formContractDigest"`
	UserInstructionRevision string             `json:"userInstructionRevision"`
	ToolVersion             string             `json:"toolVersion"`
	Modules                 []AdapterModule    `json:"modules"`
	Queries                 []AdapterQuery     `json:"queries"`
	Effects                 []AdapterEffect    `json:"effects"`
	Entrypoints             AdapterEntrypoints `json:"entrypoints"`
	AIHandoffs              []AdapterHandoff   `json:"aiHandoffs"`
	Diagnostics             []string           `json:"diagnostics"`
	UnverifiedConditions    []string           `json:"unverifiedConditions"`
}

// 仅检查有界协议。解析受限 AST、沙箱试运行、可信覆盖和发布均由插件执行。
func validateCompileAdapterOutput(r *CompileAdapterReq, content string) (CompileAdapterOutput, error) {
	var out CompileAdapterOutput
	if len(content) > 768*1024 {
		return out, fmt.Errorf("适配响应超过 768 KiB")
	}
	raw, err := extractJSONObject(content)
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("适配响应结构非法: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return out, fmt.Errorf("适配响应包含多个 JSON 值")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return out, err
	}
	for _, key := range []string{"schemaVersion", "runtimeVersion", "compilationId", "baseRevision", "sourceContractDigest", "formContractDigest", "userInstructionRevision", "toolVersion", "modules", "queries", "effects", "entrypoints", "aiHandoffs", "diagnostics", "unverifiedConditions"} {
		if v, ok := keys[key]; !ok || (key != "baseRevision" && bytes.Equal(v, []byte("null"))) {
			return out, fmt.Errorf("适配响应缺少 %s", key)
		}
	}
	sameBase := out.BaseRevision == nil && r.BaseRevision == nil || out.BaseRevision != nil && r.BaseRevision != nil && *out.BaseRevision == *r.BaseRevision
	if out.SchemaVersion != r.SchemaVersion || out.RuntimeVersion != r.RuntimeVersion || out.CompilationID != r.CompilationID || !sameBase || out.SourceContractDigest != r.SourceContractDigest || out.FormContractDigest != r.FormContractDigest || out.UserInstructionRevision != r.UserInstructionRevision || out.ToolVersion != r.ToolVersion {
		return out, fmt.Errorf("适配响应版本或生成绑定不一致")
	}
	if err := validateAdapterModules(out.Modules, true, r.Limits.MaxSourceBytes); err != nil {
		return out, err
	}
	modules := map[string]bool{}
	for _, m := range out.Modules {
		modules[m.ID] = true
	}
	if !modules[out.Entrypoints.FillRecord] || (out.Entrypoints.Prepare != "" && !modules[out.Entrypoints.Prepare]) {
		return out, fmt.Errorf("entrypoints 引用未知模块")
	}
	if len(out.Queries) > 64 || len(out.Effects) > 64 || len(out.AIHandoffs) > 16 {
		return out, fmt.Errorf("查询、效果或移交数量超限")
	}
	queries := map[string]AdapterQuery{}
	for _, q := range out.Queries {
		if !adapterID.MatchString(q.ID) || queries[q.ID].ID != "" || q.Limit < 1 || q.Limit > 200 {
			return out, fmt.Errorf("查询 id 或 limit 非法")
		}
		if err := q.Locator.Validate(); err != nil {
			return out, err
		}
		queries[q.ID] = q
	}
	for _, q := range out.Queries {
		seen := map[string]bool{q.ID: true}
		for parent := q.Within; parent != ""; parent = queries[parent].Within {
			if seen[parent] || queries[parent].ID == "" {
				return out, fmt.Errorf("within 引用未知父查询或形成循环")
			}
			seen[parent] = true
		}
	}
	params := map[string]bool{}
	for _, p := range r.Parameters {
		params[p.ID] = true
	}
	effects := map[string]bool{}
	for _, e := range out.Effects {
		if !adapterID.MatchString(e.ID) || effects[e.ID] || queries[e.QueryRef].ID == "" {
			return out, fmt.Errorf("效果 id 或 queryRef 非法")
		}
		effects[e.ID] = true
		if !inStrings([]string{"visible", "hidden", "value-equals", "text-equals", "attribute-equals", "changed"}, e.Kind) {
			return out, fmt.Errorf("效果 kind 非法")
		}
		compares := inStrings([]string{"value-equals", "text-equals", "attribute-equals"}, e.Kind)
		if compares && ((e.ParameterID == "") == (len(e.Value) == 0)) {
			return out, fmt.Errorf("比较效果必须且只能声明参数或固定值")
		}
		if !compares && (e.ParameterID != "" || len(e.Value) > 0 || e.Attribute != "") {
			return out, fmt.Errorf("非比较效果不得携带比较值")
		}
		if e.Kind != "attribute-equals" && e.Attribute != "" {
			return out, fmt.Errorf("此效果不读取属性")
		}
		if e.ParameterID != "" && !params[e.ParameterID] {
			return out, fmt.Errorf("效果引用未知参数")
		}
		if len(e.Value) > 0 {
			var scalar any
			if err := json.Unmarshal(e.Value, &scalar); err != nil {
				return out, err
			}
			switch scalar.(type) {
			case nil, bool, float64, string:
			default:
				return out, fmt.Errorf("效果 value 必须为标量")
			}
			if len(e.Value) > 4096 {
				return out, fmt.Errorf("效果 value 超长")
			}
		}
		if e.Kind == "attribute-equals" && !allowedAdapterAttribute(e.Attribute) {
			return out, fmt.Errorf("效果读取属性未获允许")
		}
	}
	handoffs := map[string]bool{}
	for _, h := range out.AIHandoffs {
		if !adapterID.MatchString(h.ID) || handoffs[h.ID] || !effects[h.ReturnContractRef] || strings.TrimSpace(h.Goal) == "" || len([]rune(h.Goal)) > 2000 || len(h.ParameterIDs) > 64 {
			return out, fmt.Errorf("AI 移交声明非法")
		}
		handoffs[h.ID] = true
		if err := validateAdapterJSON("resumeState", h.ResumeState, 65536, false); err != nil {
			return out, err
		}
		seen := map[string]bool{}
		for _, p := range h.ParameterIDs {
			if !params[p] || seen[p] {
				return out, fmt.Errorf("AI 移交参数非法")
			}
			seen[p] = true
		}
	}
	for _, list := range [][]string{out.Diagnostics, out.UnverifiedConditions} {
		if len(list) > 32 {
			return out, fmt.Errorf("编译诊断或待验证条件超过 32 项")
		}
		for _, item := range list {
			if item == "" || len([]rune(item)) > 1000 {
				return out, fmt.Errorf("编译诊断或待验证条件为空或超长")
			}
		}
	}
	return out, nil
}

func validateAdapterModules(modules []AdapterModule, required bool, maxBytes int) error {
	if len(modules) > 16 || (required && len(modules) == 0) {
		return fmt.Errorf("modules 数量必须为 1..16")
	}
	seen := map[string]bool{}
	for _, m := range modules {
		if !adapterID.MatchString(m.ID) || seen[m.ID] {
			return fmt.Errorf("模块 id 非法或重复")
		}
		seen[m.ID] = true
		if len(m.Source) > maxBytes || !adapterFunction.MatchString(m.Source) || !strings.HasSuffix(strings.TrimSpace(m.Source), "}") {
			return fmt.Errorf("模块源码超过预算或缺少 function next 标准入口")
		}
	}
	return nil
}

func allowedAdapterAttribute(name string) bool {
	if name == "" || name != strings.ToLower(name) || len(name) > 100 {
		return false
	}
	return adapterAttribute.MatchString(name)
}

func (l AdapterLocator) Validate() error {
	count := len(l.Attributes)
	for _, value := range []string{l.CSS, l.Text, l.Tag, l.Role, l.Name} {
		if len([]rune(value)) > 1000 {
			return fmt.Errorf("locator 超长")
		}
		if value != "" {
			count++
		}
	}
	if count == 0 || len(l.Attributes) > 16 {
		return fmt.Errorf("locator 缺少过滤条件或属性超过 16 项")
	}
	for name, value := range l.Attributes {
		if !allowedAdapterAttribute(name) || len([]rune(value)) > 1000 {
			return fmt.Errorf("locator 属性未获允许或超长")
		}
	}
	return nil
}

func validateAdapterJSON(name string, raw json.RawMessage, maxBytes int, object bool) error {
	if len(raw) == 0 || len(raw) > maxBytes {
		return fmt.Errorf("%s 缺失或超过 %d 字节", name, maxBytes)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s 不是合法 JSON", name)
	}
	if object {
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("%s 必须为 JSON 对象", name)
		}
	}
	nodes := 0
	var visit func(any, int) error
	visit = func(value any, depth int) error {
		nodes++
		if depth > 32 || nodes > 20000 {
			return fmt.Errorf("%s 嵌套或节点数超过预算", name)
		}
		switch item := value.(type) {
		case map[string]any:
			for _, v := range item {
				if err := visit(v, depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, v := range item {
				if err := visit(v, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value, 0)
}

func adapterRepairMessage(req Request, validation error) string {
	r := req.(*CompileAdapterReq)
	binding, _ := json.Marshal(map[string]any{"schemaVersion": r.SchemaVersion, "runtimeVersion": r.RuntimeVersion, "compilationId": r.CompilationID, "baseRevision": r.BaseRevision, "sourceContractDigest": r.SourceContractDigest, "formContractDigest": r.FormContractDigest, "userInstructionRevision": r.UserInstructionRevision, "toolVersion": r.ToolVersion})
	return "上次候选未通过协议校验：" + safeValidationReason(validation) + "。请保持原 SDK 和权限边界，重新输出完整候选 JSON。必须原样回显当前绑定：" + string(binding) + "。不得返回 coverage、active 状态或可信成功回执。"
}
