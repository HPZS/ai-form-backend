package ai

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EnableBillingV2 只在应用入口启用；旧测试仍覆盖历史版本的重放与账务行为。
func (g *Gateway) PauseNewPaidCalls(paused bool) { g.billingPaused = paused }

func (g *Gateway) EnableBillingV2(key string) { g.billingV2 = true; g.digestKey = []byte(key) }

func BillingError(c *gin.Context, err error) {
	status, code := 500, "INTERNAL"
	switch {
	case errors.Is(err, credits.ErrCapabilityDisabled):
		status, code = 403, "CAPABILITY_DISABLED"
	case errors.Is(err, credits.ErrRefundAmount):
		status, code = 400, "REFUND_EXCEEDS_CHARGE"
	case errors.Is(err, credits.ErrFundsRevoked):
		status, code = 503, "BILLING_FUNDS_REVOKED"
	case errors.Is(err, credits.ErrAuthorization):
		status, code = 402, "BILLING_AUTHORIZATION_REQUIRED"
	case errors.Is(err, credits.ErrBudget):
		status, code = 402, "TASK_BUDGET_EXCEEDED"
	case errors.Is(err, credits.ErrQuoteExpired):
		status, code = 409, "BILLING_QUOTE_EXPIRED"
	case errors.Is(err, credits.ErrInsufficient):
		status, code = 402, "INSUFFICIENT_CREDITS"
	case errors.Is(err, credits.ErrNoActiveSub):
		status, code = 403, "NO_ACTIVE_SUBSCRIPTION"
	case errors.Is(err, credits.ErrConflict):
		status, code = 409, "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, credits.ErrConfig):
		status, code = 503, "BILLING_CONFIG_INVALID"
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, code = 404, "NOT_FOUND"
	}
	if status == 500 {
		log.Printf("[BILLING-ERROR] user=%d path=%s err=%v", c.GetInt64("userID"), c.Request.URL.Path, err)
		apiErr(c, status, code, "费用处理暂时不可用，请核对后重试")
		return
	}
	apiErr(c, status, code, err.Error())
}

func (g *Gateway) requestDigest(spec Spec, req Request) (string, error) {
	// 内容摘要只约束同一个请求身份；不同 ID 永远不会因为摘要相同而免单。
	raw, err := json.Marshal(struct {
		Capability string
		Request    Request
	}{spec.Name, req})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, g.digestKey)
	if _, err := mac.Write(raw); err != nil {
		return "", err
	}
	return "h1:" + hex.EncodeToString(mac.Sum(nil)), nil
}

func (g *Gateway) handleBillingV2(c *gin.Context, spec Spec, req Request) {
	userID, meta := c.GetInt64("userID"), req.GetMeta()
	digest, err := g.requestDigest(spec, req)
	if err != nil {
		BillingError(c, err)
		return
	}
	var r model.AIRequest
	execute, replay := false, false
	err = g.db.Transaction(func(tx *gorm.DB) error {
		if err := c.Request.Context().Err(); err != nil {
			return err
		}
		if err := model.LockUser(tx, userID); err != nil {
			return err
		}
		found := tx.Where("user_id = ? AND request_id = ?", userID, meta.RequestID).First(&r).Error
		if found == nil {
			if r.Capability != spec.Name || (r.PolicyVersion == credits.PolicyV2 && r.RequestDigest != digest) {
				return credits.ErrConflict
			}
			replay = true
			if r.PolicyVersion != credits.PolicyV2 && r.Status == model.AIReqPending && time.Now().After(r.LeaseExpiresAt) {
				r.Status, r.BillingReason = credits.RequestFailed, "legacy_execution_expired"
				return tx.Save(&r).Error
			}
			if r.PolicyVersion == credits.PolicyV2 && r.Status == model.AIReqPending && time.Now().After(r.LeaseExpiresAt) {
				if r.ExecutionAttempts >= 3 || time.Since(r.CreatedAt) > 10*time.Minute {
					if err := credits.ReleaseRequest(tx, &r); err != nil {
						return err
					}
					r.Status, r.BillingReason = credits.RequestFailed, "execution_failed"
					return tx.Save(&r).Error
				}
				r.LeaseToken, r.LeaseExpiresAt, r.ExecutionAttempts = uuid.NewString(), time.Now().Add(leaseTTL), r.ExecutionAttempts+1
				execute = true
				return tx.Save(&r).Error
			}
			return nil
		}
		if !errors.Is(found, gorm.ErrRecordNotFound) {
			return found
		}
		var p model.CapabilityPrice
		if err := tx.Where("capability = ?", spec.Name).First(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return credits.ErrConfig
			}
			return err
		}
		if !credits.ValidPrice(p) {
			return credits.ErrConfig
		}
		if p.BillingMode == credits.ModePerCall && g.billingPaused {
			return errBillingPaused
		}
		if p.BillingMode == credits.ModePerCall && meta.BillingProtocolVersion != 2 {
			return errClientUpgrade
		}
		sub, err := credits.HasSubscription(tx, userID)
		if err != nil {
			return err
		}
		if !sub {
			return credits.ErrNoActiveSub
		}
		r = model.AIRequest{UserID: userID, RequestID: meta.RequestID, TaskID: meta.TaskID, Capability: spec.Name, PolicyVersion: credits.PolicyV2,
			CallPhase: meta.CallPhase, HandoffID: meta.HandoffID, CompilationID: meta.CompilationID,
			BillingMode: p.BillingMode, PriceVersion: p.PriceVersion, UnitPrice: p.Credits, AuthorizationVersion: meta.AuthorizationVersion, RequestDigest: digest,
			Status: model.AIReqPending, LeaseToken: uuid.NewString(), LeaseExpiresAt: time.Now().Add(leaseTTL), ExecutionAttempts: 1, CreatedAt: time.Now()}
		if p.BillingMode == credits.ModePerCall {
			if err := credits.ReserveRequest(tx, &r); err != nil {
				return err
			}
		}
		if err := c.Request.Context().Err(); err != nil {
			return err
		}
		execute = true
		return tx.Create(&r).Error
	})
	if errors.Is(err, errBillingPaused) {
		apiErr(c, 503, "BILLING_PAUSED", "新的积分调用暂时暂停，历史结果和订阅包含能力仍可用")
		return
	}
	if errors.Is(err, errClientUpgrade) {
		apiErr(c, 426, "BILLING_CLIENT_UPGRADE_REQUIRED", "计费规则已更新，请升级插件后继续")
		return
	}
	if errors.Is(err, errCapabilityDisabled) {
		apiErr(c, 403, "CAPABILITY_DISABLED", "该能力已停用")
		return
	}
	if err != nil {
		BillingError(c, err)
		return
	}
	if execute {
		g.executeV2(spec, req, &r)
	}
	if r.Status == credits.SettlementPending {
		if err := g.settleV2(&r); err != nil {
			log.Printf("[BILLING-PENDING] request=%s err=%v", r.RequestID, err)
			c.JSON(503, gin.H{"error": "BILLING_SETTLEMENT_PENDING", "message": "AI 结果已经生成，费用正在核对，请恢复原请求", "requestId": r.RequestID})
			return
		}
	}
	g.respondV2(c, &r, replay, meta.BillingProtocolVersion == 2)
}

var errBillingPaused = errors.New("新积分调用已暂停")
var errClientUpgrade = errors.New("计费客户端需升级")
var errCapabilityDisabled = errors.New("能力已停用")

func (g *Gateway) executeV2(spec Spec, req Request, r *model.AIRequest) {
	start := time.Now()
	system, user, promptVer, err := g.prompts.Render(spec.Name, req)
	if err != nil {
		g.failV2(r, "execution_failed", err)
		return
	}
	messages := []ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}
	if spec.Messages != nil {
		messages, err = spec.Messages(req, system, user)
		if err != nil {
			g.failV2(r, "execution_failed", err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), CallChainTimeout)
	defer cancel()
	var attempts []AttemptUsage
	if r.UsageDetails != "" {
		if err := json.Unmarshal([]byte(r.UsageDetails), &attempts); err != nil {
			g.failV2(r, "usage_invalid", err)
			return
		}
	}
	ctx = withUsageObserver(ctx, func(item AttemptUsage) {
		attempts = append(attempts, item)
		// 每次上游结束即持久化，进程中断仍能追溯已发生的技术尝试。
		raw, err := json.Marshal(attempts)
		if err != nil {
			log.Printf("[AI-USAGE-ENCODE] request=%s err=%v", r.RequestID, err)
			return
		}
		r.UsageDetails = string(raw)
		r.InputTokens, r.OutputTokens = 0, 0
		for _, a := range attempts {
			r.InputTokens += a.InputTokens
			r.OutputTokens += a.OutputTokens
		}
		if err = g.db.Model(&model.AIRequest{}).Where("id = ? AND lease_token = ?", r.ID, r.LeaseToken).Updates(map[string]any{"usage_details": r.UsageDetails, "input_tokens": r.InputTokens, "output_tokens": r.OutputTokens}).Error; err != nil {
			log.Printf("[AI-USAGE-PERSIST] request=%s err=%v", r.RequestID, err)
		}
	})
	var result any
	usage := CallResult{}
	for attempt := 0; attempt < 2; attempt++ {
		call, callErr := g.caller.Call(ctx, spec.Name, messages)
		if call != nil {
			usage.InputTokens += call.InputTokens
			usage.OutputTokens += call.OutputTokens
			usage.Upstream, usage.Model = call.Upstream, call.Model
		}
		if callErr != nil {
			err = callErr
			break
		}
		if call.Truncated {
			err = fmt.Errorf("模型输出截断")
		} else {
			result, err = spec.Post(req, call.Content)
		}
		if err == nil {
			break
		}
		log.Printf("[AI-V2-INVALID] request=%s attempt=%d reason=%s", r.RequestID, attempt+1, safeValidationReason(err))
		messages = append(messages, ChatMessage{Role: "user", Content: spec.ProtocolRepairMessage(req, err)})
	}
	r.Upstream, r.Model = usage.Upstream, usage.Model
	r.PromptVersion, r.SchemaVersion, r.LatencyMs = promptVer, "v1", int(time.Since(start).Milliseconds())
	if err != nil {
		g.failV2(r, "execution_failed", err)
		return
	}
	reason := "successful_call"
	if r.BillingMode == credits.ModeIncluded {
		reason = "subscription_included"
	}
	if spec.Name == "match_columns" {
		if match, ok := req.(*MatchColumnsReq); ok && match.NonEmptyColumns != nil {
			allowed := map[string]bool{}
			for _, h := range match.NonEmptyColumns {
				allowed[h] = true
			}
			if m, ok := result.(map[string]any); ok {
				if items, ok := m["mapping"].([]mappedColumn); ok {
					for i := range items {
						if items[i].Column != nil && !allowed[*items[i].Column] {
							items[i].Column = nil
						}
					}
					m["mapping"] = items
				}
			}
		}
		if spec.PriceAfter != nil && spec.PriceAfter(req, result, 1) == 0 {
			reason = "no_usable_result"
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		g.failV2(r, "execution_failed", err)
		return
	}
	now, expiry := time.Now(), time.Now().Add(24*time.Hour)
	update := g.db.Model(&model.AIRequest{}).Where("id = ? AND status = ? AND lease_token = ?", r.ID, model.AIReqPending, r.LeaseToken).
		Updates(map[string]any{"status": credits.SettlementPending, "response_cache": string(raw), "result_created_at": now, "cache_expires_at": expiry,
			"billing_reason": reason, "input_tokens": r.InputTokens, "output_tokens": r.OutputTokens, "upstream": r.Upstream, "model": r.Model, "prompt_version": promptVer, "schema_version": "v1", "latency_ms": r.LatencyMs})
	if update.Error != nil || update.RowsAffected != 1 {
		log.Printf("[AI-V2-PERSIST-FAILED] request=%s rows=%d err=%v", r.RequestID, update.RowsAffected, update.Error)
		return
	}
	r.Status, r.ResponseCache, r.BillingReason = credits.SettlementPending, string(raw), reason
	r.ResultCreatedAt, r.CacheExpiresAt = &now, &expiry
}

func (g *Gateway) failV2(r *model.AIRequest, reason string, cause error) {
	log.Printf("[AI-V2-FAILED] request=%s capability=%s reason=%s err=%s", r.RequestID, r.Capability, reason, safeValidationReason(cause))
	err := g.db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, r.UserID); err != nil {
			return err
		}
		var current model.AIRequest
		if err := tx.First(&current, r.ID).Error; err != nil {
			return err
		}
		if current.Status != model.AIReqPending || current.LeaseToken != r.LeaseToken {
			return errLeaseLost
		}
		if err := credits.ReleaseRequest(tx, &current); err != nil {
			return err
		}
		current.Status, current.BillingReason = credits.RequestFailed, reason
		current.UsageDetails = r.UsageDetails
		current.InputTokens, current.OutputTokens = r.InputTokens, r.OutputTokens
		current.Model, current.Upstream = r.Model, r.Upstream
		current.LatencyMs, current.PromptVersion, current.SchemaVersion = r.LatencyMs, r.PromptVersion, r.SchemaVersion
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		*r = current
		return nil
	})
	if err != nil {
		log.Printf("[AI-V2-FAIL-STATE] request=%s err=%v", r.RequestID, err)
	}
}

func (g *Gateway) settleV2(r *model.AIRequest) error {
	return g.db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, r.UserID); err != nil {
			return err
		}
		var current model.AIRequest
		if err := tx.First(&current, r.ID).Error; err != nil {
			return err
		}
		if current.Status == model.AIReqOK {
			*r = current
			return nil
		}
		if current.Status != credits.SettlementPending {
			return errLeaseLost
		}
		if current.CacheExpiresAt == nil || !time.Now().Before(*current.CacheExpiresAt) {
			if err := credits.ReleaseRequest(tx, &current); err != nil {
				return err
			}
			current.Status, current.BillingReason, current.ResponseCache = credits.RequestFailed, "execution_failed", ""
		} else {
			if current.BillingReason == "no_usable_result" || current.BillingMode == credits.ModeIncluded {
				if err := credits.ReleaseRequest(tx, &current); err != nil {
					return err
				}
			} else if err := credits.DebitReserved(tx, &current); err != nil {
				return err
			}
			current.Status = model.AIReqOK
		}
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		available, err := credits.Available(tx, current.UserID)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.CreditLedger{}).Where("user_id = ? AND request_id = ? AND delta < 0", current.UserID, current.RequestID).Update("balance_after", available).Error; err != nil {
			return err
		}
		*r = current
		return nil
	})
}

func (g *Gateway) billingView(r *model.AIRequest, replayed bool) (gin.H, error) {
	var refund int64
	if err := g.db.Model(&model.CreditLedger{}).Where("user_id = ? AND refund_of_request_id = ? AND delta > 0", r.UserID, r.RequestID).Select("COALESCE(SUM(delta),0)").Scan(&refund).Error; err != nil {
		return nil, err
	}
	status := "pending"
	if r.Status == model.AIReqOK {
		status = "settled"
	} else if r.Status == credits.SettlementPending {
		status = credits.SettlementPending
	} else if r.Status != model.AIReqPending {
		status = "failed"
	}
	return gin.H{"policyVersion": r.PolicyVersion, "taskId": r.TaskID, "requestId": r.RequestID, "authorizationVersion": r.AuthorizationVersion, "mode": r.BillingMode,
		"callPhase": r.CallPhase, "handoffId": r.HandoffID, "compilationId": r.CompilationID, "modelCalls": requestModelCalls(r),
		"status": status, "reason": r.BillingReason, "unitPrice": r.UnitPrice, "priceVersion": r.PriceVersion, "settledCredits": r.Credits, "refundedCredits": refund, "replayed": replayed,
		"balanceAsOf": time.Now(), "cacheExpiresAt": r.CacheExpiresAt, "reservedCredits": r.ReservedCredits, "inputTokens": r.InputTokens, "outputTokens": r.OutputTokens, "costStatus": "unknown"}, nil
}

func (g *Gateway) respondV2(c *gin.Context, r *model.AIRequest, replayed, withBilling bool) {
	if r.Status != model.AIReqOK {
		if r.Status == model.AIReqPending {
			c.Header("Retry-After", "3")
			apiErr(c, 409, "REQUEST_IN_FLIGHT", "原请求仍在处理，请查询原请求")
			return
		}
		apiErr(c, 422, "AI_OUTPUT_INVALID", "原请求未成功，未新增扣费；重新尝试请发起新调用")
		return
	}
	billing, err := g.billingView(r, replayed)
	if err != nil {
		BillingError(c, err)
		return
	}
	if r.ResponseCache == "" || (r.CacheExpiresAt != nil && !time.Now().Before(*r.CacheExpiresAt)) {
		c.JSON(410, gin.H{"error": "RESULT_EXPIRED", "message": "原结果已过期，历史消费不会重复扣除；如需重新调用请确认费用", "billing": billing})
		return
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(r.ResponseCache), &result); err != nil {
		BillingError(c, err)
		return
	}
	available, err := credits.Available(g.db, r.UserID)
	if err != nil {
		log.Printf("[AI-V2-BALANCE] request=%s err=%v", r.RequestID, err)
		result["credits"] = gin.H{"charged": r.Credits, "available": nil}
		billing["balanceAsOf"] = nil
	} else {
		result["credits"] = gin.H{"charged": r.Credits, "available": available}
	}
	result["meta"] = &RespMeta{Capability: r.Capability, PromptVersion: r.PromptVersion, SchemaVersion: r.SchemaVersion, Model: r.Model, Upstream: r.Upstream, RequestID: r.RequestID, LatencyMs: r.LatencyMs,
		CallPhase: r.CallPhase, HandoffID: r.HandoffID, CompilationID: r.CompilationID, ModelCalls: requestModelCalls(r)}
	if withBilling {
		result["billing"] = billing
	}
	c.JSON(200, result)
}

// 历史请求没有逐次记录时返回未知，不能把缺失统计伪装成零次调用。
func requestModelCalls(r *model.AIRequest) *int {
	if r.UsageDetails == "" {
		return nil
	}
	var attempts []AttemptUsage
	if err := json.Unmarshal([]byte(r.UsageDetails), &attempts); err != nil {
		log.Printf("[AI-USAGE-READ] request=%s err=%v", r.RequestID, err)
		return nil
	}
	count := len(attempts)
	return &count
}

func (g *Gateway) QueryRequest(c *gin.Context) {
	var r model.AIRequest
	if err := g.db.Where("user_id = ? AND request_id = ?", c.GetInt64("userID"), c.Param("id")).First(&r).Error; err != nil {
		BillingError(c, err)
		return
	}
	if r.Status == credits.SettlementPending {
		if err := g.settleV2(&r); err != nil {
			log.Printf("[BILLING-QUERY-PENDING] request=%s err=%v", r.RequestID, err)
		}
	}
	if r.Status == model.AIReqOK {
		g.respondV2(c, &r, true, true)
		return
	}
	billing, err := g.billingView(&r, true)
	if err != nil {
		BillingError(c, err)
		return
	}
	c.JSON(200, gin.H{"billing": billing})
}

// RecoverBillingV2 与清缓存共用终态守卫：先结算/释放，后清理唯一结果。
func (g *Gateway) RecoverBillingV2() error {
	var rows []model.AIRequest
	if err := g.db.Where("policy_version = ? AND (status = ? OR (status = ? AND lease_expires_at < ?))", credits.PolicyV2, credits.SettlementPending, model.AIReqPending, time.Now().Add(-10*time.Minute)).Limit(200).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		r := &rows[i]
		if r.Status == credits.SettlementPending {
			if err := g.settleV2(r); err != nil {
				log.Printf("[BILLING-RECOVERY] request=%s err=%v", r.RequestID, err)
			}
		} else {
			g.failV2(r, "execution_failed", fmt.Errorf("执行租约已过期"))
		}
	}
	return nil
}
