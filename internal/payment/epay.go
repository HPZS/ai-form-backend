// ai-form-backend - AGPL-3.0
// 易支付:下单/异步通知/同步返回。流程移植自 new-api 的 subscription_payment_epay.go,
// 订单幂等由 subscription.CompleteOrder 的状态守卫保证。
package payment

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	var plan model.SubscriptionPlan
	if err := h.db.First(&plan, req.PlanID).Error; err != nil {
		c.JSON(404, gin.H{"error": "PLAN_NOT_FOUND", "message": "套餐不存在"})
		return
	}
	// 只有 base 与 pack 可购买;试用/赠送桶由系统发放
	sellable := plan.PlanType == model.PlanTypeBase || plan.PlanType == model.PlanTypePack
	if !plan.ForSale || !sellable || plan.PriceCents <= 0 {
		c.JSON(400, gin.H{"error": "PLAN_NOT_FOR_SALE", "message": "套餐不可购买"})
		return
	}
	userID := c.GetInt64("userID")
	// 加油包门禁(方案文档 §6.1):必须持有有效的底座订阅,堵住"买包绕过底座"的漏洞
	if plan.PlanType == model.PlanTypePack {
		var baseCnt int64
		if err := h.db.Model(&model.UserSubscription{}).
			Where("user_id = ? AND status = ? AND ends_at > ? AND plan_type = ?",
				userID, model.SubStatusActive, time.Now(), model.PlanTypeBase).
			Count(&baseCnt).Error; err != nil {
			c.JSON(500, gin.H{"error": "INTERNAL", "message": "内部错误"})
			return
		}
		if baseCnt == 0 {
			c.JSON(403, gin.H{"error": "BASE_REQUIRED", "message": "加油包需要有效的个人版订阅,请先开通个人版"})
			return
		}
	}
	client := h.client()
	if client == nil {
		c.JSON(503, gin.H{"error": "PAYMENT_UNCONFIGURED", "message": "支付未配置"})
		return
	}
	tradeNo := fmt.Sprintf("AIF%d%s", userID, uuid.NewString()[:18])

	order := model.PaymentOrder{
		TradeNo: tradeNo, UserID: userID, PlanID: plan.ID,
		AmountCents: plan.PriceCents, Method: req.Method,
		Status: model.OrderStatusPending, CreatedAt: time.Now(),
	}
	if err := h.db.Create(&order).Error; err != nil {
		log.Printf("[PAY] 创建订单失败 user=%d plan=%d err=%v", userID, plan.ID, err)
		c.JSON(500, gin.H{"error": "INTERNAL", "message": "创建订单失败"})
		return
	}
	notifyURL, err := url.Parse(h.callbackAddr + "/v1/subscription/epay/notify")
	if err != nil {
		log.Printf("[PAY] 回调地址配置错误 callbackAddr=%q err=%v", h.callbackAddr, err)
		c.JSON(500, gin.H{"error": "INTERNAL", "message": "回调地址配置错误"})
		return
	}
	returnURL, err := url.Parse(h.callbackAddr + "/v1/subscription/epay/return")
	if err != nil {
		log.Printf("[PAY] 同步返回地址配置错误 callbackAddr=%q err=%v", h.callbackAddr, err)
		c.JSON(500, gin.H{"error": "INTERNAL", "message": "回调地址配置错误"})
		return
	}
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
		log.Printf("[PAY] 拉起支付失败 trade_no=%s user=%d err=%v", tradeNo, userID, err)
		if e := h.db.Model(&model.PaymentOrder{}).
			Where("id = ? AND status = ?", order.ID, model.OrderStatusPending).
			Update("status", model.OrderStatusExpired).Error; e != nil {
			// 作废失败会留下永不回收的 pending 订单,必须能查到
			log.Printf("[PAY] 作废未拉起的订单失败 trade_no=%s err=%v", tradeNo, e)
		}
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

// parseCents 把 "9.90"/"9.9"/"10" 精确解析为分。
// 不走 float:浮点换算在边界与负值上取整方向会错,支付金额比对不容许这种模糊。
func parseCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("金额非法: %q", s)
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	if len(fracPart) > 2 {
		return 0, fmt.Errorf("金额精度超过分: %q", s)
	}
	yuan, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("金额非法: %q", s)
	}
	cents := int64(0)
	if fracPart != "" {
		f, err := strconv.ParseInt(fracPart, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("金额非法: %q", s)
		}
		if len(fracPart) == 1 {
			f *= 10
		}
		cents = f
	}
	return yuan*100 + cents, nil
}

// verifyAndComplete 验签 + 幂等落账,成功返回 true。
// 这是真金白银的路径:每条拒付原因都必须留痕——用户付了钱没到账时,
// 运营方要能凭日志区分"回调没来 / 验签失败 / 金额被篡改 / 库挂了",不能只看到一个 fail。
func (h *Handler) verifyAndComplete(c *gin.Context) bool {
	params := collectParams(c)
	if len(params) == 0 {
		log.Printf("[PAY] 回调无参数 method=%s", c.Request.Method)
		return false
	}
	tradeNo := params["out_trade_no"]
	client := h.client()
	if client == nil {
		log.Printf("[PAY] 收到回调但支付未配置 trade_no=%s", tradeNo)
		return false
	}
	info, err := client.Verify(params)
	if err != nil {
		log.Printf("[PAY] 回调解析失败 trade_no=%s err=%v", tradeNo, err)
		return false
	}
	if !info.VerifyStatus {
		// 验签不过 = 密钥不符或伪造回调,两者都要人来看
		log.Printf("[PAY-ALERT] 回调验签失败 trade_no=%s pid=%s", tradeNo, params["pid"])
		return false
	}
	if info.TradeStatus != epay.StatusTradeSuccess {
		log.Printf("[PAY] 回调交易状态非成功 trade_no=%s status=%s", tradeNo, info.TradeStatus)
		return false
	}
	// 金额一致性校验:回调金额必须等于订单金额
	var order model.PaymentOrder
	if err := h.db.Where("trade_no = ?", info.ServiceTradeNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("[PAY-ALERT] 回调指向不存在的订单 trade_no=%s", info.ServiceTradeNo)
		} else {
			log.Printf("[PAY] 回调查订单失败 trade_no=%s err=%v", info.ServiceTradeNo, err)
		}
		return false
	}
	cents, err := parseCents(params["money"])
	if err != nil {
		log.Printf("[PAY-ALERT] 回调金额无法解析 trade_no=%s money=%q err=%v", info.ServiceTradeNo, params["money"], err)
		return false
	}
	if cents != order.AmountCents {
		// 金额对不上多半是攻击(小额支付冒领大额套餐),必须显眼
		log.Printf("[PAY-ALERT] 回调金额与订单不符 trade_no=%s 回调=%d分 订单=%d分 user=%d",
			info.ServiceTradeNo, cents, order.AmountCents, order.UserID)
		return false
	}
	raw := fmt.Sprintf("%v", params)
	if err := subscription.CompleteOrder(h.db, info.ServiceTradeNo, raw); err != nil {
		// 钱已收到但发桶失败:用户侧表现为"付了钱没积分",必须留痕以便人工补发
		log.Printf("[PAY-ALERT] 回调落账失败(款已收,桶未发) trade_no=%s user=%d err=%v",
			info.ServiceTradeNo, order.UserID, err)
		return false
	}
	log.Printf("[PAY] 回调落账成功 trade_no=%s user=%d 金额=%d分", info.ServiceTradeNo, order.UserID, cents)
	return true
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
