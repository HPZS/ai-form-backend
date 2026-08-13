// ai-form-backend - AGPL-3.0
// 12 个能力的输出解析与确定性校验,镜像插件 utils/ai.ts 的逻辑:
// 丢弃模型幻觉出的不存在的 selector/列名/字段序号(插件端还有最后一道防线)。
package ai

import (
	"fmt"
	"strings"
)

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
				if err := ParseAIJSON(content, &out); err != nil {
					return nil, err
				}
				if out.OpenSelector != nil && !buttonExists(r.Buttons, *out.OpenSelector) {
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
					SubmitSelector *string `json:"submitSelector"`
					OpenSelector   *string `json:"openSelector"`
				}
				if err := ParseAIJSON(content, &out); err != nil {
					return nil, err
				}
				if out.SubmitSelector != nil && !buttonExists(r.Buttons, *out.SubmitSelector) {
					out.SubmitSelector = nil
				}
				all := append(append([]ButtonInfo{}, r.Buttons...), r.OuterButtons...)
				if out.OpenSelector != nil && !buttonExists(all, *out.OpenSelector) {
					out.OpenSelector = nil
				}
				return map[string]any{"submitSelector": out.SubmitSelector, "openSelector": out.OpenSelector}, nil
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
				if err := ParseAIJSON(content, &out); err != nil {
					return nil, err
				}
				if out.Selector != nil && !buttonExists(r.Buttons, *out.Selector) {
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
				if err := ParseAIJSON(content, &out); err != nil {
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
						out.FormID = nil
					}
				}
				return map[string]any{"formId": out.FormID}, nil
			},
		},
		{
			Name:   "match_columns",
			NewReq: func() Request { return &MatchColumnsReq{} },
			Post: func(req Request, content string) (any, error) {
				r := req.(*MatchColumnsReq)
				var out struct {
					Mapping []struct {
						FieldIndex int     `json:"fieldIndex"`
						Column     *string `json:"column"`
					} `json:"mapping"`
				}
				if err := ParseAIJSON(content, &out); err != nil {
					return nil, err
				}
				if out.Mapping == nil {
					return nil, fmt.Errorf("mapping 缺失")
				}
				validIdx := map[int]bool{}
				for _, f := range r.Fields {
					validIdx[f.Index] = true
				}
				type item struct {
					FieldIndex int     `json:"fieldIndex"`
					Column     *string `json:"column"`
				}
				filtered := []item{}
				for _, m := range out.Mapping {
					if !validIdx[m.FieldIndex] {
						continue
					}
					if m.Column != nil && !headerExists(r.Headers, *m.Column) {
						continue
					}
					filtered = append(filtered, item{m.FieldIndex, m.Column})
				}
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
				if err := ParseAIJSON(content, &out); err != nil {
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
				if err := ParseAIJSON(content, &out); err != nil {
					return nil, err
				}
				valid := []string{}
				for _, ccol := range out.ParentColumns {
					if headerExists(r.Headers, ccol) {
						valid = append(valid, ccol)
					}
				}
				if len(valid) == 0 {
					return map[string]any{"parentColumns": nil, "locateColumn": nil}, nil
				}
				locate := valid[0]
				if out.LocateColumn != nil && headerExists(valid, *out.LocateColumn) {
					locate = *out.LocateColumn
				}
				return map[string]any{"parentColumns": valid, "locateColumn": locate}, nil
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
			Post: func(req Request, content string) (any, error) {
				v := strings.TrimSpace(content)
				if v == "" {
					return nil, fmt.Errorf("生成内容为空")
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
				if err := ParseAIJSON(content, &out); err != nil {
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
				for _, i := range out.FixFields {
					if valid[i] {
						fixFields = append(fixFields, i)
					}
				}
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
				if err := ParseAIJSON(content, &out); err != nil {
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
				if err := ParseAIJSON(content, &out); err != nil {
					return nil, err
				}
				validIdx := map[int]bool{}
				for _, f := range r.Fields {
					validIdx[f.Index] = true
				}
				changes := []MappingItem{}
				for _, ch := range out.Changes {
					if !validIdx[ch.FieldIndex] {
						continue
					}
					switch ch.Mode {
					case "off":
					case "column":
						if ch.Column == "" || !headerExists(r.Headers, ch.Column) {
							continue
						}
					case "ai", "rule":
						if strings.TrimSpace(ch.AIPrompt) == "" {
							continue
						}
					default:
						continue
					}
					changes = append(changes, ch)
				}
				var order any
				if out.Order != nil && (out.Order.Dir == "asc" || out.Order.Dir == "desc") {
					if out.Order.Column == nil {
						order = map[string]any{"column": nil, "dir": out.Order.Dir}
					} else if headerExists(r.Headers, *out.Order.Column) {
						order = map[string]any{"column": *out.Order.Column, "dir": out.Order.Dir}
					}
				}
				return map[string]any{
					"changes": changes, "order": order, "reply": truncRunes(out.Reply, 200),
				}, nil
			},
		},
	}
}
