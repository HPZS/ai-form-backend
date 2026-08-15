// ai-form-backend - AGPL-3.0
package main

import (
	"embed"
	"io/fs"
	"log"
	"time"

	"github.com/joho/godotenv"

	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/ai"
	"github.com/HPZS/ai-form-backend/internal/auth"
	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/email"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/payment"
	"github.com/HPZS/ai-form-backend/internal/server"
	"github.com/HPZS/ai-form-backend/internal/subscription"
)

// 前端控制台构建产物(cd web && npm run build 生成,已随仓库提交)
//
//go:embed all:web/dist
var webFS embed.FS

func main() {
	_ = godotenv.Load() // .env 不存在时忽略,环境变量优先

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}
	db, err := model.Open(cfg.DBDSN)
	if err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}
	if err := subscription.SeedDefaults(db); err != nil {
		log.Fatalf("播种默认数据失败: %v", err)
	}

	specs := ai.Specs()
	names := make([]string, len(specs))
	for i, sp := range specs {
		names[i] = sp.Name
	}
	prompts, err := ai.LoadPrompts(cfg.PromptsDir, names)
	if err != nil {
		log.Fatalf("提示词加载失败: %v", err)
	}

	// AI 上游与能力模型参数在数据库中,由 /admin 管理台维护
	gateway := ai.NewGateway(db, ai.NewCaller(db), prompts)
	authSvc := auth.New(db, cfg.JWTSecret, cfg.HashPepper, cfg.AdminEmails)
	mailer := email.New(cfg.SMTP)

	// 后台维护任务:订阅过期、预占过期释放、幂等缓存清理
	go func() {
		for range time.Tick(time.Minute) {
			if n, err := subscription.ExpireSubscriptions(db); err != nil {
				log.Printf("订阅过期任务出错: %v", err)
			} else if n > 0 {
				log.Printf("已过期 %d 个订阅桶", n)
			}
			if n, err := credits.ExpireHolds(db); err != nil {
				log.Printf("预占过期任务出错: %v", err)
			} else if n > 0 {
				log.Printf("已释放 %d 个过期预占", n)
			}
			if n, err := payment.ExpirePendingOrders(db); err != nil {
				log.Printf("订单过期任务出错: %v", err)
			} else if n > 0 {
				log.Printf("已过期 %d 个未支付订单", n)
			}
			if _, err := ai.CleanExpiredCaches(db); err != nil {
				log.Printf("幂等缓存清理出错: %v", err)
			}
		}
	}()

	webDist, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		log.Fatalf("前端资源缺失: %v", err)
	}
	engine := server.New(db, cfg, authSvc, mailer, gateway, webDist)
	log.Printf("ai-form-backend 启动于 %s (源码: https://github.com/HPZS/ai-form-backend, AGPL-3.0)", cfg.ListenAddr)
	if err := engine.Run(cfg.ListenAddr); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
