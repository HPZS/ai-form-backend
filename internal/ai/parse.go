// ai-form-backend - AGPL-3.0
// 模型输出解析:容忍 markdown 围栏与前后废话(移植自插件 utils/ai.ts 的 parseAiJson)。
package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// stripFence 只剥离"整段回复被围栏包住"这一种形态(开头是 ```lang,结尾配对的 ```)。
//
// 不做全局替换:模型正文里本来就可能出现反引号(如 parse_command 的回复"改成 ```js 里的写法"、
// generate_field 生成的说明文字),全局删掉的是 JSON 字符串的内容——解析照样成功,
// 用户拿到的却是被服务端悄悄改过的文本,属于最难发现的一类静默失败。
// 前面带说明文字的形态无需在此处理:JSON 走首尾花括号扫描,函数走 "function" 起点截取。
func stripFence(text string) string {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return s
	}
	body := s[nl+1:]
	if end := strings.LastIndex(body, "```"); end >= 0 {
		body = body[:end]
	}
	return strings.TrimSpace(body)
}

// extractJSONObject 从模型回复中截出首个完整 JSON 对象文本(容忍围栏与前后废话)。
func extractJSONObject(text string) (string, error) {
	stripped := stripFence(text)
	start := strings.Index(stripped, "{")
	end := strings.LastIndex(stripped, "}")
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("AI 返回的不是 JSON: %.200s", text)
	}
	return stripped[start : end+1], nil
}

// ParseAIJSON 从模型回复中提取首个完整 JSON 对象。
func ParseAIJSON(text string, out any) error {
	return ParseAIJSONStrict(text, out)
}

// ParseAIJSONStrict 在 ParseAIJSON 基础上校验必填键存在。
//
// 缺键零值直通是静默失败:模型返回 {} 时布尔字段拿到 false,会被当成模型的真实判断
// ("这页不能录入" / "这条数据不值得补录"),调用方与用户都无从察觉 AI 其实什么都没答。
// 只校验键存在——值为 null 是合法答案(如"没有对应的按钮")。
func ParseAIJSONStrict(text string, out any, required ...string) error {
	raw, err := extractJSONObject(text)
	if err != nil {
		return err
	}
	if len(required) > 0 {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			return fmt.Errorf("AI 返回的 JSON 无法解析: %w", err)
		}
		for _, k := range required {
			if _, ok := probe[k]; !ok {
				return fmt.Errorf("AI 输出缺少必填字段 %q", k)
			}
		}
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return fmt.Errorf("AI 返回的 JSON 无法解析: %w", err)
	}
	return nil
}

// maxRuleCodeBytes 规则代码长度上限。真实的 transform 规则是几行到几十行,
// 上千行只可能是提示词注入或模型跑飞;插件要把它 new Function 起来执行,不接无边界输入。
const maxRuleCodeBytes = 8000

// ExtractFunction 从模型回复中截取 function 源码(generate_rule 用),并做最低限度的合规校验。
//
// 校验的依据是插件侧的执行契约(utils/ruleExec.ts):代码必须含 `function transform`,
// 否则 new Function 后取不到 transform。零校验时这些输出会一路穿过服务端、扣掉积分,
// 直到用户点执行才炸——错误现场离根因最远的一种失败。
func ExtractFunction(text string) (string, error) {
	stripped := stripFence(text)
	start := strings.Index(stripped, "function")
	if start == -1 {
		return "", fmt.Errorf("AI 未返回函数代码: %.200s", text)
	}
	code := stripped[start:]
	// 围栏前带说明文字时上面没剥掉围栏,结尾的 ``` 不能留在代码里(插件当 JS 执行 = 语法错误)
	if end := strings.Index(code, "```"); end >= 0 {
		code = code[:end]
	}
	code = strings.TrimSpace(code)
	if len(code) > maxRuleCodeBytes {
		return "", fmt.Errorf("生成的规则代码超长(%d 字节,上限 %d)", len(code), maxRuleCodeBytes)
	}
	if !strings.Contains(code, "function transform") {
		return "", fmt.Errorf("生成的规则代码缺少 transform 函数签名")
	}
	if !bracesBalanced(code) {
		return "", fmt.Errorf("生成的规则代码花括号不配平(疑似被截断)")
	}
	return code, nil
}

// bracesBalanced 判断花括号是否配平,跳过字符串字面量与注释。
// 只看花括号:圆括号会被正则字面量里的 \( 之类误伤,而截断的代码必然缺右花括号。
func bracesBalanced(code string) bool {
	depth := 0
	for i := 0; i < len(code); i++ {
		switch c := code[i]; c {
		case '\'', '"', '`':
			for i++; i < len(code); i++ {
				if code[i] == '\\' {
					i++
					continue
				}
				if code[i] == c {
					break
				}
			}
			if i >= len(code) {
				return false // 字符串没闭合 = 输出被截断
			}
		case '/':
			if i+1 < len(code) && code[i+1] == '/' {
				if nl := strings.IndexByte(code[i:], '\n'); nl >= 0 {
					i += nl
				} else {
					i = len(code)
				}
			} else if i+1 < len(code) && code[i+1] == '*' {
				end := strings.Index(code[i+2:], "*/")
				if end < 0 {
					return false // 块注释没闭合 = 输出被截断
				}
				i += 2 + end + 1
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}
