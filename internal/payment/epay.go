// ai-form-backend - AGPL-3.0
// 易支付:下单/异步通知/同步返回。流程移植自 new-api 的 subscription_payment_epay.go,
// 订单幂等由 subscription.CompleteOrder 的状态守卫保证。
package payment

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/subscription"
)

type Handler struct {
	db           *gorm.DB
	cfg          config.EpayConfig
	callbackAddr string
	consoleAddr  string
}

func New(db *gorm.DB, cfg config.EpayConfig, callbackAddr, consoleAddr string) *Handler {
	return &Handler{db: db, cfg: cfg, callbackAddr: callbackAddr, consoleAddr: consoleAddr}
}

func (h *Handler) client() *epay.Client {
	if h.cfg.URL == "" || h.cfg.PID == "" || h.cfg.Key == "" {
		return nil
	}
	c, err := epay.NewClient(&epay.Config{PartnerID: h.cfg.PID, Key: h.cfg.Key}, h.cfg.URL)
	if err != nil {
		return nil
	}
	return c
}

type payRequest struct {
	PlanID int64  `json:"planId"`
	Method string `json:"method"` // alipay / wxpay(以易支付后台支持为准)
}

// CreateOrder 下单并返回收银台跳转信息。
func (h *Handler) CreateOrder(c *gin.Context) {
	var req payRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanID <= 0 || req.Method == "" {
		c.JSON(400, gin.H{"error": "BAD_REQUEST", "message": "参数错误"})
		return
	}
	client := h.client()
	if client == nil {
		c.JSON(503, gin.H{"error": "PAYMENT_UNCONFIGURED", "message": "支付未配置"})
		return
	}
	var plan model.SubscriptionPlan
	if err := h.db.First(&plan, req.PlanID).Error; err != nil {
		c.JSON(404, gin.H{"error": "PLAN_NOT_FOUND", "message": "套餐不存在"})
		return
	}
	if !plan.ForSale || plan.Trial || plan.PriceCents <= 0 {
		c.JSON(400, gin.H{"error": "PLAN_NOT_FOR_SALE", "message": "套餐不可购买"})
		return
	}
	userID := c.GetInt64("userID")
	tradeNo := fmt.Sprintf("AIF%d%s", userID, uuid.NewString()[:18])

	order := model.PaymentOrder{
		TradeNo: tradeNo, UserID: userID, PlanID: plan.ID,
		AmountCents: plan.PriceCents, Method: req.Method,
		Status: model.OrderStatusPending, CreatedAt: time.Now(),
	}
	if err := h.db.Create(&order).Error; err != nil {
		c.JSON(500, gin.H{"error": "INTERNAL", "message": "创建订单失败"})
		return
	}
	notifyURL, _ := url.Parse(h.callbackAddr + "/v1/subscription/epay/notify")
	returnURL, _ := url.Parse(h.callbackAddr + "/v1/subscription/epay/return")
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.Method,
		ServiceTradeNo: tradeNo,
		Name:           fmt.Sprintf("订阅:%s", plan.Name),
		Money:          strconv.FormatFloat(float64(plan.PriceCents)/100, 'f', 2, 64),
		Device:         epay.PC,
		NotifyUrl:      notifyURL,
		ReturnUrl:      returnURL,
	})
	if err != nil {
		h.db.Model(&model.PaymentOrder{}).
			Where("id = ? AND status = ?", order.ID, model.OrderStatusPending).
			Update("status", model.OrderStatusExpired)
		c.JSON(502, gin.H{"error": "PAYMENT_GATEWAY_ERROR", "message": "拉起支付失败"})
		return
	}
	c.JSON(200, gin.H{"url": uri, "params": params, "tradeNo": tradeNo})
}

func collectParams(c *gin.Context) map[string]string {
	params := map[string]string{}
	if c.Request.Method == http.MethodPost {
		if err := c.Request.ParseForm(); err != nil {
			return params
		}
		for k := range c.Request.PostForm {
			params[k] = c.Request.PostForm.Get(k)
		}
	} else {
		for k := range c.Request.URL.Query() {
			params[k] = c.Request.URL.Query().Get(k)
		}
	}
	return params
}

// verifyAndComplete 验签 + 幂等落账,成功返回 true。
func (h *Handler) verifyAndComplete(c *gin.Context) bool {
	params := collectParams(c)
	if len(params) == 0 {
		return false
	}
	client := h.client()
	if client == nil {
		return false
	}
	info, err := client.Verify(params)
	if err != nil || !info.VerifyStatus || info.TradeStatus != epay.StatusTradeSuccess {
		return false
	}
	// 金额一致性校验:回调金额必须等于订单金额
	var order model.PaymentOrder
	if err := h.db.Where("trade_no = ?", info.ServiceTradeNo).First(&order).Error; err != nil {
		return false
	}
	if money, err := strconv.ParseFloat(params["money"], 64); err != nil ||
		int64(money*100+0.5) != order.AmountCents {
		return false
	}
	raw := fmt.Sprintf("%v", params)
	return subscription.CompleteOrder(h.db, info.ServiceTradeNo, raw) == nil
}

// Notify 异步通知:易支付要求返回纯文本 success/fail。
func (h *Handler) Notify(c *gin.Context) {
	if h.verifyAndComplete(c) {
		c.String(200, "success")
		return
	}
	c.String(200, "fail")
}

// Return 支付完成浏览器跳回控制台购买页。
func (h *Handler) Return(c *gin.Context) {
	if h.verifyAndComplete(c) {
		c.Redirect(http.StatusFound, h.consoleAddr+"/topup?pay=success")
		return
	}
	c.Redirect(http.StatusFound, h.consoleAddr+"/topup?pay=fail")
}
