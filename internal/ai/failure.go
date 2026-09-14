package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// 保留可判定的错误身份，禁止把网络故障当作程序输出错误修复。
type upstreamHTTPError struct {
	status int
	detail string
}

func (e *upstreamHTTPError) Error() string { return e.detail }

type outputValidationError struct{ error }

func (e *outputValidationError) Unwrap() error { return e.error }

func aiFailureCode(err error) string {
	if code := executionBudgetCode(err); code != "" {
		return code
	}
	if errors.Is(err, context.Canceled) {
		return "AI_CANCELLED"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "AI_UPSTREAM_TIMEOUT"
	}
	var upstream *upstreamHTTPError
	if errors.As(err, &upstream) {
		switch upstream.status {
		case 401, 403:
			return "AI_UPSTREAM_AUTH_FAILED"
		case 429:
			return "AI_UPSTREAM_RATE_LIMITED"
		default:
			return "AI_UPSTREAM_UNAVAILABLE"
		}
	}
	var invalid *outputValidationError
	if errors.As(err, &invalid) {
		return "AI_OUTPUT_INVALID"
	}
	if errors.Is(err, ErrAllUpstreamsDown) {
		return "AI_UPSTREAM_UNAVAILABLE"
	}
	return "AI_EXECUTION_FAILED"
}

func aiFailureResponse(code string) (int, string) {
	switch code {
	case "AI_RECOVERY_BUDGET_EXHAUSTED":
		return http.StatusConflict, "同一未解决目标的模型尝试预算已用尽，已停止自动重试并保留进度"
	case "AI_TASK_BUDGET_EXHAUSTED":
		return http.StatusConflict, "本次工作的 AI 调用、用量或等待预算已用尽；继续前需要明确增加投入"
	case "AI_EXECUTION_UNCERTAIN":
		return http.StatusConflict, "原模型尝试结果未知，已保留身份及用量预留；不能通过重放重新调用"
	case "AI_CANCELLED":
		return http.StatusConflict, "AI 请求已取消；已经发生的用量仍保留，未完成目标可在恢复后核对"
	case "AI_UPSTREAM_TIMEOUT":
		return http.StatusGatewayTimeout, "AI 上游响应超时，未取得的用量仍待核对；没有进入程序修复"
	case "AI_UPSTREAM_AUTH_FAILED":
		return http.StatusBadGateway, "AI 上游鉴权失败，需要检查上游配置；没有进入程序修复"
	case "AI_UPSTREAM_RATE_LIMITED":
		return http.StatusServiceUnavailable, "AI 上游限流，本次请求已结束；没有进入程序修复"
	case "AI_UPSTREAM_UNAVAILABLE":
		return http.StatusServiceUnavailable, "AI 上游暂不可用，本次请求已结束；没有进入程序修复"
	case "AI_OUTPUT_INVALID":
		return http.StatusUnprocessableEntity, "AI 输出未通过协议或程序校验，已保留本次失败及用量"
	case "AI_EXECUTION_FAILED":
		return http.StatusInternalServerError, "AI 请求执行失败，已保留请求身份；请核对诊断后恢复"
	default:
		return http.StatusUnprocessableEntity, "原请求未成功，历史原因无法准确分类；请核对原请求，不自动修复程序"
	}
}

func upstreamStatusError(name string, status int, data []byte) error {
	return &upstreamHTTPError{status: status, detail: fmt.Sprintf("上游 %s 返回 %d: %.200s", name, status, string(data))}
}
