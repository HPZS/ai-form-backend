// ai-form-backend - AGPL-3.0
// 模型输出解析:容忍 markdown 围栏与前后废话(移植自插件 utils/ai.ts 的 parseAiJson)。
package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var fenceRe = regexp.MustCompile("```(json|javascript|js)?")

// extractJSONObject 从模型回复中截出首个完整 JSON 对象文本(容忍围栏与前后废话)。
func extractJSONObject(text string) (string, error) {
	stripped := strings.TrimSpace(fenceRe.ReplaceAllString(text, ""))
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

// ExtractFunction 从模型回复中截取 function 源码(generate_rule 用)。
func ExtractFunction(text string) (string, error) {
	stripped := strings.TrimSpace(fenceRe.ReplaceAllString(text, ""))
	start := strings.Index(stripped, "function")
	if start == -1 {
		return "", fmt.Errorf("AI 未返回函数代码: %.200s", text)
	}
	return strings.TrimSpace(stripped[start:]), nil
}
