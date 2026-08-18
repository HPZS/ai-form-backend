// ai-form-backend - AGPL-3.0
package ai

import (
	"bytes"
	"log"
	"os"
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
		{"detect_identity", &DetectIdentityReq{}, `{}`, "identityColumn"},
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

// generate_field 是纯文本能力,模型常把内容用 ``` 包起来:
// 不剥围栏,反引号会原样被填进业务系统的表单
func TestGenerateFieldStripsFence(t *testing.T) {
	var sp Spec
	for _, s := range Specs() {
		if s.Name == "generate_field" {
			sp = s
		}
	}
	out, err := sp.Post(&GenerateFieldReq{}, "```\n这是一句总结\n```")
	if err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if got := out.(map[string]any)["content"]; got != "这是一句总结" {
		t.Fatalf("围栏应被剥掉,实际 %q", got)
	}
	// 正文里本来就有的反引号不能被动:那是模型要表达的内容
	out, err = sp.Post(&GenerateFieldReq{}, "用 `code` 表示")
	if err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if got := out.(map[string]any)["content"]; got != "用 `code` 表示" {
		t.Fatalf("正文反引号不该被删,实际 %q", got)
	}
}

// 幻觉过滤必须留痕:零日志时既监控不了各上游的幻觉率(request_id 可与 ai_requests 表的
// upstream/model 对齐),也分不清"模型答错"与"服务端过滤过严"
func TestHallucinationLogged(t *testing.T) {
	specs := map[string]Spec{}
	for _, s := range Specs() {
		specs[s.Name] = s
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	req := &MatchColumnsReq{
		Meta:    Meta{RequestID: "req-1"},
		Fields:  []FormFieldBrief{{Index: 0}},
		Headers: []string{"姓名"},
	}
	if _, err := specs["match_columns"].Post(req,
		`{"mapping":[{"fieldIndex":0,"column":"不存在的列"},{"fieldIndex":99,"column":"姓名"}]}`); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "[AI-HALLUC]") || !strings.Contains(out, "req-1") {
		t.Fatalf("幻觉过滤应留痕并带 request_id,实际日志: %s", out)
	}
	if !strings.Contains(out, "不存在的列") || !strings.Contains(out, "99") {
		t.Fatalf("日志应能看出丢了什么,实际: %s", out)
	}

	buf.Reset()
	assess := &AssessPageReq{Meta: Meta{RequestID: "req-2"}, Buttons: []ButtonInfo{{Selector: "#real"}}}
	if _, err := specs["assess_page"].Post(assess, `{"enterable":true,"openSelector":"#幻觉"}`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "#幻觉") {
		t.Fatalf("单值幻觉也要留痕,实际: %s", buf.String())
	}

	// 没有过滤发生时不该刷日志
	buf.Reset()
	if _, err := specs["assess_page"].Post(assess, `{"enterable":true,"openSelector":"#real"}`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "[AI-HALLUC]") {
		t.Fatalf("没有丢弃时不该打日志,实际: %s", buf.String())
	}
}

// detect_identity 是"模型补语义、判定仍归平台事实"的能力:它唯一被允许说的
// 就是一个**已存在的**字段名。指认了不存在的字段(幻觉)必须当没答,否则插件那边
// 会拿一个凭空的列名去算读法,把一份数据读错还看起来很有道理。
func TestDetectIdentityFiltersHallucination(t *testing.T) {
	var sp Spec
	for _, s := range Specs() {
		if s.Name == "detect_identity" {
			sp = s
		}
	}
	req := &DetectIdentityReq{Meta: Meta{RequestID: "req-ident"}, Headers: []string{"商品名称", "商品图片"}}

	out, err := sp.Post(req, `{"identityColumn":"商品名称","reason":"每条商品都该有名字"}`)
	if err != nil {
		t.Fatalf("正常答案应通过: %v", err)
	}
	m := out.(map[string]any)
	if got := m["identityColumn"]; got == nil || *(got.(*string)) != "商品名称" {
		t.Fatalf("应原样返回指认的字段,实际 %v", got)
	}

	// 幻觉字段名 → 当作没答(null),而不是把它传下去
	out, err = sp.Post(req, `{"identityColumn":"根本没有这一列","reason":"瞎猜的"}`)
	if err != nil {
		t.Fatalf("幻觉应被过滤而不是报错: %v", err)
	}
	if got := out.(map[string]any)["identityColumn"]; got != (*string)(nil) {
		t.Fatalf("不存在的字段必须被丢成 null,实际 %v", got)
	}

	// null 是合法答案:没有字段够格当身份时,老实说没有
	if _, err := sp.Post(req, `{"identityColumn":null,"reason":"都不像身份"}`); err != nil {
		t.Fatalf("null 是合法答案,应通过: %v", err)
	}

	// reason 会原样显示在用户的拍板卡上,必须截断,不让模型灌长文
	long := strings.Repeat("很", 200)
	out, err = sp.Post(req, `{"identityColumn":null,"reason":"`+long+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(out.(map[string]any)["reason"].(string))); n > 60 {
		t.Fatalf("reason 应截到 60 字以内,实际 %d", n)
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
