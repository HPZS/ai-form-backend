package server

import (
	"encoding/json"
	"github.com/google/uuid"
	"strconv"
	"strings"
	"time"

	"github.com/HPZS/ai-form-backend/internal/ai"
	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/gin-gonic/gin"
)

func (s *Server) billingCatalog(c *gin.Context) {
	var prices []model.CapabilityPrice
	if err := s.db.Order("capability asc").Find(&prices).Error; err != nil {
		internalErr(c, "读取计费目录", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	metas := map[string]ai.CapMeta{}
	for _, meta := range ai.CapabilityMetas() {
		metas[meta.Key] = meta
	}
	items := []gin.H{}
	for _, p := range prices {
		items = append(items, gin.H{"name": metas[p.Capability].Name, "description": metas[p.Capability].Desc, "capability": p.Capability, "mode": p.BillingMode, "credits": p.Credits, "priceVersion": p.PriceVersion, "enabled": true, "valid": credits.ValidPrice(p)})
	}
	c.JSON(200, gin.H{"billingProtocolVersion": 2, "policyVersion": credits.PolicyV2, "capabilities": items})
}

func (s *Server) billingQuote(c *gin.Context) {
	var req struct {
		Calls         map[string]int64 `json:"calls"`
		TaskID        string           `json:"taskId"`
		MatchCalls    int64            `json:"matchCalls"`
		GenerateCalls int64            `json:"generateCalls"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if _, err := uuid.Parse(req.TaskID); err != nil || req.MatchCalls < 0 || req.GenerateCalls < 0 || req.MatchCalls > 1_000_000 || req.GenerateCalls > 1_000_000 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "任务 ID 或预计次数不合法"})
		return
	}
	if req.Calls != nil && (req.MatchCalls != 0 || req.GenerateCalls != 0) {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "不能混用两种报价计数"})
		return
	}
	calls := req.Calls
	if calls == nil {
		calls = map[string]int64{"match_columns": req.MatchCalls, "generate_field": req.GenerateCalls}
	}
	for _, count := range calls {
		if count < 0 || count > 1_000_000 {
			c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "预计次数不合法"})
			return
		}
	}
	quote, err := credits.QuoteCapabilities(s.db, c.GetInt64("userID"), req.TaskID, calls)
	if err != nil {
		ai.BillingError(c, err)
		return
	}
	totals, err := credits.Totals(s.db, c.GetInt64("userID"), req.TaskID)
	if err != nil {
		ai.BillingError(c, err)
		return
	}
	available, err := credits.Available(s.db, c.GetInt64("userID"))
	if err != nil {
		ai.BillingError(c, err)
		return
	}
	var estimated int64
	for capability, count := range calls {
		estimated += quote.Prices[capability].Credits * count
	}
	c.JSON(200, gin.H{"quoteId": quote.ID, "taskId": quote.TaskID, "policyVersion": credits.PolicyV2, "prices": quote.Prices, "estimated": estimated,
		"suggestedMaxTotal": quote.Amount, "expiresAt": quote.ExpiresAt, "available": available, "totals": totals})
}

func (s *Server) billingAuthorize(c *gin.Context) {
	var req struct {
		QuoteID  string `json:"quoteId"`
		MaxTotal int64  `json:"maxTotal"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if _, err := uuid.Parse(req.QuoteID); err != nil || req.MaxTotal < 0 || req.MaxTotal > 1_000_000_000_000 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	h, err := credits.Confirm(s.db, c.GetInt64("userID"), req.QuoteID, req.MaxTotal)
	if err != nil {
		ai.BillingError(c, err)
		return
	}
	c.JSON(200, gin.H{"authorizationVersion": h.ID, "holdId": h.ID, "taskId": h.TaskID, "maxTotal": h.Amount, "prices": h.Prices, "expiresAt": h.ExpiresAt, "priceExpiresAt": h.PriceExpiresAt})
}

func (s *Server) billingTask(c *gin.Context) {
	uid, task := c.GetInt64("userID"), c.Param("id")
	totals, err := credits.Totals(s.db, uid, task)
	if err != nil {
		ai.BillingError(c, err)
		return
	}
	var holds []model.CreditHold
	if err := s.db.Where("user_id = ? AND task_id = ? AND policy_version = ? AND status = ? AND expires_at > ?", uid, task, credits.PolicyV2, model.HoldStatusOpen, time.Now()).Order("created_at desc").Limit(1).Find(&holds).Error; err != nil {
		ai.BillingError(c, err)
		return
	}
	var authorization any
	if len(holds) > 0 {
		h := holds[0]
		authorization = gin.H{"authorizationVersion": h.ID, "holdId": h.ID, "maxTotal": h.Amount, "prices": h.Prices, "expiresAt": h.ExpiresAt, "priceExpiresAt": h.PriceExpiresAt}
	}
	var entries []struct {
		Capability string `json:"capability"`
		Calls      int64  `json:"calls"`
		Charged    int64  `json:"charged"`
	}
	if err := s.db.Model(&model.AIRequest{}).Where("user_id = ? AND task_id = ? AND status = ?", uid, task, model.AIReqOK).Select("capability, COUNT(*) AS calls, COALESCE(SUM(credits),0) AS charged").Group("capability").Scan(&entries).Error; err != nil {
		ai.BillingError(c, err)
		return
	}
	c.JSON(200, gin.H{"taskId": task, "totals": totals, "authorization": authorization, "capabilities": entries})
}

func (s *Server) billingRequests(c *gin.Context) {
	uid := c.GetInt64("userID")
	if c.GetString("role") == "admin" && c.Query("userId") != "" {
		parsed, err := strconv.ParseInt(c.Query("userId"), 10, 64)
		if err != nil || parsed <= 0 {
			c.JSON(400, gin.H{"error": "BAD_REQUEST"})
			return
		}
		uid = parsed
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	if page > 1_000_000 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	var total int64
	q := s.db.Model(&model.AIRequest{}).Where("user_id = ?", uid)
	if err := q.Count(&total).Error; err != nil {
		ai.BillingError(c, err)
		return
	}
	// 显式字段投影，业务缓存和摘要不能流入账单列表。
	var rows []struct {
		Name          string    `json:"name" gorm:"-"`
		RequestID     string    `json:"requestId"`
		TaskID        string    `json:"taskId"`
		Capability    string    `json:"capability"`
		Status        string    `json:"status"`
		Credits       int64     `json:"charged"`
		Refunded      int64     `json:"refunded"`
		BillingMode   string    `json:"mode"`
		BillingReason string    `json:"reason"`
		PolicyVersion string    `json:"policyVersion"`
		CreatedAt     time.Time `json:"createdAt"`
	}
	if err := q.Select("request_id,task_id,capability,status,credits,billing_mode,billing_reason,policy_version,created_at").Order("id desc").Offset((page - 1) * 20).Limit(20).Scan(&rows).Error; err != nil {
		ai.BillingError(c, err)
		return
	}
	names := map[string]string{}
	for _, meta := range ai.CapabilityMetas() {
		names[meta.Key] = meta.Name
	}
	for i := range rows {
		rows[i].Name = names[rows[i].Capability]
		if err := s.db.Model(&model.CreditLedger{}).Where("user_id = ? AND refund_of_request_id = ? AND delta > 0", uid, rows[i].RequestID).Select("COALESCE(SUM(delta),0)").Scan(&rows[i].Refunded).Error; err != nil {
			ai.BillingError(c, err)
			return
		}
	}
	c.JSON(200, gin.H{"entries": rows, "total": total, "page": page, "pageSize": 20})
}

func (s *Server) billingRefund(c *gin.Context) {
	var req struct {
		UserID    int64  `json:"userId"`
		RequestID string `json:"requestId"`
		RefundID  string `json:"refundId"`
		Amount    int64  `json:"amount"`
		Reason    string `json:"reason"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil || req.UserID <= 0 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if _, err := uuid.Parse(req.RefundID); err != nil || req.Amount <= 0 || strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 300 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if err := credits.Refund(s.db, req.UserID, c.GetInt64("userID"), req.RequestID, req.RefundID, req.Reason, req.Amount); err != nil {
		ai.BillingError(c, err)
		return
	}
	c.JSON(200, gin.H{"refundId": req.RefundID, "refunded": req.Amount})
}
