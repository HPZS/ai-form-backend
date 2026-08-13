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

// ParseAIJSON 从模型回复中提取首个完整 JSON 对象。
func ParseAIJSON(text string, out any) error {
	stripped := strings.TrimSpace(fenceRe.ReplaceAllString(text, ""))
	start := strings.Index(stripped, "{")
	end := strings.LastIndex(stripped, "}")
	if start == -1 || end == -1 || end < start {
		return fmt.Errorf("AI 返回的不是 JSON: %.200s", text)
	}
	if err := json.Unmarshal([]byte(stripped[start:end+1]), out); err != nil {
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
