// ai-form-backend - AGPL-3.0
// AI 网关流水线(技术方案 v3 §5.6/§6):
//   严格解码 → 幂等闸门(唯一索引原子插入 pending + 90s 租约,可抢占接管)
//   → 订阅/余额预检 → 渲染提示词 → 上游候选链调用 → 解析(失败同链重试一次)
//   → 扣费与状态落定同事务 → 响应含 credits 并写入 24h 幂等缓存。
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
)

const (
	leaseTTL     = 90 * time.Second
	maxBodyBytes = 256 << 10
)

type Spec struct {
	Name   string
	NewReq func() Request
	Post   func(req Request, content string) (any, error)
	// BillingGroup 可选:服务端从请求内容派生计费组(方案费/AI格防重的关键,不依赖客户端诚实)。
	// 返回非空时覆盖客户端传的 billingGroupId。
	BillingGroup func(userID int64, req Request) string
	// PriceFor 可选:按请求内容调整本次单价(如 match_columns 仅 initial 语境收方案费)。
	PriceFor func(req Request, configured int64) int64
	// PriceAfter 可选:拿到解析后的结果再定价(如 match_columns 一列都没匹配上时免单——
	// "成功建立方案才收费",全空方案对用户没有价值,不收钱也不占用计费组)。
	PriceAfter func(req Request, result any, price int64) int64
}

type Gateway struct {
	db      *gorm.DB
	caller  *Caller
	prompts *PromptStore
}

func NewGateway(db *gorm.DB, caller *Caller, prompts *PromptStore) *Gateway {
	return &Gateway{db: db, caller: caller, prompts: prompts}
}

func apiErr(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"error": code, "message": msg})
}

// Handler 生成某能力的 gin 处理函数。
func (g *Gateway) Handler(spec Spec) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		userID := c.GetInt64("userID")

		// 1. 严格解码 + 校验
		req := spec.NewReq()
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
		dec := json.NewDecoder(c.Request.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(req); err != nil {
			apiErr(c, 400, "BAD_REQUEST", "请求体不合法: "+err.Error())
			return
		}
		if err := req.Validate(); err != nil {
			apiErr(c, 400, "BAD_REQUEST", err.Error())
			return
		}
		meta := req.GetMeta()
		if spec.BillingGroup != nil {
			if g := spec.BillingGroup(userID, req); g != "" {
				meta.BillingGroupID = g // 服务端派生,覆盖客户端声明
			}
		}

		// 2. 幂等闸门
		now := time.Now()
		ar := model.AIRequest{
			UserID: userID, RequestID: meta.RequestID, TaskID: meta.TaskID,
			BillingGroupID: meta.BillingGroupID, Capability: spec.Name,
			Status: model.AIReqPending, LeaseExpiresAt: now.Add(leaseTTL), CreatedAt: now,
		}
		ins := g.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ar)
		if ins.Error != nil {
			apiErr(c, 500, "INTERNAL", "内部错误")
			return
		}
		if ins.RowsAffected == 0 {
			var existing model.AIRequest
			if err := g.db.Where("user_id = ? AND request_id = ?", userID, meta.RequestID).
				First(&existing).Error; err != nil {
				apiErr(c, 500, "INTERNAL", "内部错误")
				return
			}
			switch existing.Status {
			case model.AIReqOK:
				if existing.ResponseCache != "" {
					c.Data(200, "application/json; charset=utf-8", []byte(existing.ResponseCache))
					return
				}
				apiErr(c, 410, "IDEMPOTENCY_RESULT_EXPIRED", "结果缓存已过期,请用新 requestId 重新发起")
				return
			case model.AIReqPending:
				if now.Before(existing.LeaseExpiresAt) {
					c.Header("Retry-After", "3")
					apiErr(c, 409, "REQUEST_IN_FLIGHT", "同一请求正在处理")
					return
				}
				// 租约过期:原子抢占接管
				r := g.db.Model(&model.AIRequest{}).
					Where("id = ? AND status = ? AND lease_expires_at < ?", existing.ID, model.AIReqPending, now).
					Update("lease_expires_at", now.Add(leaseTTL))
				if r.Error != nil || r.RowsAffected == 0 {
					c.Header("Retry-After", "3")
					apiErr(c, 409, "REQUEST_IN_FLIGHT", "同一请求正在处理")
					return
				}
				ar = existing
			default: // ai_invalid / upstream_error:未扣费,允许原地重跑
				r := g.db.Model(&model.AIRequest{}).
					Where("id = ? AND status = ?", existing.ID, existing.Status).
					Updates(map[string]any{"status": model.AIReqPending, "lease_expires_at": now.Add(leaseTTL)})
				if r.Error != nil || r.RowsAffected == 0 {
					c.Header("Retry-After", "3")
					apiErr(c, 409, "REQUEST_IN_FLIGHT", "同一请求正在处理")
					return
				}
				ar = existing
			}
		}

		// 3. 订阅与余额预检(减少白付模型成本;最终扣费在同事务内仍会复核)
		price, enabled, err := g.capabilityPrice(spec.Name)
		if err != nil {
			apiErr(c, 500, "INTERNAL", "内部错误")
			return
		}
		if !enabled {
			g.releaseLease(ar.ID)
			apiErr(c, 403, "CAPABILITY_DISABLED", "该能力已停用")
			return
		}
		if spec.PriceFor != nil {
			price = spec.PriceFor(req, price)
		}
		hasSub, err := hasActiveSubscription(g.db, userID)
		if err != nil {
			apiErr(c, 500, "INTERNAL", "内部错误")
			return
		}
		if !hasSub {
			g.releaseLease(ar.ID)
			apiErr(c, 403, "NO_ACTIVE_SUBSCRIPTION", "没有有效的订阅,请先开通")
			return
		}
		// 计费组已扣过 = 本次必然免单(同方案重认/同格重生成),余额为零也放行
		if price > 0 && meta.BillingGroupID != "" {
			charged, err := g.groupAlreadyCharged(userID, meta.BillingGroupID)
			if err != nil {
				apiErr(c, 500, "INTERNAL", "内部错误")
				return
			}
			if charged {
				price = 0
			}
		}
		if price > 0 {
			avail, err := credits.Available(g.db, userID)
			if err != nil {
				apiErr(c, 500, "INTERNAL", "内部错误")
				return
			}
			if avail < price {
				g.releaseLease(ar.ID)
				c.JSON(402, gin.H{"error": "INSUFFICIENT_CREDITS", "available": avail, "required": price})
				return
			}
		}

		// 4. 渲染提示词并调用上游(解析失败同链重试一次)
		system, user, promptVer, err := g.prompts.Render(spec.Name, req)
		if err != nil {
			apiErr(c, 500, "INTERNAL", "提示词配置错误")
			return
		}
		messages := []ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}
		ctx := context.Background()

		var result any
		var call *CallResult
		for attempt := 0; attempt < 2; attempt++ {
			call, err = g.caller.Call(ctx, spec.Name, messages)
			if err != nil {
				g.finishFailed(ar.ID, model.AIReqUpstreamError, call, promptVer, start)
				apiErr(c, 503, "AI_UNAVAILABLE", "AI 服务暂时不可用,请稍后重试")
				return
			}
			if call.Truncated {
				// 截断的输出不是答案:JSON 会解析失败自然拦下,纯文本能力(如生成字段内容)
				// 却会把半句话当成完整结果填进业务系统——统一按无效输出处理,不扣分
				err = fmt.Errorf("模型输出被截断(达到 maxTokens 上限)")
				continue
			}
			result, err = spec.Post(req, call.Content)
			if err == nil {
				break
			}
		}
		if err != nil {
			g.finishFailed(ar.ID, model.AIReqInvalid, call, promptVer, start)
			apiErr(c, 422, "AI_OUTPUT_INVALID", "AI 输出不符合预期,本次不扣积分")
			return
		}
		if spec.PriceAfter != nil {
			price = spec.PriceAfter(req, result, price)
		}

		// 5. 扣费与状态落定(同事务)
		var chargeRes credits.ChargeResult
		txErr := g.db.Transaction(func(tx *gorm.DB) error {
			var cerr error
			chargeRes, cerr = credits.Charge(tx, credits.ChargeArgs{
				UserID: userID, RequestID: meta.RequestID, TaskID: meta.TaskID,
				BillingGroupID: meta.BillingGroupID, Capability: spec.Name, Price: price,
			})
			if cerr != nil {
				return cerr
			}
			body, merr := mergeCredits(result, chargeRes)
			if merr != nil {
				return merr
			}
			return tx.Model(&model.AIRequest{}).Where("id = ?", ar.ID).Updates(map[string]any{
				"status": model.AIReqOK, "upstream": call.Upstream, "model": call.Model,
				"prompt_version": promptVer, "schema_version": "v1",
				"input_tokens": call.InputTokens, "output_tokens": call.OutputTokens,
				"credits": chargeRes.Charged, "latency_ms": int(time.Since(start).Milliseconds()),
				"response_cache": string(body),
			}).Error
		})
		if txErr != nil {
			if errors.Is(txErr, credits.ErrInsufficient) {
				g.releaseLease(ar.ID) // 未扣费,购买后可原 requestId 重试(会重调模型)
				avail, _ := credits.Available(g.db, userID)
				c.JSON(402, gin.H{"error": "INSUFFICIENT_CREDITS", "available": avail, "required": price})
				return
			}
			if errors.Is(txErr, credits.ErrNoActiveSub) {
				g.releaseLease(ar.ID)
				apiErr(c, 403, "NO_ACTIVE_SUBSCRIPTION", "没有有效的订阅,请先开通")
				return
			}
			// 模型已调用(成本已发生)但落账失败:必须留根因并把请求收尾。
			// 卡在 pending 会让同 requestId 的重试一律 409,直到 90 秒租约到期,
			// 而重试又会重调模型——不收尾就是一个无限白烧上游费用的循环。
			log.Printf("[AI-ALERT] 扣费事务失败(模型已调用) request_id=%s user=%d capability=%s err=%v",
				meta.RequestID, userID, spec.Name, txErr)
			g.finishFailed(ar.ID, model.AIReqUpstreamError, call, promptVer, start)
			g.releaseLease(ar.ID)
			apiErr(c, 500, "INTERNAL", "内部错误")
			return
		}
		body, _ := mergeCredits(result, chargeRes)
		c.Data(200, "application/json; charset=utf-8", body)
	}
}

// releaseLease 把 pending 租约立即置为过期,让后续同 requestId 请求可接管重跑。
// 写失败要留痕:租约没释放会让用户莫名吃满 90 秒的 409。
func (g *Gateway) releaseLease(id int64) {
	if err := g.db.Model(&model.AIRequest{}).Where("id = ?", id).
		Update("lease_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		log.Printf("[AI] 释放租约失败 ai_request_id=%d err=%v", id, err)
	}
}

// finishFailed 落定失败状态。写失败要留痕:状态丢失会让
// "未扣费可重跑"的判定链路断裂,请求就那么卡在 pending 上。
func (g *Gateway) finishFailed(id int64, status string, call *CallResult, promptVer string, start time.Time) {
	fields := map[string]any{
		"status": status, "prompt_version": promptVer,
		"latency_ms": int(time.Since(start).Milliseconds()),
	}
	if call != nil {
		fields["upstream"] = call.Upstream
		fields["model"] = call.Model
		fields["input_tokens"] = call.InputTokens
		fields["output_tokens"] = call.OutputTokens
	}
	if err := g.db.Model(&model.AIRequest{}).Where("id = ?", id).Updates(fields).Error; err != nil {
		log.Printf("[AI] 落定失败状态失败 ai_request_id=%d status=%s err=%v", id, status, err)
	}
}

func (g *Gateway) capabilityPrice(capability string) (price int64, enabled bool, err error) {
	var p model.CapabilityPrice
	e := g.db.Where("capability = ?", capability).First(&p).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return 0, true, nil
	}
	if e != nil {
		return 0, false, e
	}
	return p.Credits, p.Enabled, nil
}

// hasActiveSubscription 订阅门禁:只认 base/trial 桶。加油包/赠送桶只加积分不给资格,
// 订阅断档时购买积分随门禁自然冻结、复订即恢复(方案文档 §4)。
func hasActiveSubscription(db *gorm.DB, userID int64) (bool, error) {
	var cnt int64
	err := db.Model(&model.UserSubscription{}).
		Where("user_id = ? AND status = ? AND ends_at > ? AND plan_type IN ?",
			userID, model.SubStatusActive, time.Now(), []string{model.PlanTypeBase, model.PlanTypeTrial}).
		Count(&cnt).Error
	return cnt > 0, err
}

// groupAlreadyCharged 该计费组是否已收过费(与 credits.Charge 的防重判据同一张锚点表)。
func (g *Gateway) groupAlreadyCharged(userID int64, groupID string) (bool, error) {
	var cnt int64
	err := g.db.Model(&model.BillingGroupCharge{}).
		Where("user_id = ? AND billing_group_id = ?", userID, groupID).
		Count(&cnt).Error
	return cnt > 0, err
}

// mergeCredits 把业务结果与 credits 信息合并为一个顶层 JSON 对象。
func mergeCredits(result any, cr credits.ChargeResult) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["credits"] = map[string]any{"charged": cr.Charged, "available": cr.Available}
	return json.Marshal(m)
}

// CleanExpiredCaches 定时任务:清除超过 24 小时的幂等缓存(隐私要求)。
func CleanExpiredCaches(db *gorm.DB) (int64, error) {
	r := db.Model(&model.AIRequest{}).
		Where("status = ? AND created_at < ? AND response_cache <> ''", model.AIReqOK, time.Now().Add(-24*time.Hour)).
		Update("response_cache", "")
	return r.RowsAffected, r.Error
}

// strPtrOrNil 空串转 null 输出。
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
