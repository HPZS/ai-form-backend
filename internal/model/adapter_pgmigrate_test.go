package model_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/subscription"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 仅允许本机专用 adapter_eval 库，在独有 schema 中模拟 API20，不接触 public 或生产库。
func TestAdapterMigrationOnPostgres(t *testing.T) {
	dsn := os.Getenv("PG_ADAPTER_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置本机 PG_ADAPTER_TEST_DSN")
	}
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.Database != "adapter_eval" || config.Host != "127.0.0.1" {
		t.Fatal("只允许127.0.0.1上的专用adapter_eval隔离测试库")
	}
	options := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	admin, err := gorm.Open(postgres.Open(dsn), options)
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := adminSQL.Close(); err != nil {
			t.Error(err)
		}
	})
	schema := fmt.Sprintf("adapter_api21_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("清理本测试schema失败: %v", err)
		}
	})
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), options)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := model.Migrate(db); err != nil {
		t.Fatal(err)
	}
	row := model.AIRequest{UserID: 1, RequestID: "fixture-admitted-v2", Capability: "generate_rule", PolicyVersion: "per-call-v2", RequestDigest: "h1:original", Status: "settlement_pending", ResponseCache: `{"code":"original"}`, UnitPrice: 7, ReservedCredits: 7, LeaseToken: "fixture-lease"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	price := model.CapabilityPrice{Capability: "generate_rule", BillingMode: "per_call", Credits: 7, PriceVersion: 9, Enabled: true, Model: "fixture-custom"}
	if err := db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"call_phase", "handoff_id", "compilation_id"} {
		if err := db.Migrator().DropColumn(&model.AIRequest{}, column); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := model.Migrate(db); err != nil {
			t.Fatal(err)
		}
		if err := subscription.SeedDefaults(db); err != nil {
			t.Fatal(err)
		}
	}
	var actual model.AIRequest
	if err := db.First(&actual, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if actual.RequestDigest != row.RequestDigest || actual.Status != row.Status || actual.ResponseCache != row.ResponseCache || actual.UnitPrice != 7 || actual.ReservedCredits != 7 || actual.LeaseToken != row.LeaseToken || actual.CallPhase != "" || actual.HandoffID != "" || actual.CompilationID != "" {
		t.Fatal("API21迁移改写了已准入V2请求")
	}
	var previous, added model.CapabilityPrice
	if err := db.First(&previous, "capability = ?", "generate_rule").Error; err != nil {
		t.Fatal(err)
	}
	if previous.BillingMode != price.BillingMode || previous.Credits != 7 || previous.PriceVersion != 9 || previous.Model != price.Model {
		t.Fatal("迁移改写已有能力配置")
	}
	if err := db.First(&added, "capability = ?", "compile_adapter").Error; err != nil {
		t.Fatal(err)
	}
	if added.BillingMode != "included" || added.Credits != 0 || added.PriceVersion != 1 || added.Model != "" || !added.Enabled {
		t.Fatal("新适配能力默认配置不独立")
	}
	duplicate := model.AIRequest{UserID: 1, RequestID: row.RequestID, Capability: "compile_adapter"}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("API21迁移破坏V2幂等唯一约束")
	}
}
