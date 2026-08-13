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

	for _, col := range []string{"temperature", "max_tokens"} {
		if db.Migrator().HasColumn(&CapabilityPrice{}, col) {
			t.Fatalf("旧列 %s 应已删除", col)
		}
	}
	var p CapabilityPrice
	if err := db.First(&p, "capability = ?", "analyze_form").Error; err != nil {
		t.Fatal(err)
	}
	if p.Model != "" || p.Temperature != nil || p.MaxTokens != nil {
		t.Fatalf("旧占位配置应重置为用默认,实际 %+v", p)
	}
	if p.Credits != 5 || !p.Enabled {
		t.Fatalf("单价与启用状态应保留,实际 %+v", p)
	}

	// 再迁一次(正常启动路径)应无副作用
	if err := Migrate(db); err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
}
