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
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
)

const (
	leaseTTL     = 90 * time.Second
	maxBodyBytes = 256 << 10
	// 整条调用链(含重试与上游切换)的总时限,必须短于租约:
	// 否则请求还在跑、租约已被别人接管,白白多调一次模型
	callChainTimeout = 80 * time.Second
)

// errLeaseLost 落账时发现租约已被接管:本次结果作废(事务回滚,未扣费)。
var errLeaseLost = errors.New("租约已易主")

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

// internalErr 网关的 500 出口:客户端只看到 INTERNAL,根因必须落到服务端日志,
// 否则线上排障只剩一个状态码(守则 3.2)。
func (g *Gateway) internalErr(c *gin.Context, requestID, capability, where string, err error) {
	log.Printf("[AI-500] %s request_id=%s capability=%s err=%v", where, requestID, capability, err)
	apiErr(c, 500, "INTERNAL", "内部错误")
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
		// 计费组一律由服务端从请求内容派生,绝不采信客户端声明:
		// 用户从自己的流水接口就能读到历史已扣费的 group id,回放它即可白嫖模型调用。
		// 没有派生规则的能力直接清空,免得客户端塞的值意外参与防重判定。
		meta.BillingGroupID = ""
		if spec.BillingGroup != nil {
			meta.BillingGroupID = spec.BillingGroup(userID, req)
		}

		// 2. 幂等闸门
		now := time.Now()
		// 租约凭证:落账时凭它确认"我仍是这个请求的持有者"。租约到期被接管后,
		// 原请求即使跑完也不得落账——否则二次扣费并覆盖接管者的结果。
		leaseToken := uuid.NewString()
		ar := model.AIRequest{
			UserID: userID, RequestID: meta.RequestID, TaskID: meta.TaskID,
			BillingGroupID: meta.BillingGroupID, Capability: spec.Name,
			Status: model.AIReqPending, LeaseExpiresAt: now.Add(leaseTTL),
			LeaseToken: leaseToken, CreatedAt: now,
		}
		ins := g.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ar)
		if ins.Error != nil {
			g.internalErr(c, meta.RequestID, spec.Name, "写幂等闸门", ins.Error)
			return
		}
		if ins.RowsAffected == 0 {
			var existing model.AIRequest
			if err := g.db.Where("user_id = ? AND request_id = ?", userID, meta.RequestID).
				First(&existing).Error; err != nil {
				g.internalErr(c, meta.RequestID, spec.Name, "查在途请求", err)
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
					Updates(map[string]any{"lease_expires_at": now.Add(leaseTTL), "lease_token": leaseToken})
				if r.Error != nil || r.RowsAffected == 0 {
					c.Header("Retry-After", "3")
					apiErr(c, 409, "REQUEST_IN_FLIGHT", "同一请求正在处理")
					return
				}
				ar = existing
			default: // ai_invalid / upstream_error:未扣费,允许原地重跑
				r := g.db.Model(&model.AIRequest{}).
					Where("id = ? AND status = ?", existing.ID, existing.Status).
					Updates(map[string]any{
						"status": model.AIReqPending, "lease_expires_at": now.Add(leaseTTL), "lease_token": leaseToken,
					})
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
			g.internalErr(c, meta.RequestID, spec.Name, "查能力单价", err)
			return
		}
		if !enabled {
			g.releaseLease(ar.ID, leaseToken)
			apiErr(c, 403, "CAPABILITY_DISABLED", "该能力已停用")
			return
		}
		if spec.PriceFor != nil {
			price = spec.PriceFor(req, price)
		}
		hasSub, err := hasActiveSubscription(g.db, userID)
		if err != nil {
			g.internalErr(c, meta.RequestID, spec.Name, "查订阅状态", err)
			return
		}
		if !hasSub {
			g.releaseLease(ar.ID, leaseToken)
			apiErr(c, 403, "NO_ACTIVE_SUBSCRIPTION", "没有有效的订阅,请先开通")
			return
		}
		// 计费组已扣过 = 本次必然免单(同方案重认/同格重生成),余额为零也放行
		if price > 0 && meta.BillingGroupID != "" {
			charged, err := g.groupAlreadyCharged(userID, meta.BillingGroupID)
			if err != nil {
				g.internalErr(c, meta.RequestID, spec.Name, "查计费组防重", err)
				return
			}
			if charged {
				price = 0
			}
		}
		if price > 0 {
			avail, err := credits.Available(g.db, userID)
			if err != nil {
				g.internalErr(c, meta.RequestID, spec.Name, "查可用余额", err)
				return
			}
			if avail < price {
				g.releaseLease(ar.ID, leaseToken)
				c.JSON(402, gin.H{"error": "INSUFFICIENT_CREDITS", "available": avail, "required": price})
				return
			}
		}

		// 4. 渲染提示词并调用上游(解析失败同链重试一次)
		system, user, promptVer, err := g.prompts.Render(spec.Name, req)
		if err != nil {
			g.internalErr(c, meta.RequestID, spec.Name, "渲染提示词", err)
			return
		}
		messages := []ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}
		ctx, cancel := context.WithTimeout(context.Background(), callChainTimeout)
		defer cancel()

		var result any
		var call *CallResult
		// usage 累计本次请求真实发生的上游成本:解析失败重试时,被丢弃的那一次一样烧了 token。
		// 只记最后一次会让计量凭空少一半,单价复盘与成本核算全跟着错。
		var usage CallResult
		for attempt := 0; attempt < 2; attempt++ {
			call, err = g.caller.Call(ctx, spec.Name, messages)
			if call != nil {
				usage.InputTokens += call.InputTokens
				usage.OutputTokens += call.OutputTokens
				usage.Upstream, usage.Model = call.Upstream, call.Model
			}
			if err != nil {
				g.finishFailed(ar.ID, leaseToken, model.AIReqUpstreamError, usage, promptVer, start)
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
			g.finishFailed(ar.ID, leaseToken, model.AIReqInvalid, usage, promptVer, start)
			apiErr(c, 422, "AI_OUTPUT_INVALID", "AI 输出不符合预期,本次不扣积分")
			return
		}
		if spec.PriceAfter != nil {
			price = spec.PriceAfter(req, result, price)
		}

		// 5. 扣费与状态落定(同事务)
		var chargeRes credits.ChargeResult
		// 与落库的 prompt_version / model / schema_version 同源:回给客户端的必须是
		// 真正用过的那一份,不能事后另行拼一个近似值(架构设计 5.1)
		respMeta := &RespMeta{
			Capability: spec.Name, PromptVersion: promptVer, SchemaVersion: "v1",
			Model: usage.Model, Upstream: usage.Upstream, RequestID: meta.RequestID,
		}
		txErr := g.db.Transaction(func(tx *gorm.DB) error {
			var cerr error
			chargeRes, cerr = credits.Charge(tx, credits.ChargeArgs{
				UserID: userID, RequestID: meta.RequestID, TaskID: meta.TaskID,
				BillingGroupID: meta.BillingGroupID, Capability: spec.Name, Price: price,
			})
			if cerr != nil {
				return cerr
			}
			respMeta.LatencyMs = int(time.Since(start).Milliseconds())
			body, merr := mergeCredits(result, chargeRes, respMeta)
			if merr != nil {
				return merr
			}
			// 落账守卫:只有仍持有租约的请求能落账(RowsAffected==0 = 租约已易主)
			claim := tx.Model(&model.AIRequest{}).
				Where("id = ? AND status = ? AND lease_token = ?", ar.ID, model.AIReqPending, leaseToken).
				Updates(map[string]any{
					"status": model.AIReqOK, "upstream": usage.Upstream, "model": usage.Model,
					"prompt_version": promptVer, "schema_version": "v1",
					"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
					"credits": chargeRes.Charged, "latency_ms": int(time.Since(start).Milliseconds()),
					"response_cache": string(body),
				})
			if claim.Error != nil {
				return claim.Error
			}
			if claim.RowsAffected == 0 {
				return errLeaseLost
			}
			return nil
		})
		if txErr != nil {
			if errors.Is(txErr, errLeaseLost) {
				// 租约在处理期间被接管:接管者已经(或正在)给出结果,本次结果整个作废。
				// 事务回滚保证没有扣费,状态与缓存都归接管者写。
				log.Printf("[AI] 租约已易主,丢弃本次结果 request_id=%s user=%d capability=%s",
					meta.RequestID, userID, spec.Name)
				c.Header("Retry-After", "3")
				apiErr(c, 409, "REQUEST_IN_FLIGHT", "同一请求正在处理")
				return
			}
			if errors.Is(txErr, credits.ErrInsufficient) {
				g.releaseLease(ar.ID, leaseToken) // 未扣费,购买后可原 requestId 重试(会重调模型)
				avail, _ := credits.Available(g.db, userID)
				c.JSON(402, gin.H{"error": "INSUFFICIENT_CREDITS", "available": avail, "required": price})
				return
			}
			if errors.Is(txErr, credits.ErrNoActiveSub) {
				g.releaseLease(ar.ID, leaseToken)
				apiErr(c, 403, "NO_ACTIVE_SUBSCRIPTION", "没有有效的订阅,请先开通")
				return
			}
			// 模型已调用(成本已发生)但落账失败:必须留根因并把请求收尾。
			// 卡在 pending 会让同 requestId 的重试一律 409,直到 90 秒租约到期,
			// 而重试又会重调模型——不收尾就是一个无限白烧上游费用的循环。
			log.Printf("[AI-ALERT] 扣费事务失败(模型已调用) request_id=%s user=%d capability=%s err=%v",
				meta.RequestID, userID, spec.Name, txErr)
			g.finishFailed(ar.ID, leaseToken, model.AIReqUpstreamError, usage, promptVer, start)
			g.releaseLease(ar.ID, leaseToken)
			apiErr(c, 500, "INTERNAL", "内部错误")
			return
		}
		body, _ := mergeCredits(result, chargeRes, respMeta)
		c.Data(200, "application/json; charset=utf-8", body)
	}
}

// releaseLease 把 pending 租约立即置为过期,让后续同 requestId 请求可接管重跑。
// 同样受租约凭证守卫:租约已易主时不能去动接管者的行。
// 写失败要留痕:租约没释放会让用户莫名吃满 90 秒的 409。
func (g *Gateway) releaseLease(id int64, leaseToken string) {
	if err := g.db.Model(&model.AIRequest{}).
		Where("id = ? AND lease_token = ?", id, leaseToken).
		Update("lease_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		log.Printf("[AI] 释放租约失败 ai_request_id=%d err=%v", id, err)
	}
}

// finishFailed 落定失败状态,并记下已经发生的上游成本(失败不扣积分,但 token 是真烧了)。
// 写失败要留痕:状态丢失会让"未扣费可重跑"的判定链路断裂,请求就那么卡在 pending 上。
func (g *Gateway) finishFailed(id int64, leaseToken, status string, usage CallResult, promptVer string, start time.Time) {
	fields := map[string]any{
		"status": status, "prompt_version": promptVer,
		"latency_ms":   int(time.Since(start).Milliseconds()),
		"upstream":     usage.Upstream,
		"model":        usage.Model,
		"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
	}
	// 租约已易主时不落定:那一行归接管者,改它会把接管者的结果覆盖掉
	if err := g.db.Model(&model.AIRequest{}).
		Where("id = ? AND lease_token = ?", id, leaseToken).
		Updates(fields).Error; err != nil {
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

// RespMeta 随每次能力响应回传的版本元信息。
//
// 这些值本来只写进 ai_requests 表,客户端拿不到——于是插件的取证包无法回答
// "这一次是哪个提示词、哪个模型答的"。模型或提示词一改,历史结论就无从归因,
// 守则 §3.2"仅凭取证材料重建执行路径"在跨仓库这一段直接断掉。
//
// 只回版本与标识,不回提示词正文(提示词是私有运行时配置)。
type RespMeta struct {
	Capability    string `json:"capability"`
	PromptVersion string `json:"promptVersion"`
	SchemaVersion string `json:"schemaVersion"`
	Model         string `json:"model,omitempty"`
	Upstream      string `json:"upstream,omitempty"`
	RequestID     string `json:"requestId"`
	LatencyMs     int    `json:"latencyMs"`
}

// mergeCredits 把业务结果、credits 与版本元信息合并为一个顶层 JSON 对象。
func mergeCredits(result any, cr credits.ChargeResult, meta *RespMeta) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["credits"] = map[string]any{"charged": cr.Charged, "available": cr.Available}
	if meta != nil {
		m["meta"] = meta
	}
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
