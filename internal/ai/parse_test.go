// ai-form-backend - AGPL-3.0
package ai

import (
	"strings"
	"testing"
)

// 围栏剥离不得改动正文:全局替换会把 JSON 字符串里的反引号一起删掉,
// 用户拿到的是被服务端悄悄改过的内容(改完还能解析成功,最难发现的一类静默失败)。
func TestParseKeepsBackticksInsideJSON(t *testing.T) {
	var out struct {
		Reply string `json:"reply"`
	}
	const want = "改成 ```js 里的写法"
	if err := ParseAIJSON(`{"reply":"改成 `+"```js"+` 里的写法"}`, &out); err != nil {
		t.Fatalf("应能解析: %v", err)
	}
	if out.Reply != want {
		t.Fatalf("正文里的围栏标记不得被删,期望 %q,实际 %q", want, out.Reply)
	}
}

// 常规形态仍要兼容:围栏包裹、围栏前有废话、裸 JSON
func TestParseFencedForms(t *testing.T) {
	cases := []string{
		"```json\n{\"a\":1}\n```",
		"好的,结果如下:\n```json\n{\"a\":1}\n```\n希望有帮助",
		"{\"a\":1}",
		"  ```\n{\"a\":1}\n```  ",
	}
	for _, c := range cases {
		var out struct {
			A int `json:"a"`
		}
		if err := ParseAIJSON(c, &out); err != nil || out.A != 1 {
			t.Fatalf("形态 %q 应解析出 a=1,实际 %d err=%v", c, out.A, err)
		}
	}
}

// generate_rule:围栏与前置说明都要剥干净,结尾的 ``` 绝不能留在代码里
// (插件把它当 JS 执行,尾部围栏 = 语法错误)
func TestExtractFunctionStripsFence(t *testing.T) {
	cases := []string{
		"```js\nfunction transform(row) { return row['a']; }\n```",
		"这是规则:\n```javascript\nfunction transform(row) { return row['a']; }\n```",
		"function transform(row) { return row['a']; }",
	}
	for _, c := range cases {
		code, err := ExtractFunction(c)
		if err != nil {
			t.Fatalf("形态 %q 应提取成功: %v", c, err)
		}
		if strings.Contains(code, "```") {
			t.Fatalf("提取结果不得残留围栏: %q", code)
		}
		if code != "function transform(row) { return row['a']; }" {
			t.Fatalf("提取结果不对: %q", code)
		}
	}
}

// generate_rule 输出的最低限度校验:插件 execRule 要求 `function transform`,
// 括号不配平(输出被截断)与超长文本都要在服务端拦下,不能等插件执行时才炸
func TestExtractFunctionValidates(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"缺 transform 签名", "function convert(row) { return 1; }", "transform"},
		{"括号不配平", "function transform(row) { if (row['a']) { return '1';", "配平"},
		{"没有函数", "抱歉,我无法生成", "未返回函数代码"},
		{"超长", "function transform(row) {" + strings.Repeat("/*x*/", 3000) + "return '';}", "超长"},
	}
	for _, c := range cases {
		if _, err := ExtractFunction(c.text); err == nil {
			t.Fatalf("%s 应报错,实际通过", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s 的错误应说明原因(含 %q),实际: %v", c.name, c.want, err)
		}
	}
}

// 字符串与注释里的花括号不参与配平判定,否则正常规则会被误杀
func TestExtractFunctionBalanceIgnoresStringsAndComments(t *testing.T) {
	cases := []string{
		`function transform(row) { return "{" + row['a'] + "}"; }`,
		"function transform(row) {\n  // 去掉多余的 } 符号\n  return row['a'].replace('}', '');\n}",
		"function transform(row) {\n  /* 形如 {x} 的模板 */\n  return `{${row['a']}}`;\n}",
	}
	for _, c := range cases {
		if _, err := ExtractFunction(c); err != nil {
			t.Fatalf("字符串/注释里的花括号不该算进配平(%q): %v", c, err)
		}
	}
}
