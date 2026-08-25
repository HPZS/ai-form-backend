// ai-form-backend - AGPL-3.0
// AI 能力的输出解析与确定性校验,镜像插件 utils/ai.ts 的逻辑:
// 丢弃模型幻觉出的不存在的 selector/列名/字段序号(插件端还有最后一道防线)。
package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
)

type agentAction struct {
	Op                 string   `json:"op"`
	TargetNodeID       string   `json:"targetNodeId,omitempty"`
	ValueRef           string   `json:"valueRef,omitempty"`
	Transform          string   `json:"transform,omitempty"`
	Key                string   `json:"key,omitempty"`
	OptionNodeID       string   `json:"optionNodeId,omitempty"`
	FileSetRef         string   `json:"fileSetRef,omitempty"`
	SkillID            string   `json:"skillId,omitempty"`
	InputRefs          []string `json:"inputRefs,omitempty"`
	Block              string   `json:"block,omitempty"`
	Direction          string   `json:"direction,omitempty"`
	Amount             string   `json:"amount,omitempty"`
	Kind               string   `json:"kind,omitempty"`
	AuthorizationRef   string   `json:"authorizationRef,omitempty"`
	PreconditionDigest string   `json:"preconditionDigest,omitempty"`
}

type agentStepOutput struct {
	SchemaVersion    string           `json:"schemaVersion"`
	SnapshotID       string           `json:"snapshotId"`
	ContextDigest    string           `json:"contextDigest"`
	GoalID           string           `json:"goalId"`
	Status           string           `json:"status"`
	Explanation      string           `json:"explanation"`
	Action           json.RawMessage  `json:"action,omitempty"`
	ContextRequest   json.RawMessage  `json:"contextRequest,omitempty"`
	ExpectedEvidence []map[string]any `json:"expectedEvidence,omitempty"`
	UserRequest      string           `json:"userRequest,omitempty"`
}

type agentContextRequest struct {
	Kind      string   `json:"kind"`
	NodeIDs   []string `json:"nodeIds,omitempty"`
	RegionIDs []string `json:"regionIds,omitempty"`
	Reason    string   `json:"reason"`
}

func inStrings(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func decodeAgentAction(raw json.RawMessage) (agentAction, error) {
	var action agentAction
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&action); err != nil {
		return action, fmt.Errorf("action 非法: %w", err)
	}
	return action, nil
}

func validateAgentStepOutput(req *AgentStepReq, content string) (agentStepOutput, error) {
	var out agentStepOutput
	raw, err := extractJSONObject(content)
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("AgentStep JSON 非法: %w", err)
	}
	if out.SchemaVersion != "v1" || out.SnapshotID != req.SnapshotID || out.ContextDigest != req.ContextDigest || out.GoalID != req.GoalID {
		return out, fmt.Errorf("AgentStep 版本或上下文回显不一致")
	}
	if out.Explanation == "" {
		return out, fmt.Errorf("AgentStep 缺少 explanation")
	}
	switch out.Status {
	case "done", "need-context", "need-user", "blocked":
		if len(out.Action) > 0 && string(out.Action) != "null" {
			return out, fmt.Errorf("非 act 状态夹带 action")
		}
		if out.Status == "need-context" && len(out.ContextRequest) == 0 {
			return out, fmt.Errorf("need-context 缺少 contextRequest")
		}
		if out.Status == "need-context" {
			var request agentContextRequest
			dec := json.NewDecoder(bytes.NewReader(out.ContextRequest))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&request); err != nil {
				return out, fmt.Errorf("contextRequest 非法: %w", err)
			}
			if strings.TrimSpace(request.Reason) == "" {
				return out, fmt.Errorf("contextRequest 缺少 reason")
			}
			switch request.Kind {
			case "expand-nodes":
				if len(request.NodeIDs) == 0 {
					return out, fmt.Errorf("expand-nodes 缺少 nodeIds")
				}
				for _, id := range request.NodeIDs {
					if !inStrings(req.ProjectedNodeIDs, id) {
						return out, fmt.Errorf("contextRequest 引用了未投影 nodeId")
					}
				}
			case "region-detail":
				if len(request.RegionIDs) == 0 {
					return out, fmt.Errorf("region-detail 缺少 regionIds")
				}
				for _, id := range request.RegionIDs {
					if !inStrings(req.ProjectedRegionIDs, id) {
						return out, fmt.Errorf("contextRequest 引用了未投影 regionId")
					}
				}
			case "local-screenshot":
				if !req.VisualAuthorized {
					return out, fmt.Errorf("local-screenshot 未取得视觉授权")
				}
				if len(request.RegionIDs) == 0 {
					return out, fmt.Errorf("local-screenshot 缺少 regionIds")
				}
				for _, id := range request.RegionIDs {
					if !inStrings(req.ProjectedRegionIDs, id) {
						return out, fmt.Errorf("contextRequest 引用了未投影 regionId")
					}
				}
			default:
				return out, fmt.Errorf("contextRequest kind 非法")
			}
		}
		return out, nil
	case "act":
		if len(out.Action) == 0 || string(out.Action) == "null" {
			return out, fmt.Errorf("act 状态缺少 action")
		}
	default:
		return out, fmt.Errorf("AgentStep status 非法")
	}
	action, err := decodeAgentAction(out.Action)
	if err != nil {
		return out, err
	}
	projected := func(id string) bool {
		return id != "" && inStrings(req.ProjectedNodeIDs, id) && !inStrings(req.ForbiddenNodeIDs, id)
	}
	if action.TargetNodeID != "" && !projected(action.TargetNodeID) {
		return out, fmt.Errorf("action 引用了未投影或禁止的 targetNodeId")
	}
	if action.OptionNodeID != "" && !projected(action.OptionNodeID) {
		return out, fmt.Errorf("action 引用了未投影的 optionNodeId")
	}
	if inStrings(req.KnownSubmitNodeIDs, action.TargetNodeID) && action.Op != "commit-form" {
		return out, fmt.Errorf("提交节点只能使用 commit-form")
	}
	safeKeys := []string{"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Escape", "Tab", "Space", "Backspace"}
	switch action.Op {
	case "click", "focus", "scroll-node":
		if !projected(action.TargetNodeID) {
			return out, fmt.Errorf("%s 缺少合法 targetNodeId", action.Op)
		}
	case "replace-text":
		if !projected(action.TargetNodeID) || action.ValueRef == "" || action.ValueRef != req.ValueRef {
			return out, fmt.Errorf("replace-text 引用了未授权目标或值句柄")
		}
		transform := action.Transform
		if transform == "" {
			transform = "identity"
		}
		if !inStrings(req.AllowedTransforms, transform) {
			return out, fmt.Errorf("replace-text 使用了未授权转换")
		}
	case "press-key":
		if !inStrings(safeKeys, action.Key) {
			return out, fmt.Errorf("press-key 使用了非白名单按键")
		}
	case "select-native":
		if !projected(action.TargetNodeID) || !projected(action.OptionNodeID) {
			return out, fmt.Errorf("select-native 节点非法")
		}
	case "run-skill":
		if !inStrings(req.Skills, action.SkillID) {
			return out, fmt.Errorf("run-skill 引用了未公布技能")
		}
		for _, ref := range action.InputRefs {
			if ref != req.ValueRef {
				return out, fmt.Errorf("run-skill 引用了未授权句柄")
			}
		}
	case "scroll-page":
		if (action.Direction != "up" && action.Direction != "down") || (action.Amount != "small" && action.Amount != "page") {
			return out, fmt.Errorf("scroll-page 参数非法")
		}
	case "set-files":
		return out, fmt.Errorf("第一版 agent_step 不开放文件动作")
	case "commit-form":
		return out, fmt.Errorf("agent_step 第一版不生成不可逆动作")
	default:
		return out, fmt.Errorf("未知 action op %q", action.Op)
	}
	return out, nil
}

func validateAgentVisualGroundOutput(req *AgentVisualGroundReq, content string) (any, error) {
	var out struct {
		CandidateNodeID *string `json:"candidateNodeId"`
		Confidence      float64 `json:"confidence"`
		Reason          string  `json:"reason"`
	}
	if err := ParseAIJSONStrict(content, &out, "candidateNodeId", "confidence", "reason"); err != nil {
		return nil, err
	}
	if out.Confidence < 0 || out.Confidence > 1 {
		return nil, fmt.Errorf("视觉 confidence 必须在 0..1")
	}
	if strings.TrimSpace(out.Reason) == "" || len([]rune(out.Reason)) > 300 {
		return nil, fmt.Errorf("视觉 reason 为空或超长")
	}
	if out.CandidateNodeID != nil {
		found := false
		for _, candidate := range req.Candidates {
			if candidate.NodeID == *out.CandidateNodeID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("视觉结果引用了候选集外 nodeId")
		}
	}
	return out, nil
}

// logDropped 幻觉过滤留痕。被丢弃的选择器/列名此前零日志:既没法监控各上游的幻觉率
// (日志里的 request_id 可与 ai_requests 表的 upstream/model 对齐),也没法区分
// "模型答错"与"服务端过滤过严"——不留痕就等于放弃了这条质量反馈通道(守则 3.2)。
// 逐条打会被 200 字段的宽表单刷屏,故按 kind 聚合成一条并附前几个样本。
func logDropped(capability string, req Request, kind string, dropped []string) {
	if len(dropped) == 0 {
		return
	}
	sample := dropped
	if len(sample) > 3 {
		sample = sample[:3]
	}
	log.Printf("[AI-HALLUC] capability=%s request_id=%s 丢弃%s %d 项,例: %.200s",
		capability, req.GetMeta().RequestID, kind, len(dropped), strings.Join(sample, " | "))
}

func buttonExists(buttons []ButtonInfo, selector string) bool {
	for _, b := range buttons {
		if b.Selector == selector {
			return true
		}
	}
	return false
}

func headerExists(headers []string, h string) bool {
	for _, x := range headers {
		if x == h {
			return true
		}
	}
	return false
}

// mappedColumn match_columns 结果条目;PriceAfter 依据它判断"是否至少匹配上一列"
type mappedColumn struct {
	FieldIndex int     `json:"fieldIndex"`
	Column     *string `json:"column"`
}

type inputExclusion struct {
	SourceRef string `json:"sourceRef"`
	Reason    string `json:"reason"`
}

type jsonInputField struct {
	Name        string  `json:"name"`
	Pointer     string  `json:"pointer"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation"`
}

type textQuoteRef struct {
	Quote      string `json:"quote"`
	Occurrence int    `json:"occurrence"`
}

type textInputField struct {
	Name        string         `json:"name"`
	SourceRefs  []textQuoteRef `json:"sourceRefs"`
	Confidence  float64        `json:"confidence"`
	Explanation string         `json:"explanation"`
}

type textInputRecord struct {
	RecordID string           `json:"recordId"`
	Fields   []textInputField `json:"fields"`
}

type compiledInputPlan struct {
	SchemaVersion string            `json:"schemaVersion"`
	SourceHash    string            `json:"sourceHash"`
	SourceKind    string            `json:"sourceKind"`
	EntityName    string            `json:"entityName"`
	RecordPointer string            `json:"recordPointer"`
	RecordMode    string            `json:"recordMode"`
	Fields        []jsonInputField  `json:"fields"`
	Records       []textInputRecord `json:"records"`
	Exclusions    []inputExclusion  `json:"exclusions"`
	Confidence    float64           `json:"confidence"`
	Explanation   string            `json:"explanation"`
}

func decodePointerPart(part string) (string, error) {
	for i := 0; i < len(part); i++ {
		if part[i] != '~' {
			continue
		}
		if i+1 >= len(part) || (part[i+1] != '0' && part[i+1] != '1') {
			return "", fmt.Errorf("JSON Pointer 含非法转义")
		}
		i++
	}
	return strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~"), nil
}

func resolveInputPointer(root any, pointer string) (any, bool, error) {
	if pointer == "" {
		return root, true, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false, fmt.Errorf("JSON Pointer 必须以 / 开头")
	}
	current := root
	for _, raw := range strings.Split(pointer[1:], "/") {
		key, err := decodePointerPart(raw)
		if err != nil {
			return nil, false, err
		}
		switch node := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = node[key]
			if !ok {
				return nil, false, nil
			}
		case []any:
			if key == "" || (len(key) > 1 && key[0] == '0') {
				return nil, false, nil
			}
			idx, err := strconv.Atoi(key)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false, nil
			}
			current = node[idx]
		default:
			return nil, false, nil
		}
	}
	return current, true, nil
}

func scalarInputValue(value any) bool {
	if value == nil {
		return true
	}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if item != nil {
				if _, complex := item.(map[string]any); complex {
					return false
				}
				if _, nested := item.([]any); nested {
					return false
				}
			}
		}
		return true
	}
	_, object := value.(map[string]any)
	return !object
}

func quoteOccurrenceExists(source, quote string, occurrence int) bool {
	if quote == "" || occurrence < 0 {
		return false
	}
	from := 0
	for i := 0; i <= occurrence; i++ {
		at := strings.Index(source[from:], quote)
		if at < 0 {
			return false
		}
		from += at + len(quote)
	}
	return true
}

func validateCompiledInput(req *CompileInputReq, out *compiledInputPlan) error {
	if out.SchemaVersion != "v1" {
		return fmt.Errorf("schemaVersion 必须是 v1")
	}
	if out.SourceHash != req.SourceHash || out.SourceKind != req.SourceKind {
		return fmt.Errorf("AI 返回的来源标识与请求不一致")
	}
	if strings.TrimSpace(out.EntityName) == "" {
		return fmt.Errorf("entityName 不能为空")
	}
	if out.Confidence < 0 || out.Confidence > 1 {
		return fmt.Errorf("confidence 必须在 0 到 1 之间")
	}
	if req.SourceKind == "json" {
		var source any
		if err := json.Unmarshal([]byte(req.SourceText), &source); err != nil {
			return fmt.Errorf("sourceKind=json 但来源不是合法 JSON: %w", err)
		}
		selected, found, err := resolveInputPointer(source, out.RecordPointer)
		if err != nil || !found {
			return fmt.Errorf("recordPointer 不存在或无效")
		}
		var records []any
		switch out.RecordMode {
		case "object":
			records = []any{selected}
		case "array":
			var ok bool
			records, ok = selected.([]any)
			if !ok {
				return fmt.Errorf("recordMode=array 但来源节点不是数组")
			}
		default:
			return fmt.Errorf("recordMode 必须是 object 或 array")
		}
		if len(records) == 0 || len(out.Fields) == 0 || len(out.Fields) > 100 {
			return fmt.Errorf("记录或字段为空/超限")
		}
		names := map[string]bool{}
		for _, field := range out.Fields {
			name := strings.TrimSpace(field.Name)
			if name == "" || names[name] {
				return fmt.Errorf("字段名为空或重复")
			}
			names[name] = true
			seen := false
			for _, record := range records {
				if _, ok := record.(map[string]any); !ok {
					return fmt.Errorf("记录根不是对象")
				}
				value, found, err := resolveInputPointer(record, field.Pointer)
				if err != nil {
					return err
				}
				if found {
					seen = true
					if !scalarInputValue(value) {
						return fmt.Errorf("字段 %s 仍指向复杂对象", name)
					}
				}
			}
			if !seen {
				return fmt.Errorf("字段 %s 的来源不存在", name)
			}
		}
		return nil
	}

	if len(out.Records) == 0 || len(out.Records) > 5000 {
		return fmt.Errorf("records 为空或超限")
	}
	for _, record := range out.Records {
		if len(record.Fields) == 0 || len(record.Fields) > 100 {
			return fmt.Errorf("文本记录字段为空或超限")
		}
		names := map[string]bool{}
		for _, field := range record.Fields {
			name := strings.TrimSpace(field.Name)
			if name == "" || names[name] || len(field.SourceRefs) == 0 {
				return fmt.Errorf("文本字段名为空、重复或缺少来源引用")
			}
			names[name] = true
			for _, ref := range field.SourceRefs {
				if !quoteOccurrenceExists(req.SourceText, ref.Quote, ref.Occurrence) {
					return fmt.Errorf("文本字段 %s 引用了不存在的原文", name)
				}
			}
		}
	}
	return nil
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Specs 返回全部能力规格,按注册顺序即路由顺序。
func Specs() []Spec {
	return []Spec{
		{
			Name:   "compile_input",
			NewReq: func() Request { return &CompileInputReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*CompileInputReq)
				var out compiledInputPlan
				if err := ParseAIJSONStrict(content, &out,
					"schemaVersion", "sourceHash", "sourceKind", "entityName", "exclusions", "confidence", "explanation"); err != nil {
					return nil, err
				}
				if r.SourceKind == "json" {
					var probe map[string]json.RawMessage
					raw, _ := extractJSONObject(content)
					if err := json.Unmarshal([]byte(raw), &probe); err != nil {
						return nil, err
					}
					for _, key := range []string{"recordPointer", "recordMode", "fields"} {
						if _, ok := probe[key]; !ok {
							return nil, fmt.Errorf("AI 输出缺少必填字段 %q", key)
						}
					}
				} else {
					var probe map[string]json.RawMessage
					raw, _ := extractJSONObject(content)
					if err := json.Unmarshal([]byte(raw), &probe); err != nil {
						return nil, err
					}
					if _, ok := probe["records"]; !ok {
						return nil, fmt.Errorf("AI 输出缺少必填字段 %q", "records")
					}
				}
				if err := validateCompiledInput(r, &out); err != nil {
					return nil, err
				}
				return out, nil
			},
		},
		{
			Name:   "audit_input_plan",
			NewReq: func() Request { return &AuditInputPlanReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*AuditInputPlanReq)
				var plan compiledInputPlan
				if err := json.Unmarshal(r.Plan, &plan); err != nil {
					return nil, fmt.Errorf("plan 无法解析: %w", err)
				}
				if err := validateCompiledInput(&CompileInputReq{
					Meta: r.Meta, SourceKind: r.SourceKind, SourceText: r.SourceText, SourceHash: r.SourceHash,
				}, &plan); err != nil {
					return nil, fmt.Errorf("待审计计划未通过来源校验: %w", err)
				}
				var out struct {
					Accepted bool `json:"accepted"`
					Issues   []struct {
						Severity    string `json:"severity"`
						Code        string `json:"code"`
						Explanation string `json:"explanation"`
					} `json:"issues"`
				}
				if err := ParseAIJSONStrict(content, &out, "accepted", "issues"); err != nil {
					return nil, err
				}
				if len(out.Issues) > 50 {
					return nil, fmt.Errorf("审计问题数量超限")
				}
				blocking := false
				issues := make([]map[string]string, 0, len(out.Issues))
				for _, issue := range out.Issues {
					if issue.Severity != "warning" && issue.Severity != "blocking" {
						return nil, fmt.Errorf("审计问题 severity 非法")
					}
					if strings.TrimSpace(issue.Code) == "" || strings.TrimSpace(issue.Explanation) == "" {
						return nil, fmt.Errorf("审计问题缺少 code 或 explanation")
					}
					if issue.Severity == "blocking" {
						blocking = true
					}
					issues = append(issues, map[string]string{
						"severity":    issue.Severity,
						"code":        truncRunes(issue.Code, 60),
						"explanation": truncRunes(issue.Explanation, 300),
					})
				}
				return map[string]any{"accepted": out.Accepted && !blocking, "issues": issues}, nil
			},
		},
		{
			Name:   "repair_input_plan",
			NewReq: func() Request { return &RepairInputPlanReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*RepairInputPlanReq)
				var out compiledInputPlan
				if err := ParseAIJSONStrict(content, &out,
					"schemaVersion", "sourceHash", "sourceKind", "entityName", "exclusions", "confidence", "explanation"); err != nil {
					return nil, err
				}
				var probe map[string]json.RawMessage
				raw, _ := extractJSONObject(content)
				if err := json.Unmarshal([]byte(raw), &probe); err != nil {
					return nil, err
				}
				keys := []string{"records"}
				if r.SourceKind == "json" {
					keys = []string{"recordPointer", "recordMode", "fields"}
				}
				for _, key := range keys {
					if _, ok := probe[key]; !ok {
						return nil, fmt.Errorf("AI 输出缺少必填字段 %q", key)
					}
				}
				if err := validateCompiledInput(&CompileInputReq{Meta: r.Meta, SourceKind: r.SourceKind, SourceText: r.SourceText, SourceHash: r.SourceHash}, &out); err != nil {
					return nil, err
				}
				return out, nil
			},
		},
		{
			Name:   "agent_step",
			NewReq: func() Request { return &AgentStepReq{} },
			Post: func(req Request, content string) (any, error) {
				out, err := validateAgentStepOutput(req.(*AgentStepReq), content)
				if err != nil {
					return nil, err
				}
				return out, nil
			},
		},
		{
			Name:   "agent_visual_ground",
			NewReq: func() Request { return &AgentVisualGroundReq{} },
			Messages: func(req Request, system, user string) ([]ChatMessage, error) {
				visual := req.(*AgentVisualGroundReq)
				return []ChatMessage{
					{Role: "system", Content: system},
					{Role: "user", ContentParts: []ChatContentPart{
						{Type: "text", Text: user},
						{Type: "image_url", ImageURL: &ChatImageURL{URL: visual.ImageDataURL, Detail: "low"}},
					}},
				}, nil
			},
			Post: func(req Request, content string) (any, error) {
				return validateAgentVisualGroundOutput(req.(*AgentVisualGroundReq), content)
			},
		},
		{
			Name:   "audit_agent_checkpoint",
			NewReq: func() Request { return &AuditAgentCheckpointReq{} },
			Post: func(_ Request, content string) (any, error) {
				var out struct {
					Consistent  bool     `json:"consistent"`
					Decision    string   `json:"decision"`
					Issues      []string `json:"issues"`
					Explanation string   `json:"explanation"`
				}
				if err := ParseAIJSONStrict(content, &out, "consistent", "decision", "issues", "explanation"); err != nil {
					return nil, err
				}
				if !inStrings([]string{"continue", "replan", "need-user", "blocked"}, out.Decision) {
					return nil, fmt.Errorf("审核 decision 非法")
				}
				if !out.Consistent && out.Decision == "continue" {
					return nil, fmt.Errorf("不一致的审核不得要求 continue")
				}
				if strings.TrimSpace(out.Explanation) == "" {
					return nil, fmt.Errorf("审核 explanation 为空")
				}
				if len(out.Issues) > 20 {
					return nil, fmt.Errorf("审核 issues 数量超限")
				}
				for _, issue := range out.Issues {
					if strings.TrimSpace(issue) == "" || len([]rune(issue)) > 300 {
						return nil, fmt.Errorf("审核 issue 为空或超长")
					}
				}
				return out, nil
			},
		},
		{
			Name:   "assess_page",
			NewReq: func() Request { return &AssessPageReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*AssessPageReq)
				var out struct {
					Enterable    bool    `json:"enterable"`
					OpenSelector *string `json:"openSelector"`
				}
				if err := ParseAIJSONStrict(content, &out, "enterable"); err != nil {
					return nil, err
				}
				if out.OpenSelector != nil && !buttonExists(r.Buttons, *out.OpenSelector) {
					logDropped("assess_page", req, "openSelector", []string{*out.OpenSelector})
					out.OpenSelector = nil
				}
				return map[string]any{"enterable": out.Enterable, "openSelector": out.OpenSelector}, nil
			},
		},
		{
			Name:   "analyze_form",
			NewReq: func() Request { return &AnalyzeFormReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*AnalyzeFormReq)
				var out struct {
					SubmitSelector  *string `json:"submitSelector"`
					OpenSelector    *string `json:"openSelector"`
					AdvanceSelector *string `json:"advanceSelector"`
				}
				if err := ParseAIJSONStrict(content, &out, "submitSelector"); err != nil {
					return nil, err
				}
				if out.SubmitSelector != nil && !buttonExists(r.Buttons, *out.SubmitSelector) {
					logDropped("analyze_form", req, "submitSelector", []string{*out.SubmitSelector})
					out.SubmitSelector = nil
				}
				all := append(append([]ButtonInfo{}, r.Buttons...), r.OuterButtons...)
				if out.OpenSelector != nil && !buttonExists(all, *out.OpenSelector) {
					logDropped("analyze_form", req, "openSelector", []string{*out.OpenSelector})
					out.OpenSelector = nil
				}
				// 分步表单的"下一步"按钮:必须真实存在,且不能和提交按钮是同一个(自相矛盾按幻觉丢弃)
				if out.AdvanceSelector != nil &&
					(!buttonExists(r.Buttons, *out.AdvanceSelector) ||
						(out.SubmitSelector != nil && *out.AdvanceSelector == *out.SubmitSelector)) {
					logDropped("analyze_form", req, "advanceSelector", []string{*out.AdvanceSelector})
					out.AdvanceSelector = nil
				}
				return map[string]any{
					"submitSelector":  out.SubmitSelector,
					"openSelector":    out.OpenSelector,
					"advanceSelector": out.AdvanceSelector,
				}, nil
			},
		},
		{
			Name:   "pick_open_button",
			NewReq: func() Request { return &PickOpenButtonReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*PickOpenButtonReq)
				var out struct {
					Selector *string `json:"selector"`
				}
				if err := ParseAIJSONStrict(content, &out, "selector"); err != nil {
					return nil, err
				}
				if out.Selector != nil && !buttonExists(r.Buttons, *out.Selector) {
					logDropped("pick_open_button", req, "selector", []string{*out.Selector})
					out.Selector = nil
				}
				return map[string]any{"selector": out.Selector}, nil
			},
		},
		{
			Name:   "pick_form",
			NewReq: func() Request { return &PickFormReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*PickFormReq)
				var out struct {
					FormID *string `json:"formId"`
				}
				if err := ParseAIJSONStrict(content, &out, "formId"); err != nil {
					return nil, err
				}
				if out.FormID != nil {
					found := false
					for _, f := range r.Forms {
						if f.ID == *out.FormID {
							found = true
							break
						}
					}
					if !found {
						logDropped("pick_form", req, "formId", []string{*out.FormID})
						out.FormID = nil
					}
				}
				return map[string]any{"formId": out.FormID}, nil
			},
		},
		{
			Name:   "match_columns",
			NewReq: func() Request { return &MatchColumnsReq{} },
			// 方案费:计费键服务端派生(同方案永不重复扣);仅 initial 语境收费,
			// dynamic/repair 定价为 0(自愈与动态补充由月费覆盖)
			BillingGroup: func(userID int64, req Request) string {
				return matchColumnsBillingGroup(userID, req.(*MatchColumnsReq))
			},
			PriceFor: func(req Request, configured int64) int64 {
				r := req.(*MatchColumnsReq)
				if r.Context == "" || r.Context == MatchContextInitial {
					return configured
				}
				return 0
			},
			// 幻觉过滤后一列都没对上 = 没有建立起可用的方案:免单,也不占用计费组
			// (之后同表单同资料再次匹配成功时才收方案费)
			PriceAfter: func(req Request, result any, price int64) int64 {
				m, ok := result.(map[string]any)
				if !ok {
					return price
				}
				items, ok := m["mapping"].([]mappedColumn)
				if !ok {
					return price
				}
				for _, it := range items {
					if it.Column != nil {
						return price
					}
				}
				return 0
			},
			Post: func(req Request, content string) (any, error) {
				r := req.(*MatchColumnsReq)
				var out struct {
					Mapping []struct {
						FieldIndex int     `json:"fieldIndex"`
						Column     *string `json:"column"`
					} `json:"mapping"`
				}
				if err := ParseAIJSONStrict(content, &out, "mapping"); err != nil {
					return nil, err
				}
				if out.Mapping == nil {
					return nil, fmt.Errorf("AI 输出缺少必填字段 \"mapping\"")
				}
				validIdx := map[int]bool{}
				for _, f := range r.Fields {
					validIdx[f.Index] = true
				}
				filtered := []mappedColumn{}
				var badIdx, badCol []string
				for _, m := range out.Mapping {
					if !validIdx[m.FieldIndex] {
						badIdx = append(badIdx, strconv.Itoa(m.FieldIndex))
						continue
					}
					if m.Column != nil && !headerExists(r.Headers, *m.Column) {
						badCol = append(badCol, *m.Column)
						continue
					}
					filtered = append(filtered, mappedColumn{m.FieldIndex, m.Column})
				}
				logDropped("match_columns", req, "fieldIndex", badIdx)
				logDropped("match_columns", req, "column", badCol)
				return map[string]any{"mapping": filtered}, nil
			},
		},
		{
			Name:   "suggest_profile",
			NewReq: func() Request { return &SuggestProfileReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*SuggestProfileReq)
				var out struct {
					ProfileID *string `json:"profileId"`
				}
				if err := ParseAIJSONStrict(content, &out, "profileId"); err != nil {
					return nil, err
				}
				if out.ProfileID != nil {
					found := false
					for _, p := range r.Profiles {
						if p.ID == *out.ProfileID {
							found = true
							break
						}
					}
					if !found {
						logDropped("suggest_profile", req, "profileId", []string{*out.ProfileID})
						out.ProfileID = nil
					}
				}
				return map[string]any{"profileId": out.ProfileID}, nil
			},
		},
		{
			Name:   "detect_grouping",
			NewReq: func() Request { return &DetectGroupingReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*DetectGroupingReq)
				var out struct {
					ParentColumns []string `json:"parentColumns"`
					LocateColumn  *string  `json:"locateColumn"`
				}
				if err := ParseAIJSONStrict(content, &out, "parentColumns"); err != nil {
					return nil, err
				}
				valid := []string{}
				var bad []string
				for _, ccol := range out.ParentColumns {
					if headerExists(r.Headers, ccol) {
						valid = append(valid, ccol)
					} else {
						bad = append(bad, ccol)
					}
				}
				logDropped("detect_grouping", req, "parentColumn", bad)
				if len(valid) == 0 {
					return map[string]any{"parentColumns": nil, "locateColumn": nil}, nil
				}
				locate := valid[0]
				if out.LocateColumn != nil && headerExists(valid, *out.LocateColumn) {
					locate = *out.LocateColumn
				} else if out.LocateColumn != nil {
					logDropped("detect_grouping", req, "locateColumn", []string{*out.LocateColumn})
				}
				return map[string]any{"parentColumns": valid, "locateColumn": locate}, nil
			},
		},
		{
			Name:   "detect_identity",
			NewReq: func() Request { return &DetectIdentityReq{} },
			// 模型只被允许指认一个**已存在的**字段名。它不给"一条还是多条"的结论:
			// 那个判定在插件侧由平台事实算出(见 DetectIdentityReq 的说明)。
			Post: func(req Request, content string) (any, error) {
				r := req.(*DetectIdentityReq)
				var out struct {
					IdentityColumn *string `json:"identityColumn"`
					Reason         string  `json:"reason"`
				}
				if err := ParseAIJSONStrict(content, &out, "identityColumn"); err != nil {
					return nil, err
				}
				if out.IdentityColumn != nil && !headerExists(r.Headers, *out.IdentityColumn) {
					logDropped("detect_identity", req, "identityColumn", []string{*out.IdentityColumn})
					out.IdentityColumn = nil
				}
				// reason 会原样显示给用户(拍板卡上的一句话),所以要截断,不让模型灌长文
				return map[string]any{
					"identityColumn": out.IdentityColumn,
					"reason":         truncRunes(strings.TrimSpace(out.Reason), 60),
				}, nil
			},
		},
		{
			Name:   "extract_reveal_chain",
			NewReq: func() Request { return &ExtractRevealChainReq{} },
			// 模型只能从送来的步骤里挑(下标合法、去重、保持顺序);summary/reason 会原样显示给用户,截断。
			// "产生了目标字段的步骤不许丢"这条证伪在插件侧做(它手里有完整的步骤与字段),这里只管形态合法。
			Post: func(req Request, content string) (any, error) {
				r := req.(*ExtractRevealChainReq)
				var out struct {
					Keep        []int  `json:"keep"`
					Summary     string `json:"summary"`
					Automatable *bool  `json:"automatable"`
					Reason      string `json:"reason"`
				}
				if err := ParseAIJSONStrict(content, &out, "keep", "automatable"); err != nil {
					return nil, err
				}
				seen := map[int]bool{}
				keep := make([]int, 0, len(out.Keep))
				var bad []string
				for _, k := range out.Keep {
					if k < 0 || k >= len(r.Steps) {
						bad = append(bad, strconv.Itoa(k))
						continue
					}
					if seen[k] {
						continue
					}
					seen[k] = true
					keep = append(keep, k)
				}
				logDropped("extract_reveal_chain", req, "keep", bad)
				automatable := out.Automatable == nil || *out.Automatable
				// 链里有"输入"这一步就不可能自动照做(插件不记输入内容):模型说能也不算
				for _, k := range keep {
					if r.Steps[k].Kind == "input" {
						automatable = false
						if strings.TrimSpace(out.Reason) == "" {
							out.Reason = "其中一步要输入内容"
						}
					}
				}
				return map[string]any{
					"keep":        keep,
					"summary":     truncRunes(strings.TrimSpace(out.Summary), 80),
					"automatable": automatable,
					"reason":      truncRunes(strings.TrimSpace(out.Reason), 120),
				}, nil
			},
		},
		{
			Name:   "generate_rule",
			NewReq: func() Request { return &GenerateRuleReq{} },
			Post: func(req Request, content string) (any, error) {
				code, err := ExtractFunction(content)
				if err != nil {
					return nil, err
				}
				return map[string]any{"code": code}, nil
			},
		},
		{
			Name:   "generate_field",
			NewReq: func() Request { return &GenerateFieldReq{} },
			// 1 分/成功格:计费键 = 行内容指纹 + 字段,同格重新生成经防重自动免单
			BillingGroup: func(userID int64, req Request) string {
				return generateFieldBillingGroup(userID, req.(*GenerateFieldReq))
			},
			Post: func(req Request, content string) (any, error) {
				// 模型常把纯文本也用 ```包起来:不剥围栏,反引号会原样填进业务系统的表单
				v := stripFence(content)
				if v == "" {
					return nil, fmt.Errorf("生成内容为空")
				}
				// 表单单字段不该有几千字:超长视为无效输出(422 不扣分),
				// 防提示词注入借 maxTokens 顶格产出畸形长文灌进业务系统
				if n := len([]rune(v)); n > 2000 {
					return nil, fmt.Errorf("生成内容超长(%d 字符)", n)
				}
				return map[string]any{"content": v}, nil
			},
		},
		{
			Name:   "explain_failure",
			NewReq: func() Request { return &ExplainFailureReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*ExplainFailureReq)
				var out struct {
					Explain   string `json:"explain"`
					Fix       string `json:"fix"`
					FixFields []int  `json:"fixFields"`
				}
				if err := ParseAIJSONStrict(content, &out, "explain"); err != nil {
					return nil, err
				}
				out.Explain = strings.TrimSpace(out.Explain)
				if out.Explain == "" {
					return nil, fmt.Errorf("根因分析结果为空")
				}
				valid := map[int]bool{}
				for _, p := range r.FillPlan {
					valid[p.Index] = true
				}
				fixFields := []int{}
				var bad []string
				for _, i := range out.FixFields {
					if valid[i] {
						fixFields = append(fixFields, i)
					} else {
						bad = append(bad, strconv.Itoa(i))
					}
				}
				logDropped("explain_failure", req, "fixField", bad)
				return map[string]any{
					"explain": out.Explain, "fix": strings.TrimSpace(out.Fix), "fixFields": fixFields,
				}, nil
			},
		},
		{
			Name:   "classify_failure",
			NewReq: func() Request { return &ClassifyFailureReq{} },
			Post: func(req Request, content string) (any, error) {
				var out struct {
					Retryable bool   `json:"retryable"`
					Brief     string `json:"brief"`
					Systemic  bool   `json:"systemic"`
				}
				if err := ParseAIJSONStrict(content, &out, "retryable", "systemic", "brief"); err != nil {
					return nil, err
				}
				return map[string]any{
					"retryable": out.Retryable, "brief": truncRunes(out.Brief, 30), "systemic": out.Systemic,
				}, nil
			},
		},
		{
			Name:   "parse_command",
			NewReq: func() Request { return &ParseCommandReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*ParseCommandReq)
				var out struct {
					Changes []MappingItem `json:"changes"`
					Order   *struct {
						Column *string `json:"column"`
						Dir    string  `json:"dir"`
					} `json:"order"`
					Reply string `json:"reply"`
				}
				if err := ParseAIJSONStrict(content, &out, "changes", "reply"); err != nil {
					return nil, err
				}
				validIdx := map[int]bool{}
				for _, f := range r.Fields {
					validIdx[f.Index] = true
				}
				changes := []MappingItem{}
				var bad []string
				for _, ch := range out.Changes {
					if !validIdx[ch.FieldIndex] {
						bad = append(bad, fmt.Sprintf("字段%d(不存在)", ch.FieldIndex))
						continue
					}
					switch ch.Mode {
					case "off":
					case "column":
						if ch.Column == "" || !headerExists(r.Headers, ch.Column) {
							bad = append(bad, fmt.Sprintf("字段%d(列%q不存在)", ch.FieldIndex, ch.Column))
							continue
						}
					case "ai", "rule":
						if strings.TrimSpace(ch.AIPrompt) == "" {
							bad = append(bad, fmt.Sprintf("字段%d(%s 无提示词)", ch.FieldIndex, ch.Mode))
							continue
						}
					default:
						bad = append(bad, fmt.Sprintf("字段%d(mode=%q 非法)", ch.FieldIndex, ch.Mode))
						continue
					}
					changes = append(changes, ch)
				}
				logDropped("parse_command", req, "change", bad)
				var order any
				if out.Order != nil && (out.Order.Dir == "asc" || out.Order.Dir == "desc") {
					if out.Order.Column == nil {
						order = map[string]any{"column": nil, "dir": out.Order.Dir}
					} else if headerExists(r.Headers, *out.Order.Column) {
						order = map[string]any{"column": *out.Order.Column, "dir": out.Order.Dir}
					} else {
						logDropped("parse_command", req, "orderColumn", []string{*out.Order.Column})
					}
				}
				return map[string]any{
					"changes": changes, "order": order, "reply": truncRunes(out.Reply, 200),
				}, nil
			},
		},
	}
}
