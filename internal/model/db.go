// ai-form-backend - AGPL-3.0
package model

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Open 连接数据库。dsn 为空时用本地 sqlite(仅开发/测试)。
func Open(dsn string) (*gorm.DB, error) {
	var dial gorm.Dialector
	if dsn == "" {
		dial = sqlite.Open("aiform.db")
	} else {
		dial = postgres.Open(dsn)
	}
	db, err := gorm.Open(dial, &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if err := Migrate(db); err != nil {
		return nil, err
	}
	return db, nil
}

// OpenMemory 内存 sqlite,单元测试用。
func OpenMemory() (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	if err := Migrate(db); err != nil {
		return nil, err
	}
	return db, nil
}

func Migrate(db *gorm.DB) error {
	// 结构升级前置判断:旧版能力表直接带 temperature/max_tokens 列(必配),
	// 新版改为"全局默认 + 可空覆盖"。旧列存在说明库是老结构,迁移后要清掉播种的占位模型名。
	legacyCapSchema := db.Migrator().HasTable(&CapabilityPrice{}) &&
		db.Migrator().HasColumn(&CapabilityPrice{}, "temperature")
	// 默认模型表曾被 GORM 蛇形化成 a_idefaults:先改名保数据,再走常规迁移
	if db.Migrator().HasTable("a_idefaults") && !db.Migrator().HasTable("ai_defaults") {
		if err := db.Migrator().RenameTable("a_idefaults", "ai_defaults"); err != nil {
			return fmt.Errorf("重命名 a_idefaults 失败: %w", err)
		}
	}
	if err := db.AutoMigrate(
		&User{}, &EmailCode{}, &RefreshToken{}, &SsoCode{},
		&SubscriptionPlan{}, &UserSubscription{}, &PaymentOrder{},
		&AIDefault{}, &CapabilityPrice{}, &AIUpstream{}, &CreditLedger{}, &CreditHold{},
		&AIRequest{}, &TaskMetric{},
	); err != nil {
		return fmt.Errorf("迁移失败: %w", err)
	}
	if legacyCapSchema {
		// 旧配置全是播种占位值(非管理员手配),重置为"用全局默认",再删除废弃列
		if err := db.Model(&CapabilityPrice{}).Where("1 = 1").Update("model", "").Error; err != nil {
			return fmt.Errorf("清理旧能力配置失败: %w", err)
		}
	}
	// 废弃列清理:temperature/max_tokens 是最初的旧结构;*_override 是短暂存在过的
	// "参数可覆盖"设计——温度与 maxTokens 都已收回代码按能力定死,不再是配置项
	for _, col := range []string{"temperature", "max_tokens", "temperature_override", "max_tokens_override"} {
		if db.Migrator().HasColumn(&CapabilityPrice{}, col) {
			if err := db.Migrator().DropColumn(&CapabilityPrice{}, col); err != nil {
				return fmt.Errorf("删除旧列 %s 失败: %w", col, err)
			}
		}
	}
	for _, col := range []string{"temperature", "max_tokens"} {
		if db.Migrator().HasColumn(&AIDefault{}, col) {
			if err := db.Migrator().DropColumn(&AIDefault{}, col); err != nil {
				return fmt.Errorf("删除旧列 ai_defaults.%s 失败: %w", col, err)
			}
		}
	}
	// 部分唯一索引(postgres 与 sqlite 语法一致):
	// 1. 幂等闸门:同用户同 requestId 只允许一行
	// 2. 计费组防重:一组至多一条消费流水
	// 3. 防重复预占:同任务至多一个 open hold
	// 4. 任务统计:同任务一条(重复上报覆盖走 upsert)
	for _, sql := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_ai_requests_user_request ON ai_requests(user_id, request_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_billing_group ON credit_ledgers(user_id, billing_group_id) WHERE billing_group_id <> '' AND delta < 0`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_holds_open_task ON credit_holds(user_id, task_id) WHERE status = 'open'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_task_metrics_user_task ON task_metrics(user_id, task_id)`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			return fmt.Errorf("建索引失败(%s): %w", sql, err)
		}
	}
	return nil
}

// LockUser 在事务内锁定用户行(积分扣减/预占的并发闸门)。
// postgres 用 SELECT ... FOR UPDATE;sqlite 写事务本身串行,无需也不支持行锁。
func LockUser(tx *gorm.DB, userID int64) error {
	q := tx.Model(&User{})
	if tx.Dialector.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var u User
	if err := q.Where("id = ?", userID).First(&u).Error; err != nil {
		return err
	}
	return nil
}
