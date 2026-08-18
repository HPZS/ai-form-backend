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
	Context string `json:"context,omitempty"`
	// Required 识别期从页面上采到的必填标记(原生 required/aria-required/组件库标记)。
	// 几个同类字段抢同一列数据时,这是决定给谁的关键事实:2026-08-17 新华 xinhuamm 实测,
	// 主图 URL 被匹配给了非必填的「应用背景」,必填的「应用icon」留空,提交被页面直接拦下,
	// 随后的误判自愈又删掉了用户手工调好的整套映射。当时这个事实根本没送到服务端。
	//
	// 注意:网关是 DisallowUnknownFields 严格解码,插件多送一个字段就是整批 400
	// (守则 #3「契约无版本协商 + 严格解码」)。字段先加、插件后发,顺序不能反。
	Required bool `json:"required,omitempty"`
	// LabelGuessed 这个 label 是插件**从版面推断**出来的,不是页面上写着属于它的真名
	// (来自与同行字段共享的行标签,或从 context 借的)。
	// 2026-08-17 Shopee 商品发布页实测:「商品描述」那个富文本推断出的名字是「商品图片」,
	// 与真的图片字段撞名后被消歧成「商品图片 #2」,用户当成重复的图片位设成了「不填」,
	// 那一列数据整批没进表单。真名只留在 context 里——模型得知道"这个 label 信不得"。
	//
	// 同样受上面那条铁律约束:2026-08-17 插件先发了这个字段,服务端还不认识,
	// assess-page / analyze-form 双双 400,识别整条链路瘫痪(守则 #16)。
	LabelGuessed bool     `json:"labelGuessed,omitempty"`
	Options      []string `json:"options,omitempty"`
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

// DetectIdentityReq 读法有歧义时,请模型指认"哪一个字段是一条记录的身份"。
//
// 它**不判断**这份数据是一条还是多条——那个判定由插件用平台事实(每个字段有几个值、
// 整张表有几个位置)确定性地算出来。模型只提供一件本地拿不到的东西:字段名与值的**语义**
// (「商品名称」是身份,「商品图片」不是)。
//
// 为什么需要它:一份竖版商品卡里"一个商品 8 张图"与"8 个商品各 1 张图",在表格的格子上
// 是**同一个东西**,OOXML 里没有任何字节能分开(插件仓库守则 §2.1、#21)。人能一眼看出,
// 靠的正是「商品名称」这四个字的意思——那是可见文本层(L2a)的事实,不是猜测。
//
// 安全边界:模型答错会被插件的平台事实反证挡掉(指认的字段必须存在、不能是图片/公式类、
// 算出的结论必须与值个数自洽),而且结论最终仍要由用户在卡片上确认。
// **把这个能力整个删掉,插件退回"让用户二选一"——变慢,不会读错。**
type DetectIdentityReq struct {
	Meta
	Headers []string `json:"headers"`
	// ValueCounts 每个字段读出了几个值(平台事实,插件算好后送来供模型参考)
	ValueCounts map[string]int `json:"valueCounts"`
	// PositionCount 整张表有几个"位置"(横版=几行,竖版=几列)
	PositionCount int `json:"positionCount"`
	// SampleValues 每个字段的前几个值,让模型看得见内容的语义
	SampleValues map[string][]string `json:"sampleValues"`
}

func (r *DetectIdentityReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if err := checkHeaders(r.Headers); err != nil {
		return err
	}
	if len(r.ValueCounts) > 100 {
		return fmt.Errorf("valueCounts 数量超限(100)")
	}
	if len(r.SampleValues) > 100 {
		return fmt.Errorf("sampleValues 数量超限(100)")
	}
	for k, vs := range r.SampleValues {
		if err := capStr("列名", k, 200); err != nil {
			return err
		}
		if len(vs) > 3 {
			return fmt.Errorf("sampleValues[%s] 个数超限(3)", k)
		}
		for _, v := range vs {
			if err := capStr(k, v, 200); err != nil {
				return err
			}
		}
	}
	return nil
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
