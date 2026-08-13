// ai-form-backend - AGPL-3.0
// 路由组装与业务处理器(白名单路由:只挂载需要的,没有任何转发/代理端点)。
package server

import (
	"errors"
	"log"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/ai"
	"github.com/HPZS/ai-form-backend/internal/auth"
	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/email"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/payment"
	"github.com/HPZS/ai-form-backend/internal/ratelimit"
)

const sourceRepo = "https://github.com/HPZS/ai-form-backend"

type Server struct {
	db      *gorm.DB
	cfg     *config.Config
	auth    *auth.Service
	mailer  *email.Mailer
	gateway *ai.Gateway
	pay     *payment.Handler
	limiter *ratelimit.Limiter
}

func New(db *gorm.DB, cfg *config.Config, authSvc *auth.Service, mailer *email.Mailer, gateway *ai.Gateway) *gin.Engine {
	s := &Server{
		db: db, cfg: cfg, auth: authSvc, mailer: mailer, gateway: gateway,
		pay:     payment.New(db, cfg.Epay, cfg.CallbackAddr, cfg.ConsoleAddr),
		limiter: ratelimit.New(),
	}
	r := gin.New()
	r.Use(gin.Recovery())
	// 日志不打请求体;Authorization/验证码等敏感字段不进日志
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{SkipPaths: nil}))
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		log.Fatalf("TrustedProxies 配置错误: %v", err)
	}

	v1 := r.Group("/v1")
	{
		v1.GET("/about", s.about)

		a := v1.Group("/auth")
		{
			a.POST("/send-code", s.sendCode)
			a.POST("/login", s.login)
			a.POST("/refresh", s.refresh)
			a.POST("/logout", s.logout)
		}

		// 支付回调无需登录态
		v1.Any("/subscription/epay/notify", s.pay.Notify)
		v1.Any("/subscription/epay/return", s.pay.Return)

		u := v1.Group("", s.authRequired)
		{
			u.GET("/me", s.me)
			u.GET("/credits/ledger", s.ledger)
			u.POST("/credits/estimate", s.estimate)
			u.POST("/credits/holds", s.createHold)
			u.PATCH("/credits/holds/:id/heartbeat", s.heartbeat)
			u.POST("/credits/holds/:id/settle", s.settleHold)
			u.POST("/tasks/report", s.taskReport)
			u.GET("/subscription/plans", s.plans)
			u.POST("/subscription/pay", s.pay.CreateOrder)

			aiGroup := u.Group("/ai")
			for _, spec := range ai.Specs() {
				path := "/" + strings.ReplaceAll(spec.Name, "_", "-")
				aiGroup.POST(path, s.aiRateLimit(spec.Name), s.gateway.Handler(spec))
			}
		}

		admin := v1.Group("/admin", s.authRequired, s.adminRequired)
		{
			admin.GET("/plans", s.adminListPlans)
			admin.POST("/plans", s.adminCreatePlan)
			admin.PUT("/plans/:id", s.adminUpdatePlan)
			admin.GET("/capability-prices", s.adminListPrices)
			admin.PUT("/capability-prices/:capability", s.adminUpdatePrice)
			admin.GET("/users", s.adminListUsers)
		}
	}
	return r
}

// ===== 中间件 =====

func (s *Server) authRequired(c *gin.Context) {
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		c.AbortWithStatusJSON(401, gin.H{"error": "UNAUTHORIZED"})
		return
	}
	userID, err := s.auth.VerifyAccess(strings.TrimPrefix(h, "Bearer "))
	if err != nil {
		c.AbortWithStatusJSON(401, gin.H{"error": "UNAUTHORIZED"})
		return
	}
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		c.AbortWithStatusJSON(401, gin.H{"error": "UNAUTHORIZED"})
		return
	}
	if user.Status != "active" {
		c.AbortWithStatusJSON(403, gin.H{"error": "BANNED"})
		return
	}
	c.Set("userID", userID)
	c.Set("role", user.Role)
	c.Set("email", user.Email)
	c.Next()
}

func (s *Server) adminRequired(c *gin.Context) {
	if c.GetString("role") != "admin" {
		c.AbortWithStatusJSON(403, gin.H{"error": "FORBIDDEN"})
		return
	}
	c.Next()
}

func (s *Server) aiRateLimit(capability string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetString("email")
		if !s.limiter.Allow("ai-min:"+capability+":"+key, 60, time.Minute) ||
			!s.limiter.Allow("ai-day:"+key, 5000, 24*time.Hour) {
			c.AbortWithStatusJSON(429, gin.H{"error": "RATE_LIMITED", "message": "调用太频繁,请稍后再试"})
			return
		}
		c.Next()
	}
}

// ===== 认证 =====

type emailReq struct {
	Email string `json:"email"`
}

func (s *Server) sendCode(c *gin.Context) {
	var req emailReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	addr := auth.NormalizeEmail(req.Email)
	if _, err := mail.ParseAddress(addr); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "邮箱格式不正确"})
		return
	}
	if !s.limiter.Allow("code-ip:"+c.ClientIP(), 20, time.Hour) {
		c.JSON(429, gin.H{"error": "RATE_LIMITED"})
		return
	}
	code, err := s.auth.IssueCode(addr)
	if err != nil {
		if errors.Is(err, auth.ErrTooFrequent) {
			c.JSON(429, gin.H{"error": "RATE_LIMITED", "message": err.Error()})
			return
		}
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	if s.mailer.Configured() {
		if err := s.mailer.SendVerifyCode(addr, code); err != nil {
			log.Printf("发送验证码邮件失败: %v", err)
			c.JSON(502, gin.H{"error": "EMAIL_SEND_FAILED", "message": "验证码邮件发送失败,请稍后再试"})
			return
		}
	} else if gin.Mode() != gin.ReleaseMode {
		// 仅开发模式:SMTP 未配置时把验证码打到控制台,便于本地调试(生产必须配置 SMTP)
		log.Printf("[DEV] %s 的验证码: %s", addr, code)
	} else {
		c.JSON(503, gin.H{"error": "EMAIL_UNCONFIGURED", "message": "邮件服务未配置"})
		return
	}
	c.Status(204)
}

type loginReq struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func (s *Server) login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	user, pair, err := s.auth.Login(req.Email, req.Code)
	if err != nil {
		if errors.Is(err, auth.ErrCodeInvalid) {
			c.JSON(401, gin.H{"error": "CODE_INVALID", "message": err.Error()})
			return
		}
		if errors.Is(err, auth.ErrUserBanned) {
			c.JSON(403, gin.H{"error": "BANNED"})
			return
		}
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.JSON(200, gin.H{
		"user":         gin.H{"email": user.Email, "role": user.Role},
		"accessToken":  pair.AccessToken,
		"refreshToken": pair.RefreshToken,
	})
}

type refreshReq struct {
	RefreshToken string `json:"refreshToken"`
}

func (s *Server) refresh(c *gin.Context) {
	var req refreshReq
	if err := c.ShouldBindJSON(&req); err != nil || req.RefreshToken == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	pair, err := s.auth.Refresh(req.RefreshToken)
	if err != nil {
		c.JSON(401, gin.H{"error": "UNAUTHORIZED"})
		return
	}
	c.JSON(200, pair)
}

func (s *Server) logout(c *gin.Context) {
	var req refreshReq
	if err := c.ShouldBindJSON(&req); err != nil || req.RefreshToken == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if err := s.auth.Logout(req.RefreshToken); err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.Status(204)
}

// ===== 用户信息与积分 =====

func (s *Server) me(c *gin.Context) {
	userID := c.GetInt64("userID")
	avail, err := credits.Available(s.db, userID)
	if err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	var subs []model.UserSubscription
	s.db.Where("user_id = ? AND status = ? AND ends_at > ?", userID, model.SubStatusActive, time.Now()).
		Order("ends_at asc").Find(&subs)
	buckets := make([]gin.H, 0, len(subs))
	for _, sub := range subs {
		buckets = append(buckets, gin.H{
			"remaining": sub.AmountTotal - sub.AmountUsed,
			"total":     sub.AmountTotal,
			"endsAt":    sub.EndsAt,
		})
	}
	c.JSON(200, gin.H{
		"email":     c.GetString("email"),
		"role":      c.GetString("role"),
		"available": avail,
		"buckets":   buckets,
	})
}

func (s *Server) ledger(c *gin.Context) {
	userID := c.GetInt64("userID")
	q := s.db.Where("user_id = ?", userID).Order("id desc").Limit(50)
	if before := c.Query("beforeId"); before != "" {
		q = q.Where("id < ?", before)
	}
	var rows []model.CreditLedger
	if err := q.Find(&rows).Error; err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.JSON(200, gin.H{"entries": rows})
}

type estimateReq struct {
	TaskID      string `json:"taskId"`
	NewForms    int64  `json:"newForms"`
	NewMappings int64  `json:"newMappings"`
	NewRules    int64  `json:"newRules"`
	AICells     int64  `json:"aiCells"`
}

func (s *Server) capPrice(capability string) int64 {
	var p model.CapabilityPrice
	if err := s.db.Where("capability = ? AND enabled = ?", capability, true).First(&p).Error; err != nil {
		return 0
	}
	return p.Credits
}

func (s *Server) estimate(c *gin.Context) {
	var req estimateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	estimated := req.NewForms*s.capPrice("analyze_form") +
		req.NewMappings*s.capPrice("match_columns") +
		req.NewRules*s.capPrice("generate_rule") +
		req.AICells*s.capPrice("generate_field")
	avail, err := credits.Available(s.db, c.GetInt64("userID"))
	if err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.JSON(200, gin.H{"estimated": estimated, "available": avail})
}

type holdReq struct {
	TaskID string `json:"taskId"`
	Amount int64  `json:"amount"`
}

func (s *Server) createHold(c *gin.Context) {
	var req holdReq
	if err := c.ShouldBindJSON(&req); err != nil || req.TaskID == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	hold, err := credits.CreateHold(s.db, c.GetInt64("userID"), req.TaskID, req.Amount)
	if err != nil {
		if errors.Is(err, credits.ErrInsufficient) {
			c.JSON(402, gin.H{"error": "INSUFFICIENT_CREDITS"})
			return
		}
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.JSON(200, gin.H{"holdId": hold.ID, "expiresAt": hold.ExpiresAt})
}

func (s *Server) heartbeat(c *gin.Context) {
	exp, err := credits.Heartbeat(s.db, c.GetInt64("userID"), c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": "HOLD_NOT_FOUND"})
		return
	}
	c.JSON(200, gin.H{"expiresAt": exp})
}

func (s *Server) settleHold(c *gin.Context) {
	if err := credits.Settle(s.db, c.GetInt64("userID"), c.Param("id")); err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.Status(204)
}

type taskReportReq struct {
	TaskID        string `json:"taskId"`
	TotalRows     int    `json:"totalRows"`
	Succeeded     int    `json:"succeeded"`
	Failed        int    `json:"failed"`
	Unknown       int    `json:"unknown"`
	Skipped       int    `json:"skipped"`
	ReusedForm    bool   `json:"reusedForm"`
	ReusedMapping bool   `json:"reusedMapping"`
	AICells       int    `json:"aiCells"`
	Outcome       string `json:"outcome"`
	DurationMs    int64  `json:"durationMs"`
	SchemaVersion string `json:"schemaVersion"`
}

func (s *Server) taskReport(c *gin.Context) {
	var req taskReportReq
	if err := c.ShouldBindJSON(&req); err != nil || req.TaskID == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if _, err := uuid.Parse(req.TaskID); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "taskId 必须是 uuid"})
		return
	}
	m := model.TaskMetric{
		UserID: c.GetInt64("userID"), TaskID: req.TaskID,
		TotalRows: req.TotalRows, Succeeded: req.Succeeded, Failed: req.Failed,
		Unknown: req.Unknown, Skipped: req.Skipped,
		ReusedForm: req.ReusedForm, ReusedMapping: req.ReusedMapping,
		AICells: req.AICells, Outcome: req.Outcome, DurationMs: req.DurationMs,
		SchemaVersion: req.SchemaVersion, CreatedAt: time.Now(),
	}
	// 同任务重复上报覆盖
	err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "task_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"total_rows", "succeeded", "failed", "unknown", "skipped",
			"reused_form", "reused_mapping", "ai_cells", "outcome", "duration_ms", "schema_version",
		}),
	}).Create(&m).Error
	if err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.Status(204)
}

// ===== 订阅 =====

func (s *Server) plans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	if err := s.db.Where("for_sale = ?", true).Order("sort_order asc").Find(&plans).Error; err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	out := make([]gin.H, 0, len(plans))
	for _, p := range plans {
		out = append(out, gin.H{
			"id": p.ID, "name": p.Name, "priceCents": p.PriceCents,
			"credits": p.TotalCredits, "durationDays": p.DurationDays,
		})
	}
	c.JSON(200, gin.H{"plans": out})
}

// ===== 管理 =====

func (s *Server) adminListPlans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	if err := s.db.Order("sort_order asc").Find(&plans).Error; err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.JSON(200, gin.H{"plans": plans})
}

type planReq struct {
	Name         string `json:"name"`
	PriceCents   int64  `json:"priceCents"`
	TotalCredits int64  `json:"totalCredits"`
	DurationDays int    `json:"durationDays"`
	ForSale      *bool  `json:"forSale"`
	SortOrder    int    `json:"sortOrder"`
}

func (s *Server) adminCreatePlan(c *gin.Context) {
	var req planReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" || req.DurationDays <= 0 || req.TotalCredits <= 0 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	forSale := true
	if req.ForSale != nil {
		forSale = *req.ForSale
	}
	p := model.SubscriptionPlan{
		Name: req.Name, PriceCents: req.PriceCents, TotalCredits: req.TotalCredits,
		DurationDays: req.DurationDays, ForSale: forSale, SortOrder: req.SortOrder,
	}
	if err := s.db.Create(&p).Error; err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.JSON(200, gin.H{"plan": p})
}

// adminUpdatePlan 改价只影响新订单,已发桶不受影响(桶创建时已固化额度与期限)。
func (s *Server) adminUpdatePlan(c *gin.Context) {
	var req planReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	updates := map[string]any{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.PriceCents > 0 {
		updates["price_cents"] = req.PriceCents
	}
	if req.TotalCredits > 0 {
		updates["total_credits"] = req.TotalCredits
	}
	if req.DurationDays > 0 {
		updates["duration_days"] = req.DurationDays
	}
	if req.ForSale != nil {
		updates["for_sale"] = *req.ForSale
	}
	if len(updates) == 0 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "没有可更新的字段"})
		return
	}
	r := s.db.Model(&model.SubscriptionPlan{}).Where("id = ?", c.Param("id")).Updates(updates)
	if r.Error != nil || r.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "PLAN_NOT_FOUND"})
		return
	}
	c.Status(204)
}

func (s *Server) adminListPrices(c *gin.Context) {
	var prices []model.CapabilityPrice
	if err := s.db.Order("capability asc").Find(&prices).Error; err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.JSON(200, gin.H{"prices": prices})
}

type priceReq struct {
	Credits *int64 `json:"credits"`
	Enabled *bool  `json:"enabled"`
}

// adminUpdatePrice 改价只影响之后的请求;在途请求与已建预占按价格快照执行。
func (s *Server) adminUpdatePrice(c *gin.Context) {
	var req priceReq
	if err := c.ShouldBindJSON(&req); err != nil || (req.Credits == nil && req.Enabled == nil) {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	updates := map[string]any{}
	if req.Credits != nil {
		if *req.Credits < 0 {
			c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "单价不能为负"})
			return
		}
		updates["credits"] = *req.Credits
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	r := s.db.Model(&model.CapabilityPrice{}).
		Where("capability = ?", c.Param("capability")).Updates(updates)
	if r.Error != nil || r.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "CAPABILITY_NOT_FOUND"})
		return
	}
	c.Status(204)
}

func (s *Server) adminListUsers(c *gin.Context) {
	var users []model.User
	q := s.db.Order("id desc").Limit(100)
	if before := c.Query("beforeId"); before != "" {
		q = q.Where("id < ?", before)
	}
	if err := q.Find(&users).Error; err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL"})
		return
	}
	c.JSON(200, gin.H{"users": users})
}

// ===== about(AGPL §13 源码入口 + new-api §7 附加条款署名) =====

func (s *Server) about(c *gin.Context) {
	c.JSON(200, gin.H{
		"name":    "ai-form-backend",
		"license": "AGPL-3.0",
		"source":  sourceRepo,
		"attribution": []string{
			"Frontend design and development by New API contributors.",
			"Based on new-api: https://github.com/QuantumNous/new-api",
		},
	})
}
