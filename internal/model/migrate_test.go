// ai-form-backend - AGPL-3.0
package model

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// legacyCapabilityPrice 旧版结构(temperature/max_tokens 直接必配):
// 用 GORM 自己建表,和真实旧库的来源完全一致。
type legacyCapabilityPrice struct {
	Capability  string  `gorm:"primaryKey;size:32"`
	Credits     int64   `gorm:"not null"`
	Enabled     bool    `gorm:"not null;default:true"`
	Model       string  `gorm:"size:64;not null;default:''"`
	Temperature float64 `gorm:"not null;default:0"`
	MaxTokens   int     `gorm:"not null;default:2000"`
	UpdatedAt   time.Time
}

func (legacyCapabilityPrice) TableName() string { return "capability_prices" }

// 旧结构升级:迁移后旧列删除、播种的占位模型名清空(语义变为"用全局默认"),单价保留。
func TestMigrateLegacyCapabilitySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&legacyCapabilityPrice{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&legacyCapabilityPrice{
		Capability: "analyze_form", Credits: 5, Enabled: true, Model: "gpt-4o-mini", MaxTokens: 2000,
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("旧库迁移失败: %v", err)
	}

	for _, col := range []string{"temperature", "max_tokens", "temperature_override", "max_tokens_override"} {
		if db.Migrator().HasColumn(&CapabilityPrice{}, col) {
			t.Fatalf("旧列 %s 应已删除", col)
		}
	}
	var p CapabilityPrice
	if err := db.First(&p, "capability = ?", "analyze_form").Error; err != nil {
		t.Fatal(err)
	}
	if p.Model != "" {
		t.Fatalf("旧占位模型名应清空(改用全局默认),实际 %+v", p)
	}
	if p.Credits != 5 || !p.Enabled {
		t.Fatalf("单价与启用状态应保留,实际 %+v", p)
	}

	// 再迁一次(正常启动路径)应无副作用
	if err := Migrate(db); err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
}

// 默认模型表曾被 GORM 蛇形化成 a_idefaults:迁移应改名为 ai_defaults 且数据保留。
func TestMigrateRenamesAIDefaultsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rename.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			sqlDB.Close()
		}
	})
	for _, sql := range []string{
		`CREATE TABLE a_idefaults (id INTEGER PRIMARY KEY, model TEXT NOT NULL DEFAULT '', updated_at DATETIME)`,
		`INSERT INTO a_idefaults (id, model) VALUES (1, 'my-model')`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	if db.Migrator().HasTable("a_idefaults") {
		t.Fatal("旧表 a_idefaults 应已改名")
	}
	var def AIDefault
	if err := db.First(&def, 1).Error; err != nil {
		t.Fatal(err)
	}
	if def.Model != "my-model" {
		t.Fatalf("改名后数据应保留,实际 %+v", def)
	}
}

// 老库升级:计费组防重锚点从流水行的部分唯一索引迁到独立表。
// 必须做到 ①历史已收费的组回填成锚点(防重不失效) ②旧索引丢弃(否则跨桶扣费依旧撞索引)。
func TestMigrateBillingGroupAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-billing.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			sqlDB.Close()
		}
	})
	// 造一个旧库:有流水表、旧的部分唯一索引,以及一条历史计费组消费
	if err := db.AutoMigrate(&CreditLedger{}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE UNIQUE INDEX uq_ledger_billing_group ON credit_ledgers(user_id, billing_group_id) WHERE billing_group_id <> '' AND delta < 0`,
		`INSERT INTO credit_ledgers (user_id, subscription_id, request_id, billing_group_id, capability, delta, price_snapshot, balance_after, created_at)
		 VALUES (7, 1, 'req-old', 'g-old', 'match_columns', -50, 50, 100, CURRENT_TIMESTAMP)`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	var anchor BillingGroupCharge
	if err := db.First(&anchor, "user_id = ? AND billing_group_id = ?", 7, "g-old").Error; err != nil {
		t.Fatalf("历史计费组应回填为锚点: %v", err)
	}
	if anchor.Price != 50 || anchor.RequestID != "req-old" {
		t.Fatalf("锚点内容应来自历史流水,实际 %+v", anchor)
	}
	// 旧索引必须已丢弃:同组第二条流水(跨桶场景)应能写入
	if err := db.Exec(`INSERT INTO credit_ledgers (user_id, subscription_id, request_id, billing_group_id, capability, delta, price_snapshot, balance_after, created_at)
		VALUES (7, 2, 'req-old', 'g-old', 'match_columns', -20, 50, 80, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatalf("旧索引应已丢弃,同组第二条流水应可写入: %v", err)
	}
	// 幂等:再迁一次不应重复回填
	if err := Migrate(db); err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
	var cnt int64
	db.Model(&BillingGroupCharge{}).Where("user_id = ? AND billing_group_id = ?", 7, "g-old").Count(&cnt)
	if cnt != 1 {
		t.Fatalf("锚点应只有一条,实际 %d", cnt)
	}
}
