package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestV2ReplayConflictAndExpiredResult(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, _, task, auth := v2Fixture(t, upstream)
	body := v2Body(t, matchBody, task, auth)
	run := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
		return w
	}
	if w := run(body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := run(body); w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("重放再次执行模型")
	}
	if w := run(strings.ReplaceAll(body, "张三", "李四")); w.Code != 409 || !strings.Contains(w.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatal(w.Body.String())
	}
	if err := db.Model(&model.AIRequest{}).Where("task_id = ?", task).Update("cache_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if w := run(body); w.Code != 410 {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("结果过期重新推理")
	}
}

func TestV2SettlementRecoveryDoesNotCallModelAgain(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, uid, task, auth := v2Fixture(t, upstream)
	if err := db.Callback().Create().Before("gorm:create").Register("fail-ledger", func(tx *gorm.DB) {
		if fail.Load() && tx.Statement.Table == "credit_ledgers" {
			tx.AddError(errors.New("注入落账失败"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	body := v2Body(t, matchBody, task, auth)
	run := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
		return w
	}
	if w := run(); w.Code != 503 {
		t.Fatal(w.Body.String())
	}
	totals, err := credits.Totals(db, uid, task)
	if err != nil || totals.Charged != 0 || totals.InFlight != 5 {
		t.Fatalf("失败不留半笔账: %+v %v", totals, err)
	}
	fail.Store(false)
	if w := run(); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatal("恢复结算重新推理")
	}
	totals, err = credits.Totals(db, uid, task)
	if err != nil || totals.Charged != 5 || totals.InFlight != 0 {
		t.Fatalf("恢复失败: %+v %v", totals, err)
	}
}

func TestV2OldClientCannotStartPaidCall(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{}`))
	defer upstream.Close()
	router, _, _ := setupGateway(t, upstream, true)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 426 {
		t.Fatal(w.Body.String())
	}
}

func v2Fixture(t *testing.T, upstream *httptest.Server) (*gin.Engine, *gorm.DB, int64, string, string) {
	t.Helper()
	router, db, uid := setupGateway(t, upstream, true)
	task := uuid.NewString()
	q, err := credits.Quote(db, uid, task, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, err := credits.Confirm(db, uid, q.ID, q.Amount)
	if err != nil {
		t.Fatal(err)
	}
	return router, db, uid, task, h.ID
}

func v2Body(t *testing.T, body, task, authorization string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	m["taskId"], m["authorizationVersion"], m["billingProtocolVersion"] = task, authorization, 2
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 新请求即使内容相同，也不能继承旧方案的永久免单资格。
func TestV2SameContentNewRequestChargesAgain(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`))
	defer upstream.Close()
	router, _, _, task, authorization := v2Fixture(t, upstream)
	for _, id := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		body := v2Body(t, strings.ReplaceAll(matchBody, "11111111-1111-4111-8111-111111111111", id), task, authorization)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ai/match-columns", strings.NewReader(body)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"charged":5`) {
			t.Fatalf("新调用必须按次扣费: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestV2RepairContextIsNotFreePass(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`))
	defer upstream.Close()
	router, _, _, task, authorization := v2Fixture(t, upstream)
	for _, context := range []string{"initial", "dynamic", "repair"} {
		raw := strings.Replace(matchBody, `"fields"`, `"context":"`+context+`","billingGroupId":"old-paid-group","fields"`, 1)
		raw = strings.Replace(raw, "11111111-1111-4111-8111-111111111111", uuid.NewString(), 1)
		body := v2Body(t, raw, task, authorization)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"charged":5`) {
			t.Fatalf("%s 与计费组不能免单 %d %s", context, w.Code, w.Body.String())
		}
	}
}

func TestV2GenerateEachCallAndInvalidFree(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          int
	}{{"成功", "有效生成内容", 200}, {"空输出", "", 422}, {"超长", strings.Repeat("长", 2100), 422}} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(chatOK(tc.content))
			defer upstream.Close()
			router, db, uid, task, auth := v2Fixture(t, upstream)
			for i := 0; i < 2; i++ {
				body := v2Body(t, `{"requestId":"`+uuid.NewString()+`","field":{"index":0,"label":"备注","tag":"input","type":"text"},"prompt":"概括","row":{"名称":"测试"}}`, task, auth)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/generate-field", strings.NewReader(body)))
				if w.Code != tc.want {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
			}
			totals, e := credits.Totals(db, uid, task)
			want := int64(0)
			if tc.want == 200 {
				want = 2
			}
			if e != nil || totals.Charged != want || totals.InFlight != 0 {
				t.Fatalf("%+v %v", totals, e)
			}
		})
	}
}
func TestV2ProtocolRepairAndNoUsableResult(t *testing.T) {
	for _, noMatch := range []bool{false, true} {
		t.Run(fmt.Sprint(noMatch), func(t *testing.T) {
			var n atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if n.Add(1) == 1 {
					chatOK("不是合法结构")(w, r)
					return
				}
				value := `{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`
				if noMatch {
					value = `{"mapping":[{"fieldIndex":0,"column":null}]}`
				}
				chatOK(value)(w, r)
			}))
			defer upstream.Close()
			router, db, uid, task, auth := v2Fixture(t, upstream)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(v2Body(t, matchBody, task, auth))))
			if w.Code != 200 || n.Load() != 2 {
				t.Fatalf("%d %s calls=%d", w.Code, w.Body.String(), n.Load())
			}
			totals, e := credits.Totals(db, uid, task)
			want := int64(5)
			if noMatch {
				want = 0
			}
			if e != nil || totals.Charged != want {
				t.Fatalf("%+v %v", totals, e)
			}
			var r model.AIRequest
			if e = db.Where("task_id = ?", task).First(&r).Error; e != nil {
				t.Fatal(e)
			}
			var usage []AttemptUsage
			if e = json.Unmarshal([]byte(r.UsageDetails), &usage); e != nil || len(usage) != 2 {
				t.Fatalf("必须记录全部技术尝试 %s %v", r.UsageDetails, e)
			}
		})
	}
}
func TestV2ZeroBalanceIncludedAndPackHasNoEntitlement(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"identityColumn":null,"reason":"无需额外身份字段"}`))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream, true)
	if e := db.Model(&model.UserSubscription{}).Where("user_id = ?", uid).Update("amount_total", 0).Error; e != nil {
		t.Fatal(e)
	}
	body := `{"requestId":"` + uuid.NewString() + `","headers":["姓名"],"sampleValues":{"姓名":["张三"]},"positionCount":1,"taskId":"` + uuid.NewString() + `","billingProtocolVersion":2}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/detect-identity", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("零余额订阅仍可用 %d %s", w.Code, w.Body.String())
	}
	if e := db.Model(&model.UserSubscription{}).Where("user_id = ?", uid).Updates(map[string]any{"plan_type": model.PlanTypePack, "amount_total": 1000}).Error; e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/detect-identity", strings.NewReader(strings.Replace(body, strings.Split(body, `"`)[3], uuid.NewString(), 1))))
	if w.Code != 403 {
		t.Fatalf("只有积分包不能新调用 %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/detect-identity", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatal("历史结果应可查询", w.Body.String())
	}
}

func TestV2EmptyColumnsAndPriceSnapshot(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`))
	defer upstream.Close()
	router, db, uid, task, auth := v2Fixture(t, upstream)
	empty := strings.Replace(matchBody, `"sampleRow":{"姓名":"张三"}`, `"sampleRow":{"姓名":""},"nonEmptyColumns":[]`, 1)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(v2Body(t, empty, task, auth))))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reason":"no_usable_result"`) || !strings.Contains(w.Body.String(), `"charged":0`) {
		t.Fatalf("全空列不收费 %d %s", w.Code, w.Body.String())
	}
	if e := db.Model(&model.CapabilityPrice{}).Where("capability = ?", "match_columns").Updates(map[string]any{"credits": 9, "price_version": 2}).Error; e != nil {
		t.Fatal(e)
	}
	paid := v2Body(t, strings.Replace(matchBody, "11111111-1111-4111-8111-111111111111", uuid.NewString(), 1), task, auth)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(paid)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unitPrice":5`) {
		t.Fatalf("有效授权沿用价格快照 %d %s", w.Code, w.Body.String())
	}
	if e := db.Model(&model.AIRequest{}).Where("task_id = ?", task).Update("created_at", time.Now().Add(-48*time.Hour)).Error; e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(paid)))
	if w.Code != 200 {
		t.Fatal("缓存从结果生成时计 24 小时", w.Body.String())
	}
	total, e := credits.Totals(db, uid, task)
	if e != nil || total.Charged != 5 {
		t.Fatalf("%+v %v", total, e)
	}
}

func TestV2CancelAfterAdmissionKeepsSettlement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, uid, task, authorization := v2Fixture(t, upstream)
	body := v2Body(t, matchBody, task, authorization)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)).WithContext(ctx))
		close(done)
	}()
	<-entered
	pending := httptest.NewRecorder()
	router.ServeHTTP(pending, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if pending.Code != 409 || calls.Load() != 1 {
		t.Fatal("同 ID 并发必须返回原在途状态", pending.Body.String())
	}
	cancel()
	if err := credits.Settle(db, uid, authorization); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	totals, e := credits.Totals(db, uid, task)
	if e != nil || totals.Charged != 5 || totals.InFlight != 0 || calls.Load() != 1 {
		t.Fatalf("已准入请求取消后按最终产出结算 %+v %v", totals, e)
	}
	before, cancelBefore := context.WithCancel(context.Background())
	cancelBefore()
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(strings.Replace(body, "11111111-1111-4111-8111-111111111111", uuid.NewString(), 1))).WithContext(before))
	if calls.Load() != 1 {
		t.Fatal("取消早于准入不得调用模型")
	}
}
func TestV2LegacyReplaySurvivesNewPolicy(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{}`))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream, true)
	r := model.AIRequest{UserID: uid, RequestID: "11111111-1111-4111-8111-111111111111", Capability: "match_columns", Status: model.AIReqOK, Credits: 0, ResponseCache: `{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`, CreatedAt: time.Now()}
	if e := db.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"charged":0`) {
		t.Fatalf("旧历史不补扣 %d %s", w.Code, w.Body.String())
	}
}

func TestV2PauseDoesNotInvalidatePaidHistory(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`))
	defer upstream.Close()
	router, db, uid, task, auth := v2Fixture(t, upstream)
	body := v2Body(t, matchBody, task, auth)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	dir := t.TempDir()
	writePrompt(t, dir, "match_columns")
	prompts, e := LoadPrompts(dir, []string{"match_columns"})
	if e != nil {
		t.Fatal(e)
	}
	g := NewGateway(db, NewCaller(db), prompts)
	g.EnableBillingV2("billing-test-key")
	g.PauseNewPaidCalls(true)
	replay := gin.New()
	for _, spec := range Specs() {
		if spec.Name == "match_columns" {
			replay.POST("/ai/match-columns", func(c *gin.Context) { c.Set("userID", uid); c.Next() }, g.Handler(spec))
		}
	}
	w = httptest.NewRecorder()
	replay.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal("暂停开关不可破坏旧结果", w.Body.String())
	}
	w = httptest.NewRecorder()
	replay.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(strings.Replace(body, "11111111-1111-4111-8111-111111111111", uuid.NewString(), 1))))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "BILLING_PAUSED") {
		t.Fatal("必须明确阻止新收费调用", w.Body.String())
	}
}
