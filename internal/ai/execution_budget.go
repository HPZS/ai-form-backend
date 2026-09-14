package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 初始策略快照；用量缺失保留预留，参数调整必须附完整任务成本证据。
const ExecutionMaxAttempts = 32
const ExecutionMaxTokens int64 = 256000
const ExecutionMaxWaitMs int64 = 180000
const RecoveryMaxAttempts = 2

type executionBudgetError struct{ code string }

func (e *executionBudgetError) Error() string { return e.code }

type modelAttemptKey struct{}
type startModelAttempt func(context.Context, model.AIUpstream, callParams, []ChatMessage) (context.Context, func(AttemptUsage) error, error)

func executionScope(r *model.AIRequest) string {
	if r.WorkID != "" {
		return r.WorkID
	}
	if r.TaskID != "" {
		return r.TaskID
	}
	return r.RequestID
}
func recoveryGroup(r *model.AIRequest) string {
	if r.RecoveryGroupID != "" {
		return r.RecoveryGroupID
	}
	if r.HandoffID != "" {
		return r.HandoffID
	}
	return r.RequestID
}

func saveAttemptUsage(tx *gorm.DB, r *model.AIRequest, attempts []AttemptUsage) error {
	raw, err := json.Marshal(attempts)
	if err != nil {
		return err
	}
	r.UsageDetails = string(raw)
	r.InputTokens, r.OutputTokens = 0, 0
	for _, item := range attempts {
		r.InputTokens += item.InputTokens
		r.OutputTokens += item.OutputTokens
	}
	result := tx.Model(&model.AIRequest{}).Where("id = ? AND status = ? AND lease_token = ?", r.ID, model.AIReqPending, r.LeaseToken).Updates(map[string]any{"usage_details": r.UsageDetails, "input_tokens": r.InputTokens, "output_tokens": r.OutputTokens})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLeaseLost
	}
	return nil
}

func (g *Gateway) withExecutionBudget(ctx context.Context, r *model.AIRequest) context.Context {
	start := func(parent context.Context, up model.AIUpstream, params callParams, messages []ChatMessage) (context.Context, func(AttemptUsage) error, error) {
		body, err := json.Marshal(messages)
		if err != nil {
			return nil, nil, err
		}
		// UTF-8 字节数加消息结构余量作为保守输入预留；供应商实际 usage 仍是最终已知用量。
		reserved := int64(len(body) + len(messages)*256 + params.MaxTokens)
		waitAllowance := int64(callTimeout / time.Millisecond)
		waitCause := error(context.DeadlineExceeded)
		index := 0
		scope, group := executionScope(r), recoveryGroup(r)
		reservedRequest := *r
		var denied error
		err = g.db.Transaction(func(tx *gorm.DB) error {
			if err := model.LockUser(tx, r.UserID); err != nil {
				return err
			}
			if err := parent.Err(); err != nil {
				return err
			}
			if cancelled, err := cancellationRequested(tx, r.UserID, r.RequestID, r.TaskID); err != nil {
				return err
			} else if cancelled {
				return context.Canceled
			}
			var current model.AIRequest
			if err := tx.Where("id = ? AND status = ? AND lease_token = ?", r.ID, model.AIReqPending, r.LeaseToken).First(&current).Error; err != nil {
				return err
			}
			budget := model.AIExecutionBudget{UserID: r.UserID, WorkID: scope, MaxAttempts: ExecutionMaxAttempts, MaxTokens: ExecutionMaxTokens, MaxWaitMs: ExecutionMaxWaitMs, Groups: map[string]int{}, GroupExtra: map[string]int{}}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&budget).Error; err != nil {
				return err
			}
			if err := tx.Where("user_id = ? AND work_id = ?", r.UserID, scope).First(&budget).Error; err != nil {
				return err
			}
			if budget.Groups[group] >= RecoveryMaxAttempts+budget.GroupExtra[group] {
				denied = &executionBudgetError{"AI_RECOVERY_BUDGET_EXHAUSTED"}
				return nil
			}
			if budget.Attempts >= budget.MaxAttempts || budget.KnownTokens+budget.ReservedTokens+reserved > budget.MaxTokens {
				denied = &executionBudgetError{"AI_TASK_BUDGET_EXHAUSTED"}
				return nil
			}
			remaining := budget.MaxWaitMs - budget.WaitMs - budget.ReservedWaitMs
			if remaining <= 0 {
				denied = &executionBudgetError{"AI_TASK_BUDGET_EXHAUSTED"}
				return nil
			}
			if remaining <= waitAllowance {
				waitAllowance = remaining
				waitCause = &executionBudgetError{"AI_TASK_BUDGET_EXHAUSTED"}
			}
			var attempts []AttemptUsage
			if current.UsageDetails != "" {
				if err := json.Unmarshal([]byte(current.UsageDetails), &attempts); err != nil {
					return err
				}
			}
			for _, attempt := range attempts {
				if attempt.Status == "started" {
					return &executionBudgetError{"AI_EXECUTION_UNCERTAIN"}
				}
			}
			index = len(attempts)
			attempts = append(attempts, AttemptUsage{Upstream: up.Name, Model: params.Model, Status: "started", CostStatus: "unknown", ReservedTokens: reserved, ReservedWaitMs: waitAllowance})
			if err := saveAttemptUsage(tx, &reservedRequest, attempts); err != nil {
				return err
			}
			if budget.Groups == nil {
				budget.Groups = map[string]int{}
			}
			budget.Attempts++
			budget.Groups[group]++
			budget.ReservedTokens += reserved
			budget.ReservedWaitMs += waitAllowance
			return tx.Save(&budget).Error
		})
		if err != nil {
			return nil, nil, err
		}
		// 拒绝时仅提交预算初始记录，不预留、不分发，确保首个超额请求也能查询和显式扩额。
		if denied != nil {
			return nil, nil, denied
		}
		r.UsageDetails, r.InputTokens, r.OutputTokens = reservedRequest.UsageDetails, reservedRequest.InputTokens, reservedRequest.OutputTokens
		attemptContext, cancel := context.WithTimeoutCause(parent, time.Duration(waitAllowance)*time.Millisecond, waitCause)
		finish := func(item AttemptUsage) error {
			defer cancel()
			completedRequest := *r
			err := g.db.Transaction(func(tx *gorm.DB) error {
				if err := model.LockUser(tx, r.UserID); err != nil {
					return err
				}
				var current model.AIRequest
				if err := tx.Where("id = ? AND status = ? AND lease_token = ?", r.ID, model.AIReqPending, r.LeaseToken).First(&current).Error; err != nil {
					return err
				}
				var attempts []AttemptUsage
				if err := json.Unmarshal([]byte(current.UsageDetails), &attempts); err != nil {
					return err
				}
				if index >= len(attempts) || attempts[index].Status != "started" {
					return fmt.Errorf("上游尝试身份已变化，拒绝重复结算")
				}
				var budget model.AIExecutionBudget
				if err := tx.Where("user_id = ? AND work_id = ?", r.UserID, scope).First(&budget).Error; err != nil {
					return err
				}
				known := int64(item.InputTokens) + int64(item.OutputTokens)
				budget.KnownTokens += known
				budget.ReservedWaitMs -= waitAllowance
				budget.WaitMs += item.DurationMs
				if item.Dispatch == dispatchNotSent {
					budget.Attempts--
					budget.Groups[group]--
				}
				if item.UsageKnown {
					budget.ReservedTokens -= reserved
				} else {
					released := min(known, reserved)
					budget.ReservedTokens -= released
					item.ReservedTokens = reserved - released
				}
				attempts[index] = item
				if err := saveAttemptUsage(tx, &completedRequest, attempts); err != nil {
					return err
				}
				return tx.Save(&budget).Error
			})
			if err == nil {
				r.UsageDetails, r.InputTokens, r.OutputTokens = completedRequest.UsageDetails, completedRequest.InputTokens, completedRequest.OutputTokens
			}
			return err
		}
		return attemptContext, finish, nil
	}
	return context.WithValue(ctx, modelAttemptKey{}, startModelAttempt(start))
}

func hasUnfinishedAttempt(r *model.AIRequest) bool {
	var attempts []AttemptUsage
	if err := json.Unmarshal([]byte(r.UsageDetails), &attempts); err != nil {
		return r.UsageDetails != ""
	}
	for _, attempt := range attempts {
		if attempt.Status == "started" {
			return true
		}
	}
	return false
}

func executionBudgetCode(err error) string {
	var budget *executionBudgetError
	if errors.As(err, &budget) {
		return budget.code
	}
	return ""
}

func executionBudgetView(b model.AIExecutionBudget) gin.H {
	return gin.H{"workId": b.WorkID, "attempts": b.Attempts, "knownTokens": b.KnownTokens, "reservedTokens": b.ReservedTokens, "waitMs": b.WaitMs, "reservedWaitMs": b.ReservedWaitMs, "maxAttempts": b.MaxAttempts, "maxTokens": b.MaxTokens, "maxWaitMs": b.MaxWaitMs, "revision": b.Extensions}
}

func (g *Gateway) QueryExecutionBudget(c *gin.Context) {
	var budget model.AIExecutionBudget
	if err := g.db.Where("user_id = ? AND work_id = ?", c.GetInt64("userID"), c.Param("id")).First(&budget).Error; err != nil {
		BillingError(c, err)
		return
	}
	c.JSON(200, executionBudgetView(budget))
}

// 只有明确的扩额请求能增加预算；重复点击或丢失回执按同一扩额身份重放。
func (g *Gateway) ExtendExecutionBudget(c *gin.Context) {
	var input struct {
		RequestID        string `json:"requestId"`
		ExpectedRevision int    `json:"expectedRevision"`
		GroupID          string `json:"groupId"`
		Confirmation     string `json:"confirmation"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Confirmation != "increase-ai-budget" || len(input.GroupID) > 128 {
		apiErr(c, 400, "INVALID_REQUEST", "需要明确增加 AI 预算的确认及原预算版本")
		return
	}
	if _, err := uuid.Parse(input.RequestID); err != nil {
		apiErr(c, 400, "INVALID_REQUEST", "扩额 requestId 必须是 UUID")
		return
	}
	var budget model.AIExecutionBudget
	err := g.db.Transaction(func(tx *gorm.DB) error {
		userID := c.GetInt64("userID")
		if err := model.LockUser(tx, userID); err != nil {
			return err
		}
		if err := tx.Where("user_id = ? AND work_id = ?", userID, c.Param("id")).First(&budget).Error; err != nil {
			return err
		}
		if original, exists := budget.ExtensionRequests[input.RequestID]; exists {
			if original.ExpectedRevision != input.ExpectedRevision || original.GroupID != input.GroupID {
				return credits.ErrConflict
			}
			return nil
		}
		if budget.Extensions != input.ExpectedRevision {
			return credits.ErrConflict
		}
		if budget.Extensions >= 3 {
			return &executionBudgetError{"AI_TASK_BUDGET_EXHAUSTED"}
		}
		if input.GroupID != "" {
			if _, exists := budget.Groups[input.GroupID]; !exists {
				return credits.ErrConflict
			}
			if budget.GroupExtra == nil {
				budget.GroupExtra = map[string]int{}
			}
			budget.GroupExtra[input.GroupID] += RecoveryMaxAttempts
		}
		budget.Extensions++
		if budget.ExtensionRequests == nil {
			budget.ExtensionRequests = map[string]model.AIExecutionBudgetExtension{}
		}
		budget.ExtensionRequests[input.RequestID] = model.AIExecutionBudgetExtension{ExpectedRevision: input.ExpectedRevision, GroupID: input.GroupID}
		budget.MaxAttempts += ExecutionMaxAttempts
		budget.MaxTokens += ExecutionMaxTokens
		budget.MaxWaitMs += ExecutionMaxWaitMs
		return tx.Save(&budget).Error
	})
	if err != nil {
		if code := executionBudgetCode(err); code != "" {
			apiErr(c, http.StatusConflict, code, "本次工作已达到追加预算上限，请先核对当前未解决问题")
		} else {
			BillingError(c, err)
		}
		return
	}
	c.JSON(200, executionBudgetView(budget))
}
