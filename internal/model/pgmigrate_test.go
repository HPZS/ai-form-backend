// ai-form-backend - AGPL-3.0
package model

import (
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 针对真 PostgreSQL 的迁移验证。生产是 Postgres 而单元测试跑 sqlite,
// 方言差异(部分唯一索引、DROP INDEX、INSERT...SELECT...GROUP BY 里的相关子查询)
// 只有在真库上才验得出来。上线前跑一次:
//
//	docker run -d --name pgtest -e POSTGRES_USER=aiform -e POSTGRES_PASSWORD=testpw \
//	  -e POSTGRES_DB=aiform -p 55432:5432 postgres:16-alpine
//	PG_TEST_DSN='host=localhost user=aiform password=testpw dbname=aiform port=55432 sslmode=disable' \
//	  go test ./internal/model/ -run TestMigrateOnPostgres -v
func TestMigrateOnPostgres(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 PG_TEST_DSN,跳过 Postgres 迁移验证")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("连库失败: %v", err)
	}
	// 每次从干净库开始:上一轮的表全部丢弃
	if err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`).Error; err != nil {
		t.Fatal(err)
	}

	// 1. 造一个"旧库":老结构的流水表 + 老的部分唯一索引 + 一条历史计费组消费
	if err := db.AutoMigrate(&CreditLedger{}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE UNIQUE INDEX uq_ledger_billing_group ON credit_ledgers(user_id, billing_group_id) WHERE billing_group_id <> '' AND delta < 0`,
		`INSERT INTO credit_ledgers (user_id, subscription_id, request_id, billing_group_id, capability, delta, price_snapshot, balance_after, created_at)
		 VALUES (7, 1, 'req-old', 'g-old', 'match_columns', -50, 50, 100, NOW())`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("造旧库失败(%s): %v", sql, err)
		}
	}

	// 2. 跑迁移
	if err := Migrate(db); err != nil {
		t.Fatalf("Postgres 迁移失败: %v", err)
	}

	// 3. 历史计费组必须回填成锚点(否则防重失效,老用户会被重复收费)
	var anchor BillingGroupCharge
	if err := db.First(&anchor, "user_id = ? AND billing_group_id = ?", 7, "g-old").Error; err != nil {
		t.Fatalf("历史计费组应回填为锚点: %v", err)
	}
	if anchor.Price != 50 || anchor.RequestID != "req-old" {
		t.Fatalf("锚点内容应来自历史流水,实际 %+v", anchor)
	}

	// 4. 旧索引必须已丢弃:同组第二条流水(跨桶场景)要能写进去
	if err := db.Exec(`INSERT INTO credit_ledgers (user_id, subscription_id, request_id, billing_group_id, capability, delta, price_snapshot, balance_after, created_at)
		VALUES (7, 2, 'req-old', 'g-old', 'match_columns', -20, 50, 80, NOW())`).Error; err != nil {
		t.Fatalf("旧索引应已丢弃,同组第二条流水应可写入: %v", err)
	}

	// 5. 新锚点的唯一约束要真的生效
	dup := BillingGroupCharge{UserID: 7, BillingGroupID: "g-old", Capability: "x", Price: 1, CreatedAt: time.Now()}
	if err := db.Create(&dup).Error; err == nil {
		t.Fatal("同一计费组重复落锚点应被唯一索引拒绝")
	}

	// 6. 迁移幂等:再跑一次不应重复回填、不应报错
	if err := Migrate(db); err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
	var cnt int64
	db.Model(&BillingGroupCharge{}).Where("user_id = ? AND billing_group_id = ?", 7, "g-old").Count(&cnt)
	if cnt != 1 {
		t.Fatalf("锚点应只有一条,实际 %d", cnt)
	}
}
