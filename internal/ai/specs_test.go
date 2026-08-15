// ai-form-backend - AGPL-3.0
package ai

import (
	"strings"
	"testing"
)

// 必填键缺失必须报错,不能靠零值直通。
// 模型返回 {} 时,classify_failure 会得到 retryable=false —— 被当成"这条数据不值得补录"
// 的真实判断,用户永远不知道 AI 其实什么都没答;assess_page 的 enterable=false 同理。
func TestRequiredKeysMissing(t *testing.T) {
	specs := map[string]Spec{}
	for _, s := range Specs() {
		specs[s.Name] = s
	}
	cases := []struct {
		capability string
		req        Request
		content    string
		wantKey    string
	}{
		{"classify_failure", &ClassifyFailureReq{}, `{}`, "retryable"},
		{"classify_failure", &ClassifyFailureReq{}, `{"retryable":true,"brief":"x"}`, "systemic"},
		{"assess_page", &AssessPageReq{}, `{"openSelector":null}`, "enterable"},
		{"pick_open_button", &PickOpenButtonReq{}, `{}`, "selector"},
		{"pick_form", &PickFormReq{}, `{}`, "formId"},
		{"suggest_profile", &SuggestProfileReq{}, `{}`, "profileId"},
		{"detect_grouping", &DetectGroupingReq{}, `{}`, "parentColumns"},
		{"analyze_form", &AnalyzeFormReq{}, `{}`, "submitSelector"},
		{"match_columns", &MatchColumnsReq{}, `{}`, "mapping"},
		{"parse_command", &ParseCommandReq{}, `{"changes":[]}`, "reply"},
	}
	for _, c := range cases {
		sp, ok := specs[c.capability]
		if !ok {
			t.Fatalf("能力 %s 不存在", c.capability)
		}
		_, err := sp.Post(c.req, c.content)
		if err == nil {
			t.Fatalf("%s 收到 %s 应报错(缺 %s),实际通过", c.capability, c.content, c.wantKey)
		}
		if !strings.Contains(err.Error(), c.wantKey) {
			t.Fatalf("%s 的错误应点名缺失的 %s,实际: %v", c.capability, c.wantKey, err)
		}
	}
}

// 键齐全时正常放行(含合法的 null 值:没有对应按钮/不匹配任何表单都是真实答案)
func TestRequiredKeysPresentPasses(t *testing.T) {
	specs := map[string]Spec{}
	for _, s := range Specs() {
		specs[s.Name] = s
	}
	if _, err := specs["classify_failure"].Post(&ClassifyFailureReq{}, `{"retryable":false,"brief":"已存在","systemic":false}`); err != nil {
		t.Fatalf("键齐全应通过,实际 %v", err)
	}
	if _, err := specs["pick_form"].Post(&PickFormReq{}, `{"formId":null}`); err != nil {
		t.Fatalf("null 是合法答案,应通过,实际 %v", err)
	}
	if _, err := specs["assess_page"].Post(&AssessPageReq{}, `{"enterable":true,"openSelector":null}`); err != nil {
		t.Fatalf("键齐全应通过,实际 %v", err)
	}
}

// 能力登记一致性:Specs / CapabilityMetas / capMaxTokens 三处必须对齐。
// 漏登记 capMaxTokens 会静默落到默认值(match_columns 就曾因此在宽表单上必然截断),
// 这类"漏配即静默降级"的结构性弱点必须由测试堵死,不能靠新增能力时记得改五处。
func TestCapabilityRegistrationConsistency(t *testing.T) {
	metas := map[string]bool{}
	for _, m := range CapabilityMetas() {
		metas[m.Key] = true
	}
	specs := Specs()
	for _, s := range specs {
		if !metas[s.Name] {
			t.Errorf("能力 %s 缺少 CapabilityMetas 登记(管理台看不到)", s.Name)
		}
		if _, ok := capMaxTokens[s.Name]; !ok {
			t.Errorf("能力 %s 缺少 capMaxTokens 登记,会静默落到默认 %d,输出大时必被截断", s.Name, defaultMaxTokens)
		}
	}
	if len(metas) != len(specs) {
		t.Errorf("CapabilityMetas(%d) 与 Specs(%d) 数量不一致", len(metas), len(specs))
	}
	for name := range capMaxTokens {
		found := false
		for _, s := range specs {
			if s.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("capMaxTokens 登记了不存在的能力 %s", name)
		}
	}
}
