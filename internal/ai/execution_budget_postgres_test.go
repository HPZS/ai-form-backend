package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 只连接本机专用验收库，每次创建并保留独立 schema；不删除既有表或业务数据。
func TestPostgresExecutionBudgetMigrationAndConcurrency(t *testing.T) {
	dsn := os.Getenv("AIFORM_EXECUTION_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置本机执行预算验收库 AIFORM_EXECUTION_TEST_DSN")
	}
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "127.0.0.1" || config.Database != "aiform_execution_test" {
		t.Fatal("仅允许本机专用 aiform_execution_test 库")
	}
	options := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	admin, err := gorm.Open(postgres.Open(dsn), options)
	if err != nil {
		t.Fatal(err)
	}
	closeDB := func(db *gorm.DB) {
		pool, err := db.DB()
		if err != nil {
			t.Error(err)
			return
		}
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { closeDB(admin) })
	schema := fmt.Sprintf("execution_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeDB(db) })
	t.Logf("保留验收 schema=%s", schema)
	// 模拟已有 API21 请求和自定义能力价格；新预算/取消表及请求字段尚不存在。
	for _, statement := range []string{
		`CREATE TABLE ai_requests (id bigserial PRIMARY KEY, user_id bigint, request_id varchar(36), task_id varchar(36), capability varchar(64), policy_version varchar(32), status varchar(32), request_digest varchar(80), response_cache text, unit_price bigint, reserved_credits bigint, lease_token varchar(36), usage_details text)`,
		`INSERT INTO ai_requests(user_id,request_id,task_id,capability,policy_version,status,request_digest,response_cache,unit_price,reserved_credits,lease_token,usage_details) VALUES(7,'original-request','original-task','match_columns','per-call-v2','settlement_pending','h1:original','{"mapping":[]}',7,7,'original-lease','[{"status":"responded","inputTokens":12}]')`,
		`CREATE TABLE capability_prices (id bigserial PRIMARY KEY, capability varchar(64), billing_mode varchar(16), credits bigint, price_version bigint, model varchar(128), enabled boolean)`,
		`INSERT INTO capability_prices(capability,billing_mode,credits,price_version,model,enabled) VALUES('match_columns','per_call',7,9,'fixture',true)`,
		`CREATE TABLE ai_upstreams (id bigserial PRIMARY KEY, name varchar(64) NOT NULL, base_url varchar(255) NOT NULL, api_key varchar(255) NOT NULL, enabled boolean NOT NULL DEFAULT true, sort_order bigint NOT NULL DEFAULT 0, created_at timestamptz, updated_at timestamptz)`,
		`INSERT INTO ai_upstreams(id,name,base_url,api_key,enabled,sort_order) VALUES(7,'legacy-disabled','https://example.test/v1','test-only-key',false,12)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := model.Migrate(db); err != nil {
			t.Fatal(err)
		}
		var legacy model.AIUpstream
		if err := db.First(&legacy, 7).Error; err != nil {
			t.Fatal(err)
		}
		if legacy.Name != "legacy-disabled" || legacy.BaseURL != "https://example.test/v1" || legacy.APIKey != "test-only-key" || legacy.Enabled || legacy.SortOrder != 12 {
			t.Fatal("迁移不能改写旧上游配置")
		}
		if i == 0 {
			if legacy.ThinkingMode != "" || legacy.TokenLimitParameter != "" {
				t.Fatal("旧上游必须保留原请求行为")
			}
			if err := db.Model(&legacy).Updates(map[string]any{"thinking_mode": "disabled", "token_limit_parameter": "max_completion_tokens"}).Error; err != nil {
				t.Fatal(err)
			}
		} else if legacy.ThinkingMode != "disabled" || legacy.TokenLimitParameter != "max_completion_tokens" {
			t.Fatal("重复迁移不能覆盖显式生成设置")
		}
	}
	var original model.AIRequest
	if err := db.First(&original, "request_id = ?", "original-request").Error; err != nil {
		t.Fatal(err)
	}
	if original.Status != "settlement_pending" || original.RequestDigest != "h1:original" || original.ResponseCache != `{"mapping":[]}` || original.UnitPrice != 7 || original.ReservedCredits != 7 || original.LeaseToken != "original-lease" || original.WorkID != "" || original.RecoveryGroupID != "" || original.FailureCode != "" || original.UsageDetails != `[{"status":"responded","inputTokens":12}]` {
		t.Fatalf("迁移改写历史请求: %+v", original)
	}
	var price model.CapabilityPrice
	if err := db.First(&price, "capability = ?", "match_columns").Error; err != nil {
		t.Fatal(err)
	}
	if price.Credits != 7 || price.PriceVersion != 9 || price.Model != "fixture" {
		t.Fatalf("迁移改写能力价格: %+v", price)
	}
	u := model.User{Email: uuid.NewString() + "@test.local", Status: "active"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); chatOK(`{"mapping":[]}`)(w, r) }))
	defer upstream.Close()
	if err := db.Create(&model.AIUpstream{Name: "fixture", BaseURL: upstream.URL, APIKey: "fixture", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	work := uuid.NewString()
	b := model.AIExecutionBudget{UserID: u.ID, WorkID: work, MaxAttempts: 1, MaxTokens: ExecutionMaxTokens, MaxWaitMs: ExecutionMaxWaitMs}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	var requests [2]model.AIRequest
	for i := range requests {
		requests[i] = model.AIRequest{UserID: u.ID, WorkID: work, TaskID: uuid.NewString(), RequestID: uuid.NewString(), Status: model.AIReqPending, LeaseToken: uuid.NewString(), UsageDetails: "[]"}
		if err := db.Create(&requests[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	g := &Gateway{db: db}
	caller := NewCaller(db)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := caller.Call(g.withExecutionBudget(context.Background(), &requests[i]), "match_columns", []ChatMessage{{Role: "user", Content: "fixture"}})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, denied := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if executionBudgetCode(err) == "AI_TASK_BUDGET_EXHAUSTED" {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || succeeded != 1 || denied != 1 {
		t.Fatalf("最后额度被并发重复消费: calls=%d success=%d denied=%d", calls.Load(), succeeded, denied)
	}
	if err := db.First(&b, b.ID).Error; err != nil {
		t.Fatal(err)
	}
	if b.Attempts != 1 || b.ReservedTokens != 0 || b.ReservedWaitMs != 0 || b.KnownTokens <= 0 {
		t.Fatalf("实际用量未核销预留: %+v", b)
	}
}
