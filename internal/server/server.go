// ai-form-backend - AGPL-3.0
// 路由组装与业务处理器(白名单路由:只挂载需要的,没有任何转发/代理端点)。
package server

import (
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/mail"
	"strconv"
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

// internalErr 统一的 500 出口:客户端只看到 INTERNAL,根因必须落到服务端日志——
// 否则线上每一次 INTERNAL 都无从定位,访问日志里只剩一个状态码。
func internalErr(c *gin.Context, where string, err error) {
	log.Printf("[HTTP-500] %s user=%d path=%s err=%v", where, c.GetInt64("userID"), c.Request.URL.Path, err)
	c.JSON(500, gin.H{"error": "INTERNAL"})
}

// abortInternalErr 中间件里的 500 出口(需要中断后续处理器)。
func abortInternalErr(c *gin.Context, where string, err error) {
	log.Printf("[HTTP-500] %s user=%d path=%s err=%v", where, c.GetInt64("userID"), c.Request.URL.Path, err)
	c.AbortWithStatusJSON(500, gin.H{"error": "INTERNAL"})
}

type Server struct {
	db      *gorm.DB
	cfg     *config.Config
	auth    *auth.Service
	mailer  *email.Mailer
	gateway *ai.Gateway
	pay     *payment.Handler
	limiter *ratelimit.Limiter
}

// New 组装路由。webDist 为前端构建产物(web/dist),经 SPA 回退托管;nil 则不挂前端。
func New(db *gorm.DB, cfg *config.Config, authSvc *auth.Service, mailer *email.Mailer, gateway *ai.Gateway, webDist fs.FS) *gin.Engine {
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

	// 前端控制台:静态文件 + SPA 回退(非 /v1 路径找不到文件时回 index.html)
	if webDist != nil {
		fileServer := http.FileServer(http.FS(webDist))
		r.NoRoute(func(c *gin.Context) {
			p := strings.TrimPrefix(c.Request.URL.Path, "/")
			if strings.HasPrefix(c.Request.URL.Path, "/v1/") {
				c.JSON(404, gin.H{"error": "NOT_FOUND"})
				return
			}
			if p != "" {
				if f, err := webDist.Open(p); err == nil {
					f.Close()
					fileServer.ServeHTTP(c.Writer, c.Request)
					return
				}
			}
			index, err := fs.ReadFile(webDist, "index.html")
			if err != nil {
				c.String(500, "前端资源缺失,请先构建 web/dist")
				return
			}
			c.Data(200, "text/html; charset=utf-8", index)
		})
	}

	v1 := r.Group("/v1")
	{
		v1.GET("/about", s.about)

		a := v1.Group("/auth")
		{
			a.POST("/send-code", s.sendCode)
			a.POST("/login", s.login)
			a.POST("/login-password", s.loginPassword)
			a.POST("/refresh", s.refresh)
			a.POST("/logout", s.logout)
			a.POST("/sso-exchange", s.ssoExchange)
		}

		// 支付回调无需登录态
		v1.Any("/subscription/epay/notify", s.pay.Notify)
		v1.Any("/subscription/epay/return", s.pay.Return)

		u := v1.Group("", s.authRequired)
		{
			u.GET("/me", s.me)
			u.POST("/auth/set-password", s.setPassword)
			u.POST("/auth/sso-code", s.ssoCode)
			u.GET("/credits/ledger", s.ledger)
			u.POST("/credits/estimate", s.estimate)
			u.POST("/credits/holds", s.createHold)
			u.PATCH("/credits/holds/:id/heartbeat", s.heartbeat)
			u.POST("/credits/holds/:id/settle", s.settleHold)
			u.POST("/tasks/report", s.userRateLimit("task-report", taskReportPerHour, time.Hour), s.taskReport)
			u.GET("/subscription/plans", s.plans)
			u.POST("/subscription/pay", s.userRateLimit("pay-order", payOrderPerHour, time.Hour), s.pay.CreateOrder)

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
			admin.GET("/ai-defaults", s.adminGetAIDefaults)
			admin.PUT("/ai-defaults", s.adminUpdateAIDefaults)
			admin.GET("/capability-prices", s.adminListPrices)
			admin.PUT("/capability-prices/:capability", s.adminUpdatePrice)
			admin.GET("/upstreams", s.adminListUpstreams)
			admin.POST("/upstreams", s.adminCreateUpstream)
			admin.PUT("/upstreams/:id", s.adminUpdateUpstream)
			admin.DELETE("/upstreams/:id", s.adminDeleteUpstream)
			admin.GET("/users", s.adminListUsers)
			admin.PUT("/users/:id", s.adminUpdateUser)
			admin.GET("/users/:id/subscriptions", s.adminListUserSubs)
			admin.POST("/users/:id/grants", s.adminGrantBucket)
			admin.PATCH("/subscriptions/:id", s.adminAdjustSub)
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

// AI 限流阈值:正常录入远用不到,只有滥用才触发(方案文档 §5"包含≠无限")
const (
	aiPerCapPerMinute = 60
	aiPerUserPerDay   = 5000
	aiPerIPPerDay     = 10000 // 同 IP 全部账号合计:一次性邮箱农场的总量闸门
	aiTrialPerDay     = 1000  // 无付费底座(试用)的更低日限:薅免费额度的成本闸门
	// 写接口闸门:一次录入任务一条上报、一次购买一个订单,正常用户远用不到
	taskReportPerHour = 120
	payOrderPerHour   = 20
)

// hasActiveBase 是否持有付费底座桶(试用不算);试用档 AI 日限更严。
func hasActiveBase(db *gorm.DB, userID int64) (bool, error) {
	var cnt int64
	err := db.Model(&model.UserSubscription{}).
		Where("user_id = ? AND status = ? AND ends_at > ? AND plan_type = ?",
			userID, model.SubStatusActive, time.Now(), model.PlanTypeBase).
		Count(&cnt).Error
	return cnt > 0, err
}

// aiRateLimit 分钟/日/IP/试用四条闸门一次性判定:被拒的请求一条配额都不消耗
// (逐条 Allow 串 `||` 会让后一条拒绝的请求白吃掉前几条的名额),拒绝原因落日志。
func (s *Server) aiRateLimit(capability string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetString("email")
		// 试用档日限更严,先确定档位才能把它并进同一次判定
		hasBase, err := hasActiveBase(s.db, c.GetInt64("userID"))
		if err != nil {
			abortInternalErr(c, "AI 限流查订阅底座", err)
			return
		}
		trialKey := "ai-day-trial:" + key
		rules := []ratelimit.Rule{
			{Key: "ai-min:" + capability + ":" + key, Limit: aiPerCapPerMinute, Window: time.Minute},
			{Key: "ai-day:" + key, Limit: aiPerUserPerDay, Window: 24 * time.Hour},
			{Key: "ai-ip-day:" + c.ClientIP(), Limit: aiPerIPPerDay, Window: 24 * time.Hour},
		}
		if !hasBase {
			rules = append(rules, ratelimit.Rule{Key: trialKey, Limit: aiTrialPerDay, Window: 24 * time.Hour})
		}
		if denied := s.limiter.AllowAll(rules...); denied != "" {
			log.Printf("[RATE] AI 限流拒绝 rule=%s user=%d capability=%s ip=%s",
				denied, c.GetInt64("userID"), capability, c.ClientIP())
			msg := "调用太频繁,请稍后再试"
			if denied == trialKey {
				msg = "试用期今日的调用额度已用完,开通个人版后不受此限制"
			}
			c.AbortWithStatusJSON(429, gin.H{"error": "RATE_LIMITED", "message": msg})
			return
		}
		c.Next()
	}
}

// userRateLimit 按账号限流的通用闸门,用于下单/上报这类写接口:
// 正常使用远用不到,没有闸门时脚本可无限刷单(留下大量 pending 订单)或灌统计表。
func (s *Server) userRateLimit(name string, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.limiter.Allow(name+":"+c.GetString("email"), limit, window) {
			log.Printf("[RATE] %s 限流拒绝 user=%d ip=%s", name, c.GetInt64("userID"), c.ClientIP())
			c.AbortWithStatusJSON(429, gin.H{"error": "RATE_LIMITED", "message": "操作太频繁,请稍后再试"})
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
		internalErr(c, "签发邮箱验证码", err)
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
		internalErr(c, "验证码登录", err)
		return
	}
	c.JSON(200, gin.H{
		"user":         gin.H{"email": user.Email, "role": user.Role},
		"accessToken":  pair.AccessToken,
		"refreshToken": pair.RefreshToken,
	})
}

type passwordLoginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) loginPassword(c *gin.Context) {
	var req passwordLoginReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Email == "" || req.Password == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	// 防爆破:按邮箱与 IP 双限
	email := auth.NormalizeEmail(req.Email)
	if !s.limiter.Allow("pw:"+email, 10, 15*time.Minute) ||
		!s.limiter.Allow("pw-ip:"+c.ClientIP(), 30, 15*time.Minute) {
		c.JSON(429, gin.H{"error": "RATE_LIMITED", "message": "尝试太频繁,请稍后再试"})
		return
	}
	user, pair, err := s.auth.LoginPassword(email, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrNoPassword):
			c.JSON(401, gin.H{"error": "NO_PASSWORD", "message": err.Error()})
		case errors.Is(err, auth.ErrPasswordInvalid):
			c.JSON(401, gin.H{"error": "PASSWORD_INVALID", "message": err.Error()})
		case errors.Is(err, auth.ErrUserBanned):
			c.JSON(403, gin.H{"error": "BANNED"})
		default:
			internalErr(c, "密码登录", err)
		}
		return
	}
	c.JSON(200, gin.H{
		"user":         gin.H{"email": user.Email, "role": user.Role},
		"accessToken":  pair.AccessToken,
		"refreshToken": pair.RefreshToken,
	})
}

type setPasswordReq struct {
	Password string `json:"password"`
}

func (s *Server) setPassword(c *gin.Context) {
	var req setPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if err := s.auth.SetPassword(c.GetInt64("userID"), req.Password); err != nil {
		if errors.Is(err, auth.ErrWeakPassword) {
			c.JSON(400, gin.H{"error": "WEAK_PASSWORD", "message": err.Error()})
			return
		}
		internalErr(c, "设置密码", err)
		return
	}
	c.Status(204)
}

// ssoCode 已登录端(插件)签发一次性登录码,用于打开网页控制台免登录。
func (s *Server) ssoCode(c *gin.Context) {
	userID := c.GetInt64("userID")
	if !s.limiter.Allow("sso:"+c.GetString("email"), 20, time.Hour) {
		c.JSON(429, gin.H{"error": "RATE_LIMITED"})
		return
	}
	code, err := s.auth.IssueSsoCode(userID)
	if err != nil {
		internalErr(c, "签发 SSO 码", err)
		return
	}
	c.JSON(200, gin.H{"code": code, "expiresIn": 60, "consoleUrl": s.cfg.ConsoleAddr})
}

type ssoExchangeReq struct {
	Code string `json:"code"`
}

func (s *Server) ssoExchange(c *gin.Context) {
	var req ssoExchangeReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if !s.limiter.Allow("sso-ex-ip:"+c.ClientIP(), 30, time.Hour) {
		c.JSON(429, gin.H{"error": "RATE_LIMITED"})
		return
	}
	user, pair, err := s.auth.ExchangeSsoCode(req.Code)
	if err != nil {
		if errors.Is(err, auth.ErrUserBanned) {
			c.JSON(403, gin.H{"error": "BANNED"})
			return
		}
		c.JSON(401, gin.H{"error": "SSO_CODE_INVALID", "message": "登录码无效或已过期"})
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
		internalErr(c, "登出", err)
		return
	}
	c.Status(204)
}

// ===== 用户信息与积分 =====

func (s *Server) me(c *gin.Context) {
	userID := c.GetInt64("userID")
	avail, err := credits.Available(s.db, userID)
	if err != nil {
		internalErr(c, "查可用余额", err)
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
			"planType":  sub.PlanType,
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
		internalErr(c, "查积分流水", err)
		return
	}
	c.JSON(200, gin.H{"entries": rows})
}

// estimateReq 预估请求(方案文档 §6.6):公式 = 新方案数 × 方案费 + AI 格数 × 格费。
// 判断/诊断/自愈类能力 0 分,不进预估。
type estimateReq struct {
	TaskID   string `json:"taskId"`
	NewPlans int64  `json:"newPlans"`
	AICells  int64  `json:"aiCells"`
}

// capPrice 取能力单价;能力未配置(未播种/已停用)是 0 分,查询出错必须上抛——
// 按 0 兜底会告诉用户"这次不花钱",随后真扣,是最伤信任的一种静默降级。
func (s *Server) capPrice(capability string) (int64, error) {
	var p model.CapabilityPrice
	err := s.db.Where("capability = ? AND enabled = ?", capability, true).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return p.Credits, nil
}

// estimateMaxUnits 单个任务的方案数/AI 格数上限:插件端的行数与字段数远小于此。
// 不设上限时超大入参会让乘法溢出成负数,预估重新变成"这次不花钱"。
const estimateMaxUnits = 1_000_000

func (s *Server) estimate(c *gin.Context) {
	var req estimateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if req.NewPlans < 0 || req.AICells < 0 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "newPlans/aiCells 不能为负"})
		return
	}
	if req.NewPlans > estimateMaxUnits || req.AICells > estimateMaxUnits {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "newPlans/aiCells 超出合理范围"})
		return
	}
	planPrice, err := s.capPrice("match_columns")
	if err != nil {
		internalErr(c, "预估查方案费单价", err)
		return
	}
	cellPrice, err := s.capPrice("generate_field")
	if err != nil {
		internalErr(c, "预估查 AI 格单价", err)
		return
	}
	avail, err := credits.Available(s.db, c.GetInt64("userID"))
	if err != nil {
		internalErr(c, "预估任务积分", err)
		return
	}
	c.JSON(200, gin.H{"estimated": req.NewPlans*planPrice + req.AICells*cellPrice, "available": avail})
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
		internalErr(c, "创建预占", err)
		return
	}
	c.JSON(200, gin.H{"holdId": hold.ID, "expiresAt": hold.ExpiresAt})
}

func (s *Server) heartbeat(c *gin.Context) {
	exp, err := credits.Heartbeat(s.db, c.GetInt64("userID"), c.Param("id"))
	if err != nil {
		// DB 故障不能伪装成"预占不存在":插件收到 404 会永久清掉 holdId 不再结算,
		// 冻结额白挂到两小时过期,期间用户的可用余额被压低
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			internalErr(c, "预占心跳", err)
			return
		}
		c.JSON(404, gin.H{"error": "HOLD_NOT_FOUND"})
		return
	}
	c.JSON(200, gin.H{"expiresAt": exp})
}

// settleHold 结算。重复结算幂等 204;从不存在的预占必须 404——
// 一律 204 会把 holdId 串号/传错当成"结算成功",冻结额挂到过期为止都没人知道。
func (s *Server) settleHold(c *gin.Context) {
	if err := credits.Settle(s.db, c.GetInt64("userID"), c.Param("id")); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "HOLD_NOT_FOUND"})
			return
		}
		internalErr(c, "结算预占", err)
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
		internalErr(c, "上报任务统计", err)
		return
	}
	c.Status(204)
}

// ===== 订阅 =====

func (s *Server) plans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	if err := s.db.Where("for_sale = ?", true).Order("sort_order asc").Find(&plans).Error; err != nil {
		internalErr(c, "查在售套餐", err)
		return
	}
	out := make([]gin.H, 0, len(plans))
	for _, p := range plans {
		out = append(out, gin.H{
			"id": p.ID, "name": p.Name, "planType": p.PlanType, "priceCents": p.PriceCents,
			"credits": p.TotalCredits, "durationDays": p.DurationDays,
		})
	}
	c.JSON(200, gin.H{"plans": out})
}

// ===== 管理 =====

func (s *Server) adminListPlans(c *gin.Context) {
	var plans []model.SubscriptionPlan
	if err := s.db.Order("sort_order asc").Find(&plans).Error; err != nil {
		internalErr(c, "管理台查套餐", err)
		return
	}
	c.JSON(200, gin.H{"plans": plans})
}

type planReq struct {
	Name         string `json:"name"`
	PlanType     string `json:"planType"` // base | trial | pack | bonus
	PriceCents   int64  `json:"priceCents"`
	TotalCredits int64  `json:"totalCredits"`
	DurationDays int    `json:"durationDays"`
	ForSale      *bool  `json:"forSale"`
	SortOrder    int    `json:"sortOrder"`
}

func validPlanType(t string) bool {
	return t == model.PlanTypeBase || t == model.PlanTypeTrial ||
		t == model.PlanTypePack || t == model.PlanTypeBonus
}

func (s *Server) adminCreatePlan(c *gin.Context) {
	var req planReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" || req.DurationDays <= 0 || req.TotalCredits <= 0 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if !validPlanType(req.PlanType) {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "planType 必须是 base/trial/pack/bonus"})
		return
	}
	forSale := true
	if req.ForSale != nil {
		forSale = *req.ForSale
	}
	p := model.SubscriptionPlan{
		Name: req.Name, PlanType: req.PlanType, PriceCents: req.PriceCents, TotalCredits: req.TotalCredits,
		DurationDays: req.DurationDays, ForSale: forSale, SortOrder: req.SortOrder,
	}
	if err := s.db.Create(&p).Error; err != nil {
		internalErr(c, "管理台建套餐", err)
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
	if req.PlanType != "" {
		if !validPlanType(req.PlanType) {
			c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "planType 必须是 base/trial/pack/bonus"})
			return
		}
		updates["plan_type"] = req.PlanType // 只影响之后发的桶,已发桶类型已固化
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
	if r.Error != nil {
		internalErr(c, "管理台改套餐", r.Error)
		return
	}
	if r.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "PLAN_NOT_FOUND"})
		return
	}
	c.Status(204)
}

// ===== AI 全局默认参数(能力未覆盖时统一用这里的值) =====

func (s *Server) adminGetAIDefaults(c *gin.Context) {
	var def model.AIDefault
	if err := s.db.First(&def, 1).Error; err != nil {
		internalErr(c, "管理台查 AI 默认参数", err)
		return
	}
	c.JSON(200, gin.H{"model": def.Model})
}

type aiDefaultsReq struct {
	Model string `json:"model"`
}

func (s *Server) adminUpdateAIDefaults(c *gin.Context) {
	var req aiDefaultsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "默认模型不能为空"})
		return
	}
	err := s.db.Model(&model.AIDefault{}).Where("id = 1").
		Update("model", strings.TrimSpace(req.Model)).Error
	if err != nil {
		internalErr(c, "管理台改默认模型", err)
		return
	}
	c.Status(204)
}

// adminListPrices 按能力清单固定顺序返回,并合并中文名与说明(管理台可读展示)。
func (s *Server) adminListPrices(c *gin.Context) {
	var prices []model.CapabilityPrice
	if err := s.db.Find(&prices).Error; err != nil {
		internalErr(c, "管理台查能力单价", err)
		return
	}
	byKey := map[string]model.CapabilityPrice{}
	for _, p := range prices {
		byKey[p.Capability] = p
	}
	out := make([]gin.H, 0, len(prices))
	for _, m := range ai.CapabilityMetas() {
		p, ok := byKey[m.Key]
		if !ok {
			continue // 库里没有的能力不展示(理论上播种保证齐全)
		}
		out = append(out, gin.H{
			"capability": p.Capability, "name": m.Name, "desc": m.Desc,
			"credits": p.Credits, "enabled": p.Enabled, "model": p.Model,
		})
	}
	c.JSON(200, gin.H{"prices": out})
}

type priceReq struct {
	Credits int64  `json:"credits"`
	Enabled bool   `json:"enabled"`
	Model   string `json:"model"` // 空 = 用全局默认
}

// adminUpdatePrice 整行保存;改价/改模型只影响之后的请求,在途请求与已建预占按价格快照执行。
func (s *Server) adminUpdatePrice(c *gin.Context) {
	var req priceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if req.Credits < 0 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "单价不能为负"})
		return
	}
	r := s.db.Model(&model.CapabilityPrice{}).
		Where("capability = ?", c.Param("capability")).
		Updates(map[string]any{
			"credits": req.Credits, "enabled": req.Enabled, "model": strings.TrimSpace(req.Model),
		})
	if r.Error != nil {
		internalErr(c, "管理台改能力单价", r.Error)
		return
	}
	if r.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "CAPABILITY_NOT_FOUND"})
		return
	}
	c.Status(204)
}

// ===== AI 上游管理(所有上游均为 OpenAI 兼容第三方;密钥常换,此处即改即生效) =====

func maskKey(k string) string {
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

func (s *Server) adminListUpstreams(c *gin.Context) {
	var ups []model.AIUpstream
	if err := s.db.Order("sort_order asc, id asc").Find(&ups).Error; err != nil {
		internalErr(c, "管理台查上游", err)
		return
	}
	out := make([]gin.H, 0, len(ups))
	for _, u := range ups {
		out = append(out, gin.H{
			"id": u.ID, "name": u.Name, "baseUrl": u.BaseURL,
			"apiKeyMasked": maskKey(u.APIKey), "enabled": u.Enabled, "sortOrder": u.SortOrder,
		})
	}
	c.JSON(200, gin.H{"upstreams": out})
}

type upstreamReq struct {
	Name      string `json:"name"`
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey"` // 更新时留空 = 不改密钥
	Enabled   *bool  `json:"enabled"`
	SortOrder *int   `json:"sortOrder"`
}

func (s *Server) adminCreateUpstream(c *gin.Context) {
	var req upstreamReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" || req.BaseURL == "" || req.APIKey == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "name/baseUrl/apiKey 必填"})
		return
	}
	if !strings.HasPrefix(req.BaseURL, "http://") && !strings.HasPrefix(req.BaseURL, "https://") {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "baseUrl 必须以 http(s):// 开头"})
		return
	}
	u := model.AIUpstream{Name: req.Name, BaseURL: strings.TrimRight(req.BaseURL, "/"), APIKey: req.APIKey, Enabled: true}
	if req.Enabled != nil {
		u.Enabled = *req.Enabled
	}
	if req.SortOrder != nil {
		u.SortOrder = *req.SortOrder
	}
	if err := s.db.Create(&u).Error; err != nil {
		internalErr(c, "管理台建上游", err)
		return
	}
	c.JSON(200, gin.H{"id": u.ID})
}

func (s *Server) adminUpdateUpstream(c *gin.Context) {
	var req upstreamReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	updates := map[string]any{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.BaseURL != "" {
		if !strings.HasPrefix(req.BaseURL, "http://") && !strings.HasPrefix(req.BaseURL, "https://") {
			c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "baseUrl 必须以 http(s):// 开头"})
			return
		}
		updates["base_url"] = strings.TrimRight(req.BaseURL, "/")
	}
	if req.APIKey != "" {
		updates["api_key"] = req.APIKey
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if len(updates) == 0 {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "没有可更新的字段"})
		return
	}
	r := s.db.Model(&model.AIUpstream{}).Where("id = ?", c.Param("id")).Updates(updates)
	if r.Error != nil {
		internalErr(c, "管理台改上游", r.Error)
		return
	}
	if r.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "UPSTREAM_NOT_FOUND"})
		return
	}
	c.Status(204)
}

func (s *Server) adminDeleteUpstream(c *gin.Context) {
	r := s.db.Delete(&model.AIUpstream{}, "id = ?", c.Param("id"))
	if r.Error != nil {
		internalErr(c, "管理台删上游", r.Error)
		return
	}
	if r.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "UPSTREAM_NOT_FOUND"})
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
		internalErr(c, "管理台查用户", err)
		return
	}
	c.JSON(200, gin.H{"users": users})
}

type userUpdateReq struct {
	Status string `json:"status"` // active | banned
}

// adminUpdateUser 封禁/解封;封禁同时撤销全部 refresh token(全端登出)。
func (s *Server) adminUpdateUser(c *gin.Context) {
	var req userUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil || (req.Status != "active" && req.Status != "banned") {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	var target model.User
	if err := s.db.First(&target, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(404, gin.H{"error": "USER_NOT_FOUND"})
		return
	}
	if target.Role == "admin" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "不能封禁管理员"})
		return
	}
	if err := s.db.Model(&target).Update("status", req.Status).Error; err != nil {
		internalErr(c, "管理台改用户状态", err)
		return
	}
	if req.Status == "banned" {
		s.db.Model(&model.RefreshToken{}).
			Where("user_id = ? AND revoked_at IS NULL", target.ID).
			Update("revoked_at", time.Now())
	}
	c.Status(204)
}

// ===== 管理员手动调整额度(测试发分/补偿/人工退款) =====

// adminListUserSubs 某用户的全部额度桶(含过期/作废),新的在前;PlanID=0 显示为手动发放。
func (s *Server) adminListUserSubs(c *gin.Context) {
	var subs []model.UserSubscription
	if err := s.db.Where("user_id = ?", c.Param("id")).Order("id desc").Limit(100).Find(&subs).Error; err != nil {
		internalErr(c, "管理台查用户额度桶", err)
		return
	}
	planNames := map[int64]string{}
	var plans []model.SubscriptionPlan
	s.db.Find(&plans)
	for _, p := range plans {
		planNames[p.ID] = p.Name
	}
	out := make([]gin.H, 0, len(subs))
	for _, sub := range subs {
		name := planNames[sub.PlanID]
		if sub.PlanID == 0 {
			name = "手动发放"
		}
		out = append(out, gin.H{
			"id": sub.ID, "planName": name, "planType": sub.PlanType,
			"total": sub.AmountTotal, "used": sub.AmountUsed,
			"startsAt": sub.StartsAt, "endsAt": sub.EndsAt, "status": sub.Status,
		})
	}
	c.JSON(200, gin.H{"subscriptions": out})
}

type grantReq struct {
	PlanType string `json:"planType"` // base(给订阅资格) | pack | bonus(纯积分)
	Credits  int64  `json:"credits"`
	Days     int    `json:"days"`
}

func (s *Server) adminGrantBucket(c *gin.Context) {
	var req grantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if req.PlanType != model.PlanTypeBase && req.PlanType != model.PlanTypePack && req.PlanType != model.PlanTypeBonus {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "planType 必须是 base/pack/bonus"})
		return
	}
	var target model.User
	if err := s.db.First(&target, "id = ?", c.Param("id")).Error; err != nil {
		c.JSON(404, gin.H{"error": "USER_NOT_FOUND"})
		return
	}
	sub, err := credits.AdminGrant(s.db, target.ID, req.PlanType, req.Credits, req.Days)
	if err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"id": sub.ID, "endsAt": sub.EndsAt})
}

type adjustSubReq struct {
	AddDays int  `json:"addDays"`
	Revoke  bool `json:"revoke"`
}

func (s *Server) adminAdjustSub(c *gin.Context) {
	var req adjustSubReq
	if err := c.ShouldBindJSON(&req); err != nil || (req.AddDays <= 0 && !req.Revoke) {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "addDays 或 revoke 必填其一"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "BAD_REQUEST"})
		return
	}
	if req.Revoke {
		if err := credits.RevokeBucket(s.db, id); err != nil {
			c.JSON(404, gin.H{"error": "SUBSCRIPTION_NOT_FOUND"})
			return
		}
		c.Status(204)
		return
	}
	sub, err := credits.ExtendBucket(s.db, id, req.AddDays)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "SUBSCRIPTION_NOT_FOUND"})
			return
		}
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"id": sub.ID, "endsAt": sub.EndsAt, "status": sub.Status})
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
