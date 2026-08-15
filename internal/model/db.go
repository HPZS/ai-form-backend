// ai-form-backend - AGPL-3.0
package model

import (
	"fmt"
	"sync/atomic"

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

// memDBSeq 给每次 OpenMemory 生成独立库名,测试之间互不可见。
var memDBSeq atomic.Int64

// OpenMemory 内存 sqlite,单元测试用。
//
// 必须用具名 + cache=shared 的 DSN:裸 ":memory:" 下每条连接都是一个**独立的空库**,
// 而 GORM 连接池会在并发查询时开第二条连接——迁移建的表在它上面根本不存在,
// 测试随机报 "no such table"(并发路径的用例首当其冲)。
func OpenMemory() (*gorm.DB, error) {
	dsn := fmt.Sprintf("file:memdb%d?mode=memory&cache=shared", memDBSeq.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
	// 计费模型 M0 改造前置判断:旧版套餐表用 trial 布尔列区分试用,新版改为 plan_type 四类。
	// 旧列存在 = 旧计费体系(按次扣分):套餐与能力单价整体重播种(产品未上线,无真实用户,方案文档 §6.7)
	legacyBillingSchema := db.Migrator().HasTable(&SubscriptionPlan{}) &&
		db.Migrator().HasColumn(&SubscriptionPlan{}, "trial")
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
		&AIRequest{}, &TaskMetric{}, &BillingGroupCharge{},
	); err != nil {
		return fmt.Errorf("迁移失败: %w", err)
	}
	if legacyCapSchema {
		// 旧配置全是播种占位值(非管理员手配),重置为"用全局默认",再删除废弃列
		if err := db.Model(&CapabilityPrice{}).Where("1 = 1").Update("model", "").Error; err != nil {
			return fmt.Errorf("清理旧能力配置失败: %w", err)
		}
	}
	if legacyBillingSchema {
		// 1) 已发桶按旧套餐的 trial 标记回填 plan_type(开发库桶保持可用)
		if err := db.Exec(`UPDATE user_subscriptions SET plan_type = 'trial'
			WHERE plan_id IN (SELECT id FROM subscription_plans WHERE trial = ?)`, true).Error; err != nil {
			return fmt.Errorf("回填桶类型失败: %w", err)
		}
		if err := db.Exec(`UPDATE user_subscriptions SET plan_type = 'base'
			WHERE plan_type = '' OR plan_type IS NULL`).Error; err != nil {
			return fmt.Errorf("回填桶类型失败: %w", err)
		}
		// 2) 旧套餐与旧能力单价整体清空,由 SeedDefaults 按新计费模型重播种
		if err := db.Exec(`DELETE FROM subscription_plans`).Error; err != nil {
			return fmt.Errorf("清空旧套餐失败: %w", err)
		}
		if err := db.Exec(`DELETE FROM capability_prices`).Error; err != nil {
			return fmt.Errorf("清空旧能力单价失败: %w", err)
		}
		// 3) 删除废弃的 trial 列
		if err := db.Migrator().DropColumn(&SubscriptionPlan{}, "trial"); err != nil {
			return fmt.Errorf("删除旧列 subscription_plans.trial 失败: %w", err)
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
	// 计费组防重锚点迁移:旧版把"已收费"绑在流水行的部分唯一索引上,与跨桶扣费(每桶一条流水)互斥,
	// 跨桶带计费组的扣费必然撞索引失败。改为独立表后必须丢弃旧索引,否则老库上问题依旧。
	if err := db.Exec(`INSERT INTO billing_group_charges (user_id, billing_group_id, request_id, capability, price, created_at)
		SELECT user_id, billing_group_id, MIN(request_id), MIN(capability), MIN(-delta), MIN(created_at)
		FROM credit_ledgers WHERE billing_group_id <> '' AND delta < 0
		  AND NOT EXISTS (SELECT 1 FROM billing_group_charges b
		    WHERE b.user_id = credit_ledgers.user_id AND b.billing_group_id = credit_ledgers.billing_group_id)
		GROUP BY user_id, billing_group_id`).Error; err != nil {
		return fmt.Errorf("回填计费组防重锚点失败: %w", err)
	}
	if err := db.Exec(`DROP INDEX IF EXISTS uq_ledger_billing_group`).Error; err != nil {
		return fmt.Errorf("删除旧计费组索引失败: %w", err)
	}
	// 部分唯一索引(postgres 与 sqlite 语法一致):
	// 1. 幂等闸门:同用户同 requestId 只允许一行
	// 2. 计费组防重:一组至多收一次费(锚点独立于流水行数)
	// 3. 防重复预占:同任务至多一个 open hold
	// 4. 任务统计:同任务一条(重复上报覆盖走 upsert)
	for _, sql := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_ai_requests_user_request ON ai_requests(user_id, request_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_billing_group_charge ON billing_group_charges(user_id, billing_group_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_holds_open_task ON credit_holds(user_id, task_id) WHERE status = 'open'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_task_metrics_user_task ON task_metrics(user_id, task_id)`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			return fmt.Errorf("建索引失败(%s): %w", sql, err)
		}
	}
	return nil
}

// LockSubscription 在事务内锁定并读取额度桶行(桶的读-改-写并发闸门)。
// 不加锁时两笔并发延期会各自读到同一个 ends_at,后写的覆盖前写的,一笔延期凭空消失;
// 状态从 expired 翻回 active 的判断也建立在这次读取上。
func LockSubscription(tx *gorm.DB, subID int64) (UserSubscription, error) {
	q := tx.Model(&UserSubscription{})
	if tx.Dialector.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var sub UserSubscription
	err := q.Where("id = ?", subID).First(&sub).Error
	return sub, err
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
