// ai-form-backend - AGPL-3.0
package ai

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/internal/model"
)

func writePrompt(t *testing.T, dir, cap string) {
	t.Helper()
	content := "version: v1\nsystem: |\n  测试系统提示词\nuser: |\n  {{json .Req}}\n"
	if err := os.WriteFile(filepath.Join(dir, cap+".yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupGateway(t *testing.T, upstream *httptest.Server) (*gin.Engine, *gorm.DB, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	u := model.User{Email: "t@example.com", Role: "user", Status: "active"}
	db.Create(&u)
	db.Create(&model.UserSubscription{
		UserID: u.ID, PlanID: 1, PlanType: model.PlanTypeBase, AmountTotal: 100,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	db.Create(&model.AIDefault{ID: 1, Model: "m"})
	db.Create(&model.CapabilityPrice{Capability: "match_columns", Credits: 5, Enabled: true})
	db.Create(&model.CapabilityPrice{Capability: "generate_field", Credits: 1, Enabled: true})
	db.Create(&model.AIUpstream{Name: "t1", BaseURL: upstream.URL, APIKey: "k", Enabled: true})

	dir := t.TempDir()
	writePrompt(t, dir, "match_columns")
	writePrompt(t, dir, "generate_field")
	writePrompt(t, dir, "analyze_form")
	prompts, err := LoadPrompts(dir, []string{"match_columns", "generate_field", "analyze_form"})
	if err != nil {
		t.Fatal(err)
	}
	g := NewGateway(db, NewCaller(db), prompts)

	r := gin.New()
	for _, sp := range Specs() {
		if sp.Name == "match_columns" || sp.Name == "generate_field" || sp.Name == "analyze_form" {
			r.POST("/ai/"+strings.ReplaceAll(sp.Name, "_", "-"), func(c *gin.Context) {
				c.Set("userID", u.ID)
				c.Next()
			}, g.Handler(sp))
		}
	}
	return r, db, u.ID
}

const matchBody = `{"requestId":"11111111-1111-4111-8111-111111111111","fields":[{"index":0,"label":"姓名","tag":"input","type":"text","name":"name","placeholder":""}],"headers":["姓名"],"sampleRow":{"姓名":"张三"}}`

// 幂等:同 requestId 第二次直接返回缓存,不重调模型、不重复扣分
func TestIdempotentReplay(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)

	do := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody))
		router.ServeHTTP(w, req)
		return w
	}
	w1 := do()
	if w1.Code != 200 {
		t.Fatalf("首次应 200,实际 %d: %s", w1.Code, w1.Body.String())
	}
	w2 := do()
	if w2.Code != 200 || w2.Body.String() != w1.Body.String() {
		t.Fatalf("重放应返回相同缓存,实际 %d: %s", w2.Code, w2.Body.String())
	}
	if hits != 1 {
		t.Fatalf("模型应只被调用 1 次,实际 %d", hits)
	}
	var ledgerCount int64
	db.Model(&model.CreditLedger{}).Where("user_id = ?", uid).Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("应只有 1 条扣费流水,实际 %d", ledgerCount)
	}
	if !strings.Contains(w1.Body.String(), `"charged":5`) {
		t.Fatalf("响应应含扣分信息: %s", w1.Body.String())
	}
}

// 缓存清除后旧 requestId → 410,绝不重调重扣
func TestIdempotencyExpired(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		chatOK(`{"mapping":[]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)

	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w1.Code != 200 {
		t.Fatalf("首次应 200: %s", w1.Body.String())
	}
	// 模拟 24h 清理
	db.Model(&model.AIRequest{}).Where("1=1").Update("response_cache", "")

	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w2.Code != 410 {
		t.Fatalf("应返回 410,实际 %d: %s", w2.Code, w2.Body.String())
	}
	if hits != 1 {
		t.Fatalf("清除缓存后不得重调模型,调用次数 %d", hits)
	}
}

// 模型输出不合规:同链重试一次,仍失败 → 422 且不扣分
func TestInvalidOutputNoCharge(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		chatOK("这不是 JSON")(w, r)
	}))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 422 {
		t.Fatalf("应 422,实际 %d: %s", w.Code, w.Body.String())
	}
	if hits != 2 {
		t.Fatalf("应重试一次共 2 调,实际 %d", hits)
	}
	var ledgerCount int64
	db.Model(&model.CreditLedger{}).Where("user_id = ?", uid).Count(&ledgerCount)
	if ledgerCount != 0 {
		t.Fatalf("不应扣分,流水 %d 条", ledgerCount)
	}
}

// 解析失败重试后,被丢弃的那次调用一样烧了上游 token:
// 只记最后一次会让成本核算凭空少一半,单价复盘与幻觉率分母全跟着错
func TestRetryTokensAccumulated(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			chatOK("这不是 JSON")(w, r) // 第一次废掉,触发同链重试
		} else {
			chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
		}
	}))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 200 {
		t.Fatalf("重试后应成功,实际 %d: %s", w.Code, w.Body.String())
	}
	var ar model.AIRequest
	if err := db.First(&ar, "request_id = ?", "11111111-1111-4111-8111-111111111111").Error; err != nil {
		t.Fatal(err)
	}
	// chatOK 每次固定 10 输入 / 5 输出,两次调用应记 20 / 10
	if ar.InputTokens != 20 || ar.OutputTokens != 10 {
		t.Fatalf("两次调用的 token 都要计入,期望 20/10,实际 %d/%d", ar.InputTokens, ar.OutputTokens)
	}
}

// 两次都解析失败(422 不扣分)时,已经发生的 token 成本同样要记全
func TestFailedRetryTokensAccumulated(t *testing.T) {
	upstream := httptest.NewServer(chatOK("这不是 JSON"))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 422 {
		t.Fatalf("应 422,实际 %d: %s", w.Code, w.Body.String())
	}
	var ar model.AIRequest
	if err := db.First(&ar, "request_id = ?", "11111111-1111-4111-8111-111111111111").Error; err != nil {
		t.Fatal(err)
	}
	if ar.InputTokens != 20 || ar.OutputTokens != 10 {
		t.Fatalf("失败路径也要记全成本,期望 20/10,实际 %d/%d", ar.InputTokens, ar.OutputTokens)
	}
}

// 方案费防重:同一表单×同一列集(不同 requestId)只扣一次——计费键由服务端派生,不依赖客户端
func TestPlanFeeChargedOncePerScheme(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)

	body1 := strings.Replace(matchBody, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body1)))
	if w1.Code != 200 || w2.Code != 200 {
		t.Fatalf("两次应都 200,实际 %d/%d: %s", w1.Code, w2.Code, w2.Body.String())
	}
	if !strings.Contains(w1.Body.String(), `"charged":5`) {
		t.Fatalf("首次应扣方案费: %s", w1.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"charged":0`) {
		t.Fatalf("同方案第二次应免单: %s", w2.Body.String())
	}
	var ledgerCount int64
	db.Model(&model.CreditLedger{}).Where("user_id = ? AND delta < 0", uid).Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("应只有 1 条消费流水,实际 %d", ledgerCount)
	}
}

// dynamic/repair 语境的映射免费(录入中动态字段与自愈重建由月费覆盖)
func TestMatchContextDynamicRepairFree(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)

	for i, ctx := range []string{"dynamic", "repair"} {
		reqID := "33333333-3333-4333-8333-33333333333" + string(rune('0'+i))
		body := strings.Replace(matchBody, "11111111-1111-4111-8111-111111111111", reqID, 1)
		body = strings.Replace(body, `"fields"`, `"context":"`+ctx+`","fields"`, 1)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("context=%s 应 200,实际 %d: %s", ctx, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"charged":0`) {
			t.Fatalf("context=%s 应免费: %s", ctx, w.Body.String())
		}
	}
	var ledgerCount int64
	db.Model(&model.CreditLedger{}).Where("user_id = ? AND delta < 0", uid).Count(&ledgerCount)
	if ledgerCount != 0 {
		t.Fatalf("不应有消费流水,实际 %d", ledgerCount)
	}
}

// AI 生成格:同一行同一字段重新生成免费(计费键 = 行内容指纹 + 字段)
func TestGenerateFieldRegenFree(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`这是生成的内容`))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)

	genBody := func(reqID string) string {
		return `{"requestId":"` + reqID + `","field":{"index":0,"label":"备注","tag":"input","type":"text","name":"remark","placeholder":""},"prompt":"写一句总结","row":{"姓名":"张三","金额":"100"}}`
	}
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, httptest.NewRequest("POST", "/ai/generate-field", strings.NewReader(genBody("44444444-4444-4444-8444-444444444444"))))
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, httptest.NewRequest("POST", "/ai/generate-field", strings.NewReader(genBody("55555555-5555-4555-8555-555555555555"))))
	if w1.Code != 200 || w2.Code != 200 {
		t.Fatalf("两次应都 200,实际 %d/%d: %s", w1.Code, w2.Code, w2.Body.String())
	}
	if !strings.Contains(w1.Body.String(), `"charged":1`) {
		t.Fatalf("首次生成应扣 1 分: %s", w1.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"charged":0`) {
		t.Fatalf("同格重新生成应免单: %s", w2.Body.String())
	}
	var ledgerCount int64
	db.Model(&model.CreditLedger{}).Where("user_id = ? AND capability = ? AND delta < 0", uid, "generate_field").Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("应只有 1 条消费流水,实际 %d", ledgerCount)
	}
}

// 一列都没匹配上的映射不收方案费,也不占用计费组;之后同方案匹配成功时照常收费
func TestAllNullMappingFree(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			chatOK(`{"mapping":[{"fieldIndex":0,"column":null}]}`)(w, r) // 全空映射
		} else {
			chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r) // 匹配成功
		}
	}))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)

	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w1.Code != 200 || !strings.Contains(w1.Body.String(), `"charged":0`) {
		t.Fatalf("全空映射应免单,实际 %d: %s", w1.Code, w1.Body.String())
	}
	var ledgerCount int64
	db.Model(&model.CreditLedger{}).Where("user_id = ? AND delta < 0", uid).Count(&ledgerCount)
	if ledgerCount != 0 {
		t.Fatalf("全空映射不应产生流水,实际 %d", ledgerCount)
	}

	// 同方案再次匹配(新 requestId),这次成功:应正常收方案费(计费组未被全空结果占用)
	body2 := strings.Replace(matchBody, "11111111-1111-4111-8111-111111111111", "66666666-6666-4666-8666-666666666666", 1)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body2)))
	if w2.Code != 200 || !strings.Contains(w2.Body.String(), `"charged":5`) {
		t.Fatalf("匹配成功应正常收费,实际 %d: %s", w2.Code, w2.Body.String())
	}
}

// 生成内容超长(注入顶格产出)按无效输出处理:422 且不扣分
func TestGenerateFieldOversizeRejected(t *testing.T) {
	long := strings.Repeat("啰", 2500)
	upstream := httptest.NewServer(chatOK(long))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)

	body := `{"requestId":"77777777-7777-4777-8777-777777777777","field":{"index":0,"label":"备注","tag":"input","type":"text","name":"remark","placeholder":""},"prompt":"写一句总结","row":{"姓名":"张三"}}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/generate-field", strings.NewReader(body)))
	if w.Code != 422 {
		t.Fatalf("超长输出应 422,实际 %d: %s", w.Code, w.Body.String())
	}
	var ledgerCount int64
	db.Model(&model.CreditLedger{}).Where("user_id = ? AND delta < 0", uid).Count(&ledgerCount)
	if ledgerCount != 0 {
		t.Fatalf("不应扣分,流水 %d 条", ledgerCount)
	}
}

// 幻觉列名被服务端过滤
func TestHallucinationFiltered(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[{"fieldIndex":0,"column":"不存在的列"},{"fieldIndex":99,"column":"姓名"}]}`))
	defer upstream.Close()
	router, _, _ := setupGateway(t, upstream)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 200 {
		t.Fatalf("应 200: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"mapping":[]`) {
		t.Fatalf("幻觉映射应全被过滤: %s", w.Body.String())
	}
}

// 扣费事务通用失败(DB 故障等):必须落状态、释租约、留根因。
// 否则请求卡 pending 90 秒(同 requestId 一律 409),而模型已经调过、成本已经发生——
// 插件重试→再调模型→再失败,就成了无限白烧上游费用的循环。
func TestChargeFailureFinishesRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)

	// 制造扣费事务内的通用故障:流水表没了,Charge 必然报错(非余额不足、非无订阅)
	if err := db.Exec(`DROP TABLE credit_ledgers`).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 500 {
		t.Fatalf("扣费事务失败应 500,实际 %d: %s", w.Code, w.Body.String())
	}

	var ar model.AIRequest
	if err := db.First(&ar, "request_id = ?", "11111111-1111-4111-8111-111111111111").Error; err != nil {
		t.Fatal(err)
	}
	if ar.Status == model.AIReqPending {
		t.Fatal("扣费失败后不应仍是 pending:会卡满整个租约期,同 requestId 重试一律 409")
	}
	if ar.LeaseExpiresAt.After(time.Now()) {
		t.Fatalf("租约应已释放,实际到期于 %v", ar.LeaseExpiresAt)
	}
}

// 截断输出必须判无效:纯文本能力(生成字段内容)没有"解析失败"这道保险,
// 半句话会被当成完整答案填进业务系统
func TestTruncatedOutputRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"这段文案写到一半就"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":1000}}`))
	}))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)

	body := `{"requestId":"22222222-2222-4222-8222-222222222222","field":{"index":0,"label":"描述","tag":"input","type":"text","name":"desc","placeholder":""},"prompt":"写个描述","row":{"名称":"甲"}}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/generate-field", strings.NewReader(body)))

	if w.Code != 422 {
		t.Fatalf("截断输出应 422 不扣分,实际 %d: %s", w.Code, w.Body.String())
	}
	var cnt int64
	db.Model(&model.CreditLedger{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("截断输出不应扣分,实际有 %d 条流水", cnt)
	}
}

// 租约被接管后,原请求跑完也不得落账:否则会二次扣费并覆盖接管者的结果。
// 当前两个计费能力恰好都有计费组防重掩护,但任何无计费组的能力一旦定价,双扣即成立。
func TestStaleLeaseCannotSettle(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 卡住首个请求,模拟"跑得比租约还久"
		chatOK(`{"content":"甲"}`)(w, r)
	}))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)

	const body = `{"requestId":"33333333-3333-4333-8333-333333333333","field":{"index":0,"label":"描述","tag":"input","type":"text","name":"d","placeholder":""},"prompt":"写","row":{"名称":"甲"}}`
	done := make(chan int)
	go func() {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/generate-field", strings.NewReader(body)))
		done <- w.Code
	}()

	// 等首个请求把 pending 行写进去,然后模拟"租约到期被另一请求接管":换掉租约凭证
	var ar model.AIRequest
	for i := 0; i < 100; i++ {
		if err := db.First(&ar, "request_id = ?", "33333333-3333-4333-8333-333333333333").Error; err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ar.ID == 0 {
		t.Fatal("首个请求应已写入 pending 行")
	}
	if err := db.Model(&model.AIRequest{}).Where("id = ?", ar.ID).
		Update("lease_token", "接管者的新凭证").Error; err != nil {
		t.Fatal(err)
	}
	close(release)

	if code := <-done; code != 409 && code != 500 {
		t.Fatalf("租约已被接管,原请求不应落账成功,实际 %d", code)
	}
	var cnt int64
	db.Model(&model.CreditLedger{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("租约已易主,原请求不得扣费,实际有 %d 条流水", cnt)
	}
	db.First(&ar, ar.ID)
	if ar.Status == model.AIReqOK {
		t.Fatal("租约已易主,原请求不得把状态落成 ok(会覆盖接管者的结果)")
	}
}

// 余额不足:402 且带上可用/所需,不调模型,租约当场释放(买了分就能用原 requestId 重试)
func TestInsufficientCredits402(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		chatOK(`{"mapping":[]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)
	// 单价抬到超过桶里的 100 分
	db.Model(&model.CapabilityPrice{}).Where("capability = ?", "match_columns").Update("credits", 500)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 402 {
		t.Fatalf("余额不足应 402,实际 %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"INSUFFICIENT_CREDITS", `"available":100`, `"required":500`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("响应应含 %s,实际 %s", want, w.Body.String())
		}
	}
	if hits != 0 {
		t.Fatalf("余额不足不该调模型,实际 %d 次", hits)
	}
	var ar model.AIRequest
	db.First(&ar, "request_id = ?", "11111111-1111-4111-8111-111111111111")
	if ar.LeaseExpiresAt.After(time.Now()) {
		t.Fatal("余额不足应立即释放租约,否则用户买了分还要干等 90 秒才能重试")
	}
}

// 能力被停用:403 CAPABILITY_DISABLED,不调模型
func TestCapabilityDisabled403(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		chatOK(`{"mapping":[]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)
	db.Model(&model.CapabilityPrice{}).Where("capability = ?", "match_columns").Update("enabled", false)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "CAPABILITY_DISABLED") {
		t.Fatalf("停用能力应 403 CAPABILITY_DISABLED,实际 %d: %s", w.Code, w.Body.String())
	}
	if hits != 0 {
		t.Fatalf("停用能力不该调模型,实际 %d 次", hits)
	}
}

// 订阅断档:403 NO_ACTIVE_SUBSCRIPTION(加油包只加分不给资格,门禁只认 base/trial)
func TestNoActiveSubscription403(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{"mapping":[]}`))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)
	// 底座过期,只剩一个加油包桶:有余额但没资格
	db.Model(&model.UserSubscription{}).Where("user_id = ?", uid).
		Update("ends_at", time.Now().Add(-time.Minute))
	db.Create(&model.UserSubscription{
		UserID: uid, PlanID: 2, PlanType: model.PlanTypePack, AmountTotal: 1000,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "NO_ACTIVE_SUBSCRIPTION") {
		t.Fatalf("无有效订阅应 403 NO_ACTIVE_SUBSCRIPTION,实际 %d: %s", w.Code, w.Body.String())
	}
}

// 上游全挂(503):网关层统一翻成 AI_UNAVAILABLE,不扣分,请求落 upstream_error 可原地重跑
func TestUpstreamDownGives503(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("service unavailable"))
	}))
	defer upstream.Close()
	router, db, _ := setupGateway(t, upstream)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(matchBody)))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "AI_UNAVAILABLE") {
		t.Fatalf("上游全挂应 503 AI_UNAVAILABLE,实际 %d: %s", w.Code, w.Body.String())
	}
	var cnt int64
	db.Model(&model.CreditLedger{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("上游故障不应扣分,实际 %d 条流水", cnt)
	}
	var ar model.AIRequest
	db.First(&ar, "request_id = ?", "11111111-1111-4111-8111-111111111111")
	if ar.Status != model.AIReqUpstreamError {
		t.Fatalf("应落 upstream_error(未扣费,允许原地重跑),实际 %s", ar.Status)
	}
}

// 无派生规则的能力必须忽略客户端自报的 billingGroupId:
// 用户能从自己的流水里读到历史已扣费的 group id,回放它就能白嫖模型调用
func TestClientBillingGroupIgnored(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(chatOK(`{"submitSelector":null,"openSelector":null,"advanceSelector":null}`)))
	defer upstream.Close()
	router, db, uid := setupGateway(t, upstream)
	db.Create(&model.CapabilityPrice{Capability: "analyze_form", Credits: 3, Enabled: true})
	// 伪造一条"该组已收过费"的锚点:若网关采信客户端声明,本次就会被判免单
	db.Create(&model.BillingGroupCharge{UserID: uid, BillingGroupID: "偷来的组", Capability: "x", Price: 3})

	body := `{"requestId":"44444444-4444-4444-8444-444444444444","billingGroupId":"偷来的组","fields":[],"buttons":[{"selector":"#s","text":"保存"}],"outerButtons":[]}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/analyze-form", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("应正常处理,实际 %d: %s", w.Code, w.Body.String())
	}

	var ar model.AIRequest
	db.First(&ar, "request_id = ?", "44444444-4444-4444-8444-444444444444")
	if ar.BillingGroupID != "" {
		t.Fatalf("无派生规则的能力应清空计费组,实际 %q", ar.BillingGroupID)
	}
	if ar.Credits != 3 {
		t.Fatalf("应照常扣费 3,实际 %d(客户端自报的计费组骗到了免单)", ar.Credits)
	}
}
