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
	content := "version: v1\nsystem: |\n  测试系统提示词\nuser: |\n  {{json .Req.Headers}}\n"
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
		UserID: u.ID, PlanID: 1, AmountTotal: 100,
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Status: model.SubStatusActive,
	})
	db.Create(&model.AIDefault{ID: 1, Model: "m"})
	db.Create(&model.CapabilityPrice{Capability: "match_columns", Credits: 5, Enabled: true})
	db.Create(&model.AIUpstream{Name: "t1", BaseURL: upstream.URL, APIKey: "k", Enabled: true})

	dir := t.TempDir()
	writePrompt(t, dir, "match_columns")
	prompts, err := LoadPrompts(dir, []string{"match_columns"})
	if err != nil {
		t.Fatal(err)
	}
	g := NewGateway(db, NewCaller(db), prompts)

	var spec Spec
	for _, sp := range Specs() {
		if sp.Name == "match_columns" {
			spec = sp
		}
	}
	r := gin.New()
	r.POST("/ai/match-columns", func(c *gin.Context) {
		c.Set("userID", u.ID)
		c.Next()
	}, g.Handler(spec))
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
