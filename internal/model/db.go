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
	if err := db.AutoMigrate(
		&User{}, &EmailCode{}, &RefreshToken{},
		&SubscriptionPlan{}, &UserSubscription{}, &PaymentOrder{},
		&CapabilityPrice{}, &AIUpstream{}, &CreditLedger{}, &CreditHold{},
		&AIRequest{}, &TaskMetric{},
	); err != nil {
		return fmt.Errorf("迁移失败: %w", err)
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
