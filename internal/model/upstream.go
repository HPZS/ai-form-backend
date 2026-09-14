package model

import "errors"

var ErrUpstreamGenerationOptions = errors.New("AI 上游生成参数配置无效")

// 空值保留原兼容行为。只有管理员明确配置时才发送供应商扩展，不能按模型名猜测支持。
func (u AIUpstream) ValidateGenerationOptions() error {
	if u.ThinkingMode != "" && u.ThinkingMode != "enabled" && u.ThinkingMode != "disabled" {
		return ErrUpstreamGenerationOptions
	}
	if u.TokenLimitParameter != "" && u.TokenLimitParameter != "max_tokens" && u.TokenLimitParameter != "max_completion_tokens" {
		return ErrUpstreamGenerationOptions
	}
	return nil
}
