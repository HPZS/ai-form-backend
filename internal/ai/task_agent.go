package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type TaskToolParameter struct {
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}
type TaskToolDescription struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Phase       string                       `json:"phase"`
	Effect      string                       `json:"effect"`
	Parameters  map[string]TaskToolParameter `json:"parameters"`
}
type TaskFieldSummary struct {
	Index    int      `json:"index"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Visible  bool     `json:"visible"`
	Source   *string  `json:"source"`
	Status   string   `json:"status"`
	NodeIDs  []string `json:"nodeIds"`
}
type TaskAgentReq struct {
	Meta
	SchemaVersion  string             `json:"schemaVersion"`
	RunID          string             `json:"runId"`
	SnapshotID     string             `json:"snapshotId"`
	ContextDigest  string             `json:"contextDigest"`
	RowIndex       int                `json:"rowIndex"`
	CallIndex      int                `json:"callIndex"`
	RemainingCalls int                `json:"remainingCalls"`
	Intent         string             `json:"intent"`
	SaveMode       string             `json:"saveMode"`
	Fields         []TaskFieldSummary `json:"fields"`
	Sources        []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"sources"`
	Projection       string   `json:"projection"`
	ProjectedNodeIDs []string `json:"projectedNodeIds"`
	Regions          []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"regions"`
	Tools   []TaskToolDescription `json:"tools"`
	History []json.RawMessage     `json:"history"`
}

var taskCallID = regexp.MustCompile(`^[\w:.-]{1,160}$`)

func (r *TaskAgentReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if r.SchemaVersion != "v1" || r.RunID == "" || r.SnapshotID == "" || r.ContextDigest == "" || r.TaskID == "" {
		return fmt.Errorf("任务上下文或版本无效")
	}
	if r.RowIndex < 0 || r.CallIndex < 1 || r.CallIndex > 1000 || r.RemainingCalls < 0 || r.RemainingCalls > 1000 {
		return fmt.Errorf("任务预算无效")
	}
	if !inStrings([]string{"automatic", "manual"}, r.SaveMode) {
		return fmt.Errorf("保存模式无效")
	}
	if len(r.Projection) > 192*1024 || len(r.Fields) > 1000 || len(r.History) > 24 || len(r.Tools) == 0 || len(r.Tools) > 40 {
		return fmt.Errorf("任务上下文超限")
	}
	seen := map[string]bool{}
	for _, tool := range r.Tools {
		if !taskCallID.MatchString(tool.Name) || seen[tool.Name] || len(tool.Parameters) > 16 || !inStrings([]string{"read", "write", "upload", "commit"}, tool.Effect) {
			return fmt.Errorf("工具定义无效")
		}
		seen[tool.Name] = true
		for _, parameter := range tool.Parameters {
			if !inStrings([]string{"string", "number", "boolean", "strings", "numbers"}, parameter.Type) {
				return fmt.Errorf("工具参数类型无效")
			}
		}
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > 512*1024 {
		return fmt.Errorf("任务输入超限")
	}
	return nil
}

type TaskToolCall struct {
	CallID    string                     `json:"callId"`
	Name      string                     `json:"name"`
	Arguments map[string]json.RawMessage `json:"arguments"`
}
type TaskQuestion struct {
	Kind         string `json:"kind"`
	Text         string `json:"text"`
	FieldIndexes []int  `json:"fieldIndexes,omitempty"`
}
type TaskAgentOutput struct {
	SchemaVersion string        `json:"schemaVersion"`
	RunID         string        `json:"runId"`
	SnapshotID    string        `json:"snapshotId"`
	ContextDigest string        `json:"contextDigest"`
	Status        string        `json:"status"`
	Explanation   string        `json:"explanation"`
	Tool          *TaskToolCall `json:"tool,omitempty"`
	Question      *TaskQuestion `json:"question,omitempty"`
}

func validateTaskAgentOutput(r *TaskAgentReq, content string) (TaskAgentOutput, error) {
	var out TaskAgentOutput
	raw, err := extractJSONObject(content)
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("任务规划 JSON 无效: %w", err)
	}
	if out.SchemaVersion != "v1" || out.RunID != r.RunID || out.SnapshotID != r.SnapshotID || out.ContextDigest != r.ContextDigest {
		return out, fmt.Errorf("任务规划上下文不一致")
	}
	if strings.TrimSpace(out.Explanation) == "" || len([]rune(out.Explanation)) > 400 {
		return out, fmt.Errorf("任务规划说明无效")
	}
	if out.Status != "tool" && out.Tool != nil {
		return out, fmt.Errorf("非工具状态夹带动作")
	}
	if out.Status != "need-user" && out.Question != nil {
		return out, fmt.Errorf("非求助状态夹带问题")
	}
	switch out.Status {
	case "tool":
		if out.Tool == nil || !taskCallID.MatchString(out.Tool.CallID) || out.Tool.Arguments == nil {
			return out, fmt.Errorf("缺少合法工具调用")
		}
		var definition *TaskToolDescription
		for i := range r.Tools {
			if r.Tools[i].Name == out.Tool.Name {
				definition = &r.Tools[i]
				break
			}
		}
		if definition == nil {
			return out, fmt.Errorf("工具未开放")
		}
		for name := range out.Tool.Arguments {
			if _, ok := definition.Parameters[name]; !ok {
				return out, fmt.Errorf("工具包含未知参数")
			}
		}
		for name, parameter := range definition.Parameters {
			value, exists := out.Tool.Arguments[name]
			if !exists {
				if parameter.Required {
					return out, fmt.Errorf("工具缺少必填参数")
				}
				continue
			}
			if string(value) == "null" {
				return out, fmt.Errorf("工具参数不能为空")
			}
			var target any
			switch parameter.Type {
			case "string":
				target = new(string)
			case "number":
				target = new(float64)
			case "boolean":
				target = new(bool)
			case "strings":
				target = new([]string)
			case "numbers":
				target = new([]int)
			}
			if err := json.Unmarshal(value, target); err != nil || len(value) > 16000 {
				return out, fmt.Errorf("工具参数类型或长度无效")
			}
		}
	case "need-user":
		q := out.Question
		if q == nil || strings.TrimSpace(q.Text) == "" || len([]rune(q.Text)) > 600 || !inStrings([]string{"missing-data", "ambiguity", "permission", "page-context", "capability", "unknown-submit"}, q.Kind) {
			return out, fmt.Errorf("人工问题无效")
		}
		for _, index := range q.FieldIndexes {
			found := false
			for _, field := range r.Fields {
				if field.Index == index {
					found = true
				}
			}
			if !found {
				return out, fmt.Errorf("人工问题引用未知字段")
			}
		}
	case "done", "blocked":
	default:
		return out, fmt.Errorf("任务规划状态无效")
	}
	return out, nil
}
