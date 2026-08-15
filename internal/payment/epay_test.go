// ai-form-backend - AGPL-3.0
package payment

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/model"
)

func setupPay(t *testing.T) (*gin.Engine, *gorm.DB, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com"}
	db.Create(&u)
	// 易支付故意不配置:套餐校验与 pack 门禁在拉起支付之前,403/400 与 503 可区分门禁是否放行
	h := New(db, config.EpayConfig{}, "http://cb", "http://console")
	r := gin.New()
	r.POST("/pay", func(c *gin.Context) {
		c.Set("userID", u.ID)
		c.Next()
	}, h.CreateOrder)
	return r, db, u.ID
}

func postPay(r *gin.Engine, planID int64) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	body := `{"planId":` + strconv.FormatInt(planID, 10) + `,"method":"alipay"}`
	req := httptest.NewRequest("POST", "/pay", strings.NewReader(body))
	r.ServeHTTP(w, req)
	return w
}

// 无有效 base 订阅时不允许购买加油包
func TestPackRequiresActiveBase(t *testing.T) {
	r, db, uid := setupPay(t)
	pack := model.SubscriptionPlan{Name: "加油包", PlanType: model.PlanTypePack, PriceCents: 1000, TotalCredits: 1000, DurationDays: 365, ForSale: true}
	db.Create(&pack)

	w := postPay(r, pack.ID)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "BASE_REQUIRED") {
		t.Fatalf("无底座应 403 BASE_REQUIRED,实际 %d: %s", w.Code, w.Body.String())
	}

	// 试用桶不算底座:仍拒绝
	db.Create(&model.UserSubscription{
		UserID: uid, PlanID: 99, PlanType: model.PlanTypeTrial, AmountTotal: 500,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	w = postPay(r, pack.ID)
	if w.Code != 403 {
		t.Fatalf("仅试用桶应仍 403,实际 %d: %s", w.Code, w.Body.String())
	}

	// 有 active base 桶:门禁放行(随后因支付未配置 503,证明已越过门禁)
	db.Create(&model.UserSubscription{
		UserID: uid, PlanID: 98, PlanType: model.PlanTypeBase, AmountTotal: 500,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	w = postPay(r, pack.ID)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "PAYMENT_UNCONFIGURED") {
		t.Fatalf("有底座应越过门禁到支付配置检查(503),实际 %d: %s", w.Code, w.Body.String())
	}
}

// 试用与赠送套餐不可购买
func TestTrialAndBonusNotSellable(t *testing.T) {
	r, db, _ := setupPay(t)
	trial := model.SubscriptionPlan{Name: "试用", PlanType: model.PlanTypeTrial, PriceCents: 100, TotalCredits: 500, DurationDays: 14, ForSale: true}
	bonus := model.SubscriptionPlan{Name: "首充礼", PlanType: model.PlanTypeBonus, PriceCents: 100, TotalCredits: 1500, DurationDays: 60, ForSale: true}
	db.Create(&trial)
	db.Create(&bonus)
	for _, id := range []int64{trial.ID, bonus.ID} {
		w := postPay(r, id)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "PLAN_NOT_FOR_SALE") {
			t.Fatalf("套餐 %d 应不可购买,实际 %d: %s", id, w.Code, w.Body.String())
		}
	}
}

// ===== 回调链路:真金白银的防线,每条失败路径都必须可判别 =====

const testEpayKey = "test-key-abc"

func setupNotify(t *testing.T) (*gin.Engine, *gorm.DB, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "buyer@example.com"}
	db.Create(&u)
	h := New(db, config.EpayConfig{URL: "http://epay.test", PID: "1001", Key: testEpayKey}, "http://cb", "http://console")
	r := gin.New()
	r.POST("/notify", h.Notify)
	return r, db, u.ID
}

// 造一个待支付订单(9.9 元 = 990 分)
func seedOrder(t *testing.T, db *gorm.DB, userID int64, tradeNo string) model.SubscriptionPlan {
	t.Helper()
	plan := model.SubscriptionPlan{Name: "基础版", PlanType: model.PlanTypeBase, PriceCents: 990, TotalCredits: 1000, DurationDays: 30, ForSale: true}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	order := model.PaymentOrder{
		TradeNo: tradeNo, UserID: userID, PlanID: plan.ID, AmountCents: plan.PriceCents,
		Method: "alipay", Status: model.OrderStatusPending, CreatedAt: time.Now(),
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	return plan
}

// 按易支付规则对回调参数签名(与服务端 Verify 同一算法)
func signedNotify(params map[string]string, key string) string {
	signed := epay.GenerateParams(params, key)
	form := url.Values{}
	for k, v := range signed {
		form.Set(k, v)
	}
	return form.Encode()
}

func postNotify(r *gin.Engine, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/notify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)
	return w
}

func orderStatus(t *testing.T, db *gorm.DB, tradeNo string) string {
	t.Helper()
	var o model.PaymentOrder
	if err := db.First(&o, "trade_no = ?", tradeNo).Error; err != nil {
		t.Fatal(err)
	}
	return o.Status
}

// 正常回调:落账、订单置 paid、积分桶到账
func TestNotifySuccess(t *testing.T) {
	r, db, uid := setupNotify(t)
	seedOrder(t, db, uid, "AIF-ok")

	body := signedNotify(map[string]string{
		"pid": "1001", "trade_no": "epay-1", "out_trade_no": "AIF-ok",
		"type": "alipay", "name": "订阅:基础版", "money": "9.90", "trade_status": "TRADE_SUCCESS",
	}, testEpayKey)
	w := postNotify(r, body)

	if w.Body.String() != "success" {
		t.Fatalf("合法回调应返回 success,实际 %q", w.Body.String())
	}
	if s := orderStatus(t, db, "AIF-ok"); s != model.OrderStatusPaid {
		t.Fatalf("订单应已支付,实际 %s", s)
	}
	var cnt int64
	db.Model(&model.UserSubscription{}).Where("user_id = ?", uid).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("应发一个积分桶,实际 %d", cnt)
	}
}

// 金额被篡改(付 0.01 想拿 9.9 的套餐):必须拒付,订单不动
func TestNotifyRejectsAmountMismatch(t *testing.T) {
	r, db, uid := setupNotify(t)
	seedOrder(t, db, uid, "AIF-amt")

	body := signedNotify(map[string]string{
		"pid": "1001", "trade_no": "epay-2", "out_trade_no": "AIF-amt",
		"type": "alipay", "name": "订阅:基础版", "money": "0.01", "trade_status": "TRADE_SUCCESS",
	}, testEpayKey)
	w := postNotify(r, body)

	if w.Body.String() != "fail" {
		t.Fatalf("金额不符应返回 fail,实际 %q", w.Body.String())
	}
	if s := orderStatus(t, db, "AIF-amt"); s != model.OrderStatusPending {
		t.Fatalf("金额不符时订单不应落账,实际 %s", s)
	}
}

// 超时未支付的订单由后台任务清理;paid 订单永不改写
func TestExpirePendingOrders(t *testing.T) {
	_, db, uid := setupNotify(t)
	seedOrder(t, db, uid, "AIF-old")
	seedOrder(t, db, uid, "AIF-new")
	seedOrder(t, db, uid, "AIF-paid")
	db.Model(&model.PaymentOrder{}).Where("trade_no = ?", "AIF-old").
		Update("created_at", time.Now().Add(-25*time.Hour))
	db.Model(&model.PaymentOrder{}).Where("trade_no = ?", "AIF-paid").
		Updates(map[string]any{"created_at": time.Now().Add(-25 * time.Hour), "status": model.OrderStatusPaid})

	n, err := ExpirePendingOrders(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应只过期 1 条,实际 %d", n)
	}
	if s := orderStatus(t, db, "AIF-old"); s != model.OrderStatusExpired {
		t.Fatalf("超时订单应过期,实际 %s", s)
	}
	if s := orderStatus(t, db, "AIF-new"); s != model.OrderStatusPending {
		t.Fatalf("新订单不应被动,实际 %s", s)
	}
	if s := orderStatus(t, db, "AIF-paid"); s != model.OrderStatusPaid {
		t.Fatalf("已支付订单永不改写,实际 %s", s)
	}
}

// 清理任务不得变成资损来源:已被清成 expired 的订单收到迟到的支付成功回调,仍要落账发桶
func TestNotifyAfterExpireStillCompletes(t *testing.T) {
	r, db, uid := setupNotify(t)
	seedOrder(t, db, uid, "AIF-late")
	db.Model(&model.PaymentOrder{}).Where("trade_no = ?", "AIF-late").
		Update("created_at", time.Now().Add(-25*time.Hour))
	if _, err := ExpirePendingOrders(db); err != nil {
		t.Fatal(err)
	}

	body := signedNotify(map[string]string{
		"pid": "1001", "trade_no": "epay-late", "out_trade_no": "AIF-late",
		"type": "alipay", "name": "订阅:基础版", "money": "9.90", "trade_status": "TRADE_SUCCESS",
	}, testEpayKey)
	if w := postNotify(r, body); w.Body.String() != "success" {
		t.Fatalf("迟到的合法回调仍应落账,实际 %q", w.Body.String())
	}
	if s := orderStatus(t, db, "AIF-late"); s != model.OrderStatusPaid {
		t.Fatalf("订单应已支付,实际 %s", s)
	}
	var cnt int64
	db.Model(&model.UserSubscription{}).Where("user_id = ?", uid).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("应发一个积分桶,实际 %d", cnt)
	}
}

// 验签失败(密钥不对/伪造回调):拒付
func TestNotifyRejectsBadSign(t *testing.T) {
	r, db, uid := setupNotify(t)
	seedOrder(t, db, uid, "AIF-sig")

	body := signedNotify(map[string]string{
		"pid": "1001", "trade_no": "epay-3", "out_trade_no": "AIF-sig",
		"type": "alipay", "name": "订阅:基础版", "money": "9.90", "trade_status": "TRADE_SUCCESS",
	}, "wrong-key")
	w := postNotify(r, body)

	if w.Body.String() != "fail" {
		t.Fatalf("验签失败应返回 fail,实际 %q", w.Body.String())
	}
	if s := orderStatus(t, db, "AIF-sig"); s != model.OrderStatusPending {
		t.Fatalf("验签失败时订单不应落账,实际 %s", s)
	}
}

// 订单不存在(回调指向未知单号):拒付且不炸
func TestNotifyRejectsUnknownOrder(t *testing.T) {
	r, _, _ := setupNotify(t)
	body := signedNotify(map[string]string{
		"pid": "1001", "trade_no": "epay-4", "out_trade_no": "AIF-nope",
		"type": "alipay", "name": "订阅:基础版", "money": "9.90", "trade_status": "TRADE_SUCCESS",
	}, testEpayKey)
	if w := postNotify(r, body); w.Body.String() != "fail" {
		t.Fatalf("未知订单应返回 fail,实际 %q", w.Body.String())
	}
}

// 交易状态非成功:拒付
func TestNotifyRejectsNonSuccessStatus(t *testing.T) {
	r, db, uid := setupNotify(t)
	seedOrder(t, db, uid, "AIF-st")
	body := signedNotify(map[string]string{
		"pid": "1001", "trade_no": "epay-5", "out_trade_no": "AIF-st",
		"type": "alipay", "name": "订阅:基础版", "money": "9.90", "trade_status": "WAIT_BUYER_PAY",
	}, testEpayKey)
	if w := postNotify(r, body); w.Body.String() != "fail" {
		t.Fatalf("未成功交易应返回 fail,实际 %q", w.Body.String())
	}
	if s := orderStatus(t, db, "AIF-st"); s != model.OrderStatusPending {
		t.Fatalf("订单不应落账,实际 %s", s)
	}
}

// 重复回调(易支付会重发):幂等,不重复发桶
func TestNotifyIdempotent(t *testing.T) {
	r, db, uid := setupNotify(t)
	seedOrder(t, db, uid, "AIF-dup")
	mk := func() string {
		return signedNotify(map[string]string{
			"pid": "1001", "trade_no": "epay-6", "out_trade_no": "AIF-dup",
			"type": "alipay", "name": "订阅:基础版", "money": "9.90", "trade_status": "TRADE_SUCCESS",
		}, testEpayKey)
	}
	postNotify(r, mk())
	postNotify(r, mk())

	var cnt int64
	db.Model(&model.UserSubscription{}).Where("user_id = ?", uid).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("重复回调只应发一个桶,实际 %d", cnt)
	}
}

// 金额解析:支付比对不容许浮点取整的模糊
func TestParseCents(t *testing.T) {
	ok := map[string]int64{"9.90": 990, "9.9": 990, "10": 1000, "0.01": 1, "0": 0, " 59.90 ": 5990, "1234.56": 123456}
	for in, want := range ok {
		got, err := parseCents(in)
		if err != nil || got != want {
			t.Fatalf("parseCents(%q) = %d, %v;应为 %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-9.90", "9.999", "abc", "9.9a", "9,90"} {
		if _, err := parseCents(bad); err == nil {
			t.Fatalf("parseCents(%q) 应报错", bad)
		}
	}
}
