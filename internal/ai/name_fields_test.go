// ai-form-backend - AGPL-3.0
package ai

import (
	"encoding/json"
	"testing"
)

func nameFieldsSpec(t *testing.T) Spec {
	t.Helper()
	for _, sp := range Specs() {
		if sp.Name == "name_fields" {
			return sp
		}
	}
	t.Fatal("Specs() 里没有 name_fields")
	return Spec{}
}

// 输出过滤:不存在的字段序号丢弃、空名字丢弃、超长名字截断到 30 字
func TestNameFieldsPost(t *testing.T) {
	sp := nameFieldsSpec(t)
	req := &NameFieldsReq{Fields: []NameFieldBrief{
		{Index: 3, Tag: "file", Type: "file", Label: "file", Context: "基本信息 | 商品图片"},
		{Index: 4, Tag: "input", Type: "text", Label: "Input", Context: "销售信息"},
	}}
	long := "这个名字实在太长了完全不像一个正常字段名会被截断到三十个字以内保留前缀部分即可"
	content := `{"names":[
		{"index":3,"label":"商品图片"},
		{"index":4,"label":"` + long + `"},
		{"index":99,"label":"幻觉字段"},
		{"index":3,"label":"  "}
	]}`
	out, err := sp.Post(req, content)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var m struct {
		Names []struct {
			Index int    `json:"index"`
			Label string `json:"label"`
		} `json:"names"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Names) != 2 {
		t.Fatalf("应保留 2 条(幻觉序号与空名字被丢弃),实际 %d: %s", len(m.Names), raw)
	}
	if m.Names[0].Index != 3 || m.Names[0].Label != "商品图片" {
		t.Fatalf("第一条不符: %+v", m.Names[0])
	}
	if got := len([]rune(m.Names[1].Label)); got != 30 {
		t.Fatalf("超长名字应截断到 30 字,实际 %d", got)
	}
}

// 入参校验:fields 空/超限/context 超长都应拒绝
func TestNameFieldsValidate(t *testing.T) {
	meta := Meta{RequestID: "11111111-1111-4111-8111-111111111111"}
	if err := (&NameFieldsReq{Meta: meta}).Validate(); err == nil {
		t.Fatal("fields 为空应报错")
	}
	tooMany := make([]NameFieldBrief, 101)
	if err := (&NameFieldsReq{Meta: meta, Fields: tooMany}).Validate(); err == nil {
		t.Fatal("fields 超过 100 应报错")
	}
	longCtx := make([]rune, 301)
	for i := range longCtx {
		longCtx[i] = '长'
	}
	bad := &NameFieldsReq{Meta: meta, Fields: []NameFieldBrief{{Index: 0, Context: string(longCtx)}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("context 超过 300 字应报错")
	}
	ok := &NameFieldsReq{Meta: meta, Fields: []NameFieldBrief{{Index: 0, Label: "Input", Context: "基本信息"}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法请求不应报错: %v", err)
	}
}
