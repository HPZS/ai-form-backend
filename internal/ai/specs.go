// ai-form-backend - AGPL-3.0
// 12 个能力的输出解析与确定性校验,镜像插件 utils/ai.ts 的逻辑:
// 丢弃模型幻觉出的不存在的 selector/列名/字段序号(插件端还有最后一道防线)。
package ai

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

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
