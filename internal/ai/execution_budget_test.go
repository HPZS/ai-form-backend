package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HPZS/ai-form-backend/internal/model"
)

type dispatchFailureTransport struct{ wrote bool }

func (f dispatchFailureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.wrote {
		httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
	}
	return nil, errors.New("模拟发送结果不明，未取得上游回执")
}

func TestFailedTransportKeepsReservationWithoutInventingDispatch(t *testing.T) {
	for _, wrote := range []bool{false, true} {
		t.Run(fmt.Sprint(wrote), func(t *testing.T) {
			upstream := httptest.NewServer(chatOK(`{}`))
			defer upstream.Close()
			_, db, uid, task, _ := v2Fixture(t, upstream)
			g := &Gateway{db: db}
			r := model.AIRequest{UserID: uid, TaskID: task, RequestID: uuid.NewString(), Capability: "match_columns", Status: model.AIReqPending, LeaseToken: uuid.NewString(), UsageDetails: "[]"}
			if err := db.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			caller := NewCaller(db)
			caller.http.Transport = dispatchFailureTransport{wrote: wrote}
			if _, err := caller.Call(g.withExecutionBudget(context.Background(), &r), "match_columns", nil); err == nil {
				t.Fatal("传输失败不得返回成功")
			}
			var budget model.AIExecutionBudget
			if err := db.Where("user_id = ? AND work_id = ?", uid, task).First(&budget).Error; err != nil {
				t.Fatal(err)
			}
			if budget.Attempts != 1 || budget.ReservedTokens <= 0 || budget.ReservedWaitMs != 0 || requestUsageComplete(&r) {
				t.Fatalf("无回执不能释放额度或标为免费: %+v usage=%s", budget, r.UsageDetails)
			}
			count := requestModelCalls(&r)
			if (count != nil) != wrote || (count != nil && *count != 1) {
				t.Fatalf("仅有完整写入证据才能确认调用: %s", r.UsageDetails)
			}
		})
	}
}

func TestCancelBetweenReservationAndHTTPDoesNotConsumeAttempt(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{}`)(w, r)
	}))
	defer upstream.Close()
	_, db, uid, task, _ := v2Fixture(t, upstream)
	g := &Gateway{db: db}
	r := model.AIRequest{UserID: uid, TaskID: task, RequestID: uuid.NewString(), Capability: "match_columns", Status: model.AIReqPending, PolicyVersion: "v2", BillingMode: "included", LeaseToken: uuid.NewString(), UsageDetails: "[]"}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := g.withExecutionBudget(parent, &r)
	start := ctx.Value(modelAttemptKey{}).(startModelAttempt)
	ctx = context.WithValue(ctx, modelAttemptKey{}, startModelAttempt(func(parent context.Context, up model.AIUpstream, params callParams, messages []ChatMessage) (context.Context, func(AttemptUsage) error, error) {
		attempt, finish, err := start(parent, up, params, messages)
		cancel() // 精确覆盖预留已提交、HTTP 尚未分发的窗口。
		return attempt, finish, err
	}))
	_, err := NewCaller(db).Call(ctx, "match_columns", []ChatMessage{{Role: "user", Content: "测试取消"}})
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("取消后不得发送: calls=%d err=%v", calls.Load(), err)
	}
	var budget model.AIExecutionBudget
	if err := db.Where("user_id = ? AND work_id = ?", uid, task).First(&budget).Error; err != nil {
		t.Fatal(err)
	}
	if budget.Attempts != 0 || budget.Groups[r.RequestID] != 0 || budget.ReservedTokens != 0 || budget.ReservedWaitMs != 0 || budget.KnownTokens != 0 {
		t.Fatalf("确认未发送必须释放预留: %+v", budget)
	}
	if count := requestModelCalls(&r); count == nil || *count != 0 || !requestUsageComplete(&r) {
		t.Fatalf("未发送不能报告为调用或未知用量: %s", r.UsageDetails)
	}
}

func TestV2FailoverAndProtocolRepairShareAttemptLimit(t *testing.T) {
	var calls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); chatOK(`{"invalid":true}`)(w, r) }))
	defer second.Close()
	router, db, _, task, auth := v2Fixture(t, first)
	if err := db.Create(&model.AIUpstream{Name: "second", BaseURL: second.URL, APIKey: "test", Enabled: true, SortOrder: 1}).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(v2Body(t, matchBody, task, auth))))
	if calls.Load() != 2 || !strings.Contains(w.Body.String(), "AI_RECOVERY_BUDGET_EXHAUSTED") {
		t.Fatalf("切换上游与协议修复没有共享上限: calls=%d response=%s", calls.Load(), w.Body.String())
	}
}

func TestV2ConcurrentRequestsShareLastAttempt(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, db, uid, task, auth := v2Fixture(t, upstream)
	// SQLite 无行级锁；此处验证事务内预留，生产 PostgreSQL 的竞争另行验收。
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	if err := db.Create(&model.AIExecutionBudget{UserID: uid, WorkID: task, MaxAttempts: 1, MaxTokens: ExecutionMaxTokens, MaxWaitMs: ExecutionMaxWaitMs}).Error; err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		var body map[string]any
		if err := json.Unmarshal([]byte(v2Body(t, matchBody, task, auth)), &body); err != nil {
			t.Fatal(err)
		}
		body["requestId"] = uuid.NewString()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(string(raw))))
			results <- w.Code
		}()
	}
	wait.Wait()
	close(results)
	statuses := map[int]int{}
	for status := range results {
		statuses[status]++
	}
	if calls.Load() != 1 || statuses[200] != 1 || statuses[409] != 1 {
		t.Fatalf("最后额度被并发重复消费: calls=%d statuses=%v", calls.Load(), statuses)
	}
}

func TestV2FailedAttemptKeepsUnknownTokenReservation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer upstream.Close()
	router, db, uid, task, auth := v2Fixture(t, upstream)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(v2Body(t, matchBody, task, auth))))
	var budget model.AIExecutionBudget
	if err := db.Where("user_id = ? AND work_id = ?", uid, task).First(&budget).Error; err != nil {
		t.Fatal(err)
	}
	if budget.Attempts != 1 || budget.ReservedTokens <= 0 || budget.KnownTokens != 0 || budget.ReservedWaitMs != 0 {
		t.Fatalf("超时或失败没有 usage 时不能释放 token 预留: %+v", budget)
	}
}

func TestV2FirstRequestOverTokenBudgetIsExplainableAndDoesNotDispatch(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"姓名"}]}`)(w, r)
	}))
	defer upstream.Close()
	_, db, uid, task, _ := v2Fixture(t, upstream)
	g := &Gateway{db: db}
	r := model.AIRequest{UserID: uid, TaskID: task, RequestID: uuid.NewString(), Capability: "match_columns", Status: model.AIReqPending, PolicyVersion: "v2", BillingMode: "included", LeaseToken: uuid.NewString(), UsageDetails: "[]"}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	start := g.withExecutionBudget(context.Background(), &r).Value(modelAttemptKey{}).(startModelAttempt)
	_, _, err := start(context.Background(), model.AIUpstream{}, callParams{MaxTokens: int(ExecutionMaxTokens) + 1}, nil)
	if executionBudgetCode(err) != "AI_TASK_BUDGET_EXHAUSTED" {
		t.Fatalf("未拒绝过量预留: %v", err)
	}
	g.failV2(&r, "execution_failed", err)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	g.respondV2(c, &r, false, true)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"error":"AI_TASK_BUDGET_EXHAUSTED"`) || !strings.Contains(w.Body.String(), `"executionBudget"`) || calls.Load() != 0 {
		t.Fatalf("首次拒绝必须保留可查询预算且不分发: code=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	var budget model.AIExecutionBudget
	if err := db.Where("user_id = ? AND work_id = ?", uid, task).First(&budget).Error; err != nil {
		t.Fatal(err)
	}
	if budget.Attempts != 0 || budget.ReservedTokens != 0 || budget.ReservedWaitMs != 0 {
		t.Fatalf("未分发不能消费额度: %+v", budget)
	}
}

func TestExecutionBudgetExtensionRetainsEveryConfirmationAndCounters(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{}`))
	defer upstream.Close()
	router, db, uid, task, _ := v2Fixture(t, upstream)
	g := &Gateway{db: db}
	router.POST("/budgets/:id/extend", func(c *gin.Context) { c.Set("userID", uid); c.Next() }, g.ExtendExecutionBudget)
	budget := model.AIExecutionBudget{UserID: uid, WorkID: task, Attempts: 7, KnownTokens: 100, ReservedTokens: 200, WaitMs: 300, MaxAttempts: ExecutionMaxAttempts, MaxTokens: ExecutionMaxTokens, MaxWaitMs: ExecutionMaxWaitMs, Groups: map[string]int{"group": 2}}
	if err := db.Create(&budget).Error; err != nil {
		t.Fatal(err)
	}
	first, second := uuid.NewString(), uuid.NewString()
	run := func(id string, revision int, group, confirmation string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(map[string]any{"requestId": id, "expectedRevision": revision, "groupId": group, "confirmation": confirmation})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/budgets/"+task+"/extend", strings.NewReader(string(raw))))
		return w
	}
	if w := run(first, 0, "group", ""); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	for _, input := range []struct {
		id       string
		revision int
		group    string
	}{{first, 0, "group"}, {second, 1, ""}, {first, 0, "group"}} {
		if w := run(input.id, input.revision, input.group, "increase-ai-budget"); w.Code != 200 {
			t.Fatalf("原确认重放必须成功，不重复扩额: %d %s", w.Code, w.Body.String())
		}
	}
	if w := run(second, 1, "group", "increase-ai-budget"); w.Code != 409 {
		t.Fatalf("同 ID 改变扩额内容必须冲突: %d %s", w.Code, w.Body.String())
	}
	if w := run(uuid.NewString(), 0, "", "increase-ai-budget"); w.Code != 409 {
		t.Fatalf("过期版本不能扩额: %d %s", w.Code, w.Body.String())
	}
	if err := db.First(&budget, budget.ID).Error; err != nil {
		t.Fatal(err)
	}
	if budget.Extensions != 2 || budget.MaxAttempts != ExecutionMaxAttempts*3 || budget.GroupExtra["group"] != 2 || budget.Attempts != 7 || budget.KnownTokens != 100 || budget.ReservedTokens != 200 || budget.WaitMs != 300 {
		t.Fatalf("扩额丢失累计消耗、恢复组或重复增加: %+v", budget)
	}
}

func TestExecutionBudgetAdvertisesAndEnforcesExtensionOffer(t *testing.T) {
	upstream := httptest.NewServer(chatOK(`{}`))
	defer upstream.Close()
	router, db, uid, task, _ := v2Fixture(t, upstream)
	g := &Gateway{db: db}
	router.GET("/budgets/:id", func(c *gin.Context) { c.Set("userID", uid) }, g.QueryExecutionBudget)
	router.POST("/budgets/:id/extend", func(c *gin.Context) { c.Set("userID", uid) }, g.ExtendExecutionBudget)
	budget := model.AIExecutionBudget{UserID: uid, WorkID: task, Attempts: 7, KnownTokens: 100, ReservedTokens: 200, MaxAttempts: ExecutionMaxAttempts, MaxTokens: ExecutionMaxTokens, MaxWaitMs: ExecutionMaxWaitMs}
	if err := db.Create(&budget).Error; err != nil {
		t.Fatal(err)
	}
	query := func() map[string]any {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/budgets/"+task, nil))
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal(w.Body.String())
		}
		return body
	}
	post := func(id string, revision int, offer any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(map[string]any{"requestId": id, "expectedRevision": revision, "expectedOffer": offer, "confirmation": "increase-ai-budget"})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/budgets/"+task+"/extend", strings.NewReader(string(raw))))
		return w
	}
	first := uuid.NewString()
	var originalOffer any
	for revision := 0; revision < 3; revision++ {
		view := query()
		offer, ok := view["extensionOffer"].(map[string]any)
		if !ok || view["remainingExtensions"] != float64(3-revision) {
			t.Fatalf("没有实际可追加政策: %+v", view)
		}
		if offer["attempts"] != float64(ExecutionMaxAttempts) || offer["tokens"] != float64(ExecutionMaxTokens) || offer["waitMs"] != float64(ExecutionMaxWaitMs) || offer["recoveryAttempts"] != float64(RecoveryMaxAttempts) {
			t.Fatalf("展示政策与实际增量不一致: %+v", offer)
		}
		if revision == 0 {
			originalOffer = offer
			stale := map[string]any{"attempts": 1, "tokens": 1, "waitMs": 1, "recoveryAttempts": 1}
			if w := post(uuid.NewString(), revision, stale); w.Code != 409 {
				t.Fatalf("不同于用户看到的额度不能自动增加: %s", w.Body.String())
			}
		}
		id := uuid.NewString()
		if revision == 0 {
			id = first
		}
		if w := post(id, revision, offer); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	view := query()
	if view["remainingExtensions"] != float64(0) || view["extensionOffer"] != nil {
		t.Fatalf("达到上限后不能继续提供追加选项: %+v", view)
	}
	if w := post(uuid.NewString(), 3, originalOffer); w.Code != 409 {
		t.Fatalf("第四次追加必须停止: %s", w.Body.String())
	}
	if w := post(first, 0, originalOffer); w.Code != 200 {
		t.Fatalf("原确认查询不得因后来到达上限失败: %s", w.Body.String())
	}
	if w := post(first, 0, nil); w.Code != 409 {
		t.Fatalf("原确认的政策不能在重放时被删掉: %s", w.Body.String())
	}
	if err := db.First(&budget, budget.ID).Error; err != nil {
		t.Fatal(err)
	}
	if budget.Extensions != 3 || budget.MaxAttempts != ExecutionMaxAttempts*4 || budget.Attempts != 7 || budget.KnownTokens != 100 || budget.ReservedTokens != 200 {
		t.Fatalf("额度或累计消耗被错误修改: %+v", budget)
	}
}

type budgetDeadlineTransport struct{ cancel context.CancelFunc }

func (f budgetDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
	if f.cancel != nil {
		f.cancel()
	}
	<-r.Context().Done()
	return nil, r.Context().Err()
}
func TestExecutionWaitDeadlineRetainsBudgetCauseAndUsage(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			upstream := httptest.NewServer(chatOK(`{}`))
			defer upstream.Close()
			_, db, uid, task, _ := v2Fixture(t, upstream)
			g := &Gateway{db: db}
			budget := model.AIExecutionBudget{UserID: uid, WorkID: task, MaxAttempts: ExecutionMaxAttempts, MaxTokens: ExecutionMaxTokens, MaxWaitMs: ExecutionMaxWaitMs, WaitMs: ExecutionMaxWaitMs - 100}
			if err := db.Create(&budget).Error; err != nil {
				t.Fatal(err)
			}
			r := model.AIRequest{UserID: uid, TaskID: task, RequestID: uuid.NewString(), Capability: "match_columns", Status: model.AIReqPending, PolicyVersion: "v2", BillingMode: "included", LeaseToken: uuid.NewString(), UsageDetails: "[]"}
			if err := db.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := budgetDeadlineTransport{}
			if cancelled {
				transport.cancel = cancel
			}
			caller := NewCaller(db)
			caller.http.Transport = transport
			_, err := caller.Call(g.withExecutionBudget(parent, &r), "match_columns", nil)
			expected := "AI_TASK_BUDGET_EXHAUSTED"
			if cancelled {
				expected = "AI_CANCELLED"
			}
			if aiFailureCode(err) != expected {
				t.Fatalf("预算截止与用户取消不得伪装上游故障: expected=%s err=%v code=%s", expected, err, aiFailureCode(err))
			}
			g.failV2(&r, "execution_failed", err)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			g.respondV2(c, &r, false, true)
			if w.Code != 409 || !strings.Contains(w.Body.String(), expected) || strings.Contains(w.Body.String(), `"executionBudget"`) == cancelled {
				t.Fatalf("缺少准确终态与预算解释: %d %s", w.Code, w.Body.String())
			}
			if err := db.First(&budget, budget.ID).Error; err != nil {
				t.Fatal(err)
			}
			if budget.Attempts != 1 || budget.ReservedTokens <= 0 || budget.ReservedWaitMs != 0 || requestUsageComplete(&r) {
				t.Fatalf("已分发无 usage 不能记零或释放预留: %+v %s", budget, r.UsageDetails)
			}
		})
	}
}
