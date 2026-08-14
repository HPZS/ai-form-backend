// ai-form-backend - AGPL-3.0
// 12 个 AI 能力的请求结构与输入上限校验。与插件 utils/ai.ts 的函数一一对应;
// 上限规格见技术方案 v3 §6.4(插件端先截断,服务端二次校验,超限直接 400)。
package ai

import (
	"fmt"

	"github.com/google/uuid"
)

// Meta 每个请求携带的公共元信息。
type Meta struct {
	RequestID      string `json:"requestId"`
	TaskID         string `json:"taskId,omitempty"`
	BillingGroupID string `json:"billingGroupId,omitempty"`
}

func (m *Meta) GetMeta() *Meta { return m }

func (m *Meta) validateMeta() error {
	if _, err := uuid.Parse(m.RequestID); err != nil {
		return fmt.Errorf("requestId 必须是 uuid")
	}
	return nil
}

type Request interface {
	GetMeta() *Meta
	Validate() error
}

// ===== 公共子结构 =====

type FormFieldBrief struct {
	Index       int      `json:"index"`
	Label       string   `json:"label"`
	Tag         string   `json:"tag"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Placeholder string   `json:"placeholder"`
	// Context 字段周边的页面文字线索(分组标题/相邻文本,"|"分隔,越靠前离控件越近)。
	// 自研组件库页面常提不出 label(Input/file),匹配全靠它判断字段用途——
	// 这是页面上真实存在的文字,不是编造的名字。
	Context string   `json:"context,omitempty"`
	Options []string `json:"options,omitempty"`
}

type ButtonInfo struct {
	Selector string `json:"selector"`
	Text     string `json:"text"`
}

// ===== 校验辅助 =====

func capStr(name, v string, max int) error {
	if len([]rune(v)) > max {
		return fmt.Errorf("%s 超长(上限 %d 字符)", name, max)
	}
	return nil
}

func checkFields(fields []FormFieldBrief, max int) error {
	if len(fields) > max {
		return fmt.Errorf("fields 数量超限(%d)", max)
	}
	for _, f := range fields {
		for n, v := range map[string]string{"label": f.Label, "name": f.Name, "placeholder": f.Placeholder} {
			if err := capStr(n, v, 200); err != nil {
				return err
			}
		}
		if err := capStr("context", f.Context, 300); err != nil {
			return err
		}
		if len(f.Options) > 50 {
			return fmt.Errorf("字段选项数量超限")
		}
		for _, o := range f.Options {
			if err := capStr("option", o, 200); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkButtons(name string, buttons []ButtonInfo, max int) error {
	if len(buttons) > max {
		return fmt.Errorf("%s 数量超限(%d)", name, max)
	}
	for _, b := range buttons {
		if err := capStr("selector", b.Selector, 500); err != nil {
			return err
		}
		if err := capStr("text", b.Text, 200); err != nil {
			return err
		}
	}
	return nil
}

func checkHeaders(headers []string) error {
	if len(headers) > 100 {
		return fmt.Errorf("headers 数量超限(100)")
	}
	for _, h := range headers {
		if err := capStr("header", h, 200); err != nil {
			return err
		}
	}
	return nil
}

func checkRow(name string, row map[string]string, cellMax int) error {
	if len(row) > 100 {
		return fmt.Errorf("%s 列数超限(100)", name)
	}
	for k, v := range row {
		if err := capStr("列名", k, 200); err != nil {
			return err
		}
		if err := capStr(k, v, cellMax); err != nil {
			return err
		}
	}
	return nil
}

func checkRows(name string, rows []map[string]string, maxRows, cellMax int) error {
	if len(rows) > maxRows {
		return fmt.Errorf("%s 行数超限(%d)", name, maxRows)
	}
	for _, r := range rows {
		if err := checkRow(name, r, cellMax); err != nil {
			return err
		}
	}
	return nil
}

// ===== 12 个能力的请求结构 =====

type AssessPageReq struct {
	Meta
	Headers []string         `json:"headers"`
	Fields  []FormFieldBrief `json:"fields"`
	Buttons []ButtonInfo     `json:"buttons"`
}

func (r *AssessPageReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := checkHeaders(r.Headers); err != nil {
		return err
	}
	if err := checkFields(r.Fields, 200); err != nil {
		return err
	}
	return checkButtons("buttons", r.Buttons, 100)
}

type AnalyzeFormReq struct {
	Meta
	Fields       []FormFieldBrief `json:"fields"`
	Buttons      []ButtonInfo     `json:"buttons"`
	OuterButtons []ButtonInfo     `json:"outerButtons"`
}

func (r *AnalyzeFormReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := checkFields(r.Fields, 200); err != nil {
		return err
	}
	if err := checkButtons("buttons", r.Buttons, 100); err != nil {
		return err
	}
	return checkButtons("outerButtons", r.OuterButtons, 100)
}

type PickOpenButtonReq struct {
	Meta
	Buttons  []ButtonInfo `json:"buttons"`
	DataHint string       `json:"dataHint"`
}

func (r *PickOpenButtonReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := capStr("dataHint", r.DataHint, 500); err != nil {
		return err
	}
	return checkButtons("buttons", r.Buttons, 100)
}

type PickFormEntry struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

type PickFormReq struct {
	Meta
	Headers  []string        `json:"headers"`
	FileName string          `json:"fileName"`
	Forms    []PickFormEntry `json:"forms"`
}

func (r *PickFormReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := checkHeaders(r.Headers); err != nil {
		return err
	}
	if len(r.Forms) > 50 {
		return fmt.Errorf("forms 数量超限(50)")
	}
	for _, f := range r.Forms {
		if err := capStr("form.name", f.Name, 200); err != nil {
			return err
		}
		if err := checkHeaders(f.Fields); err != nil {
			return err
		}
	}
	return capStr("fileName", r.FileName, 200)
}

// match_columns 的调用语境(方案文档 §6.3):只有 initial(初次建方案)收方案费;
// dynamic(录入中动态字段增量映射)与 repair(改版/证伪后自愈重建)免费。
const (
	MatchContextInitial = "initial"
	MatchContextDynamic = "dynamic"
	MatchContextRepair  = "repair"
)

type MatchColumnsReq struct {
	Meta
	Context   string            `json:"context,omitempty"` // initial(默认) | dynamic | repair
	Fields    []FormFieldBrief  `json:"fields"`
	Headers   []string          `json:"headers"`
	SampleRow map[string]string `json:"sampleRow"`
}

func (r *MatchColumnsReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	switch r.Context {
	case "", MatchContextInitial, MatchContextDynamic, MatchContextRepair:
	default:
		return fmt.Errorf("context 必须是 initial/dynamic/repair")
	}
	if err := checkFields(r.Fields, 200); err != nil {
		return err
	}
	if err := checkHeaders(r.Headers); err != nil {
		return err
	}
	return checkRow("sampleRow", r.SampleRow, 200)
}

type ProfileEntry struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Headers []string `json:"headers"`
}

type SuggestProfileReq struct {
	Meta
	Profiles  []ProfileEntry    `json:"profiles"`
	Headers   []string          `json:"headers"`
	SampleRow map[string]string `json:"sampleRow"`
}

func (r *SuggestProfileReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if len(r.Profiles) > 50 {
		return fmt.Errorf("profiles 数量超限(50)")
	}
	for _, p := range r.Profiles {
		if err := capStr("profile.name", p.Name, 200); err != nil {
			return err
		}
		if err := checkHeaders(p.Headers); err != nil {
			return err
		}
	}
	if err := checkHeaders(r.Headers); err != nil {
		return err
	}
	return checkRow("sampleRow", r.SampleRow, 200)
}

type DetectGroupingReq struct {
	Meta
	Headers        []string            `json:"headers"`
	SampleRows     []map[string]string `json:"sampleRows"`
	DistinctCounts map[string]int      `json:"distinctCounts"`
	TotalRows      int                 `json:"totalRows"`
}

func (r *DetectGroupingReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := checkHeaders(r.Headers); err != nil {
		return err
	}
	if len(r.DistinctCounts) > 100 {
		return fmt.Errorf("distinctCounts 数量超限")
	}
	return checkRows("sampleRows", r.SampleRows, 5, 200)
}

type GenerateRuleReq struct {
	Meta
	Field       FormFieldBrief      `json:"field"`
	Requirement string              `json:"requirement"`
	Headers     []string            `json:"headers"`
	SampleRows  []map[string]string `json:"sampleRows"`
}

func (r *GenerateRuleReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := checkFields([]FormFieldBrief{r.Field}, 1); err != nil {
		return err
	}
	if err := capStr("requirement", r.Requirement, 2000); err != nil {
		return err
	}
	if err := checkHeaders(r.Headers); err != nil {
		return err
	}
	return checkRows("sampleRows", r.SampleRows, 5, 200)
}

type GenerateFieldReq struct {
	Meta
	Field  FormFieldBrief    `json:"field"`
	Prompt string            `json:"prompt"`
	Row    map[string]string `json:"row"`
}

func (r *GenerateFieldReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := checkFields([]FormFieldBrief{r.Field}, 1); err != nil {
		return err
	}
	if err := capStr("prompt", r.Prompt, 2000); err != nil {
		return err
	}
	return checkRow("row", r.Row, 500)
}

type FillPlanItem struct {
	Index int    `json:"index"`
	Field string `json:"field"`
	How   string `json:"how"`
}

type ExplainFailureReq struct {
	Meta
	Row        map[string]string `json:"row"`
	FillPlan   []FillPlanItem    `json:"fillPlan"`
	BranchDesc []string          `json:"branchDesc"`
	EmptyDesc  string            `json:"emptyDesc"`
}

func (r *ExplainFailureReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := checkRow("row", r.Row, 500); err != nil {
		return err
	}
	if len(r.FillPlan) > 200 {
		return fmt.Errorf("fillPlan 数量超限")
	}
	for _, p := range r.FillPlan {
		if err := capStr("how", p.How, 500); err != nil {
			return err
		}
		if err := capStr("field", p.Field, 200); err != nil {
			return err
		}
	}
	if len(r.BranchDesc) > 50 {
		return fmt.Errorf("branchDesc 数量超限")
	}
	for _, b := range r.BranchDesc {
		if err := capStr("branchDesc", b, 500); err != nil {
			return err
		}
	}
	return capStr("emptyDesc", r.EmptyDesc, 1000)
}

type ClassifyFailureReq struct {
	Meta
	Error string `json:"error"`
}

func (r *ClassifyFailureReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	return capStr("error", r.Error, 1000)
}

type MappingItem struct {
	FieldIndex int    `json:"fieldIndex"`
	Mode       string `json:"mode"`
	Column     string `json:"column,omitempty"`
	AIPrompt   string `json:"aiPrompt,omitempty"`
}

type ParseCommandReq struct {
	Meta
	Instruction string           `json:"instruction"`
	Fields      []FormFieldBrief `json:"fields"`
	Headers     []string         `json:"headers"`
	Mapping     []MappingItem    `json:"mapping"`
}

func (r *ParseCommandReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := capStr("instruction", r.Instruction, 2000); err != nil {
		return err
	}
	if err := checkFields(r.Fields, 200); err != nil {
		return err
	}
	if err := checkHeaders(r.Headers); err != nil {
		return err
	}
	if len(r.Mapping) > 200 {
		return fmt.Errorf("mapping 数量超限")
	}
	for _, m := range r.Mapping {
		if err := capStr("aiPrompt", m.AIPrompt, 2000); err != nil {
			return err
		}
		if err := capStr("column", m.Column, 200); err != nil {
			return err
		}
	}
	return nil
}
