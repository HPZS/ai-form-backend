// 隔离浏览器验收服务：真实网关、真实模型、独立 PostgreSQL schema，无生产用户或支付。
package main

import (
	"encoding/json"
	"fmt"
	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/ai"
	"github.com/HPZS/ai-form-backend/internal/auth"
	"github.com/HPZS/ai-form-backend/internal/email"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/server"
	"github.com/HPZS/ai-form-backend/internal/subscription"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	dsn := os.Getenv("AIFORM_BILLING_TEST_DSN")
	if !strings.Contains(dsn, "aiform_billing_test") || !strings.Contains(dsn, "127.0.0.1") {
		return fmt.Errorf("只允许本机专用验收库")
	}
	db, err := model.Open(dsn)
	if err != nil {
		return err
	}
	schema := "browser_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		return err
	}
	db, err = model.Open(dsn + " search_path=" + schema)
	if err != nil {
		return err
	}
	if err = subscription.SeedDefaults(db); err != nil {
		return err
	}
	endpoint, key, modelName := os.Getenv("AIFORM_UPSTREAM_URL"), os.Getenv("AIFORM_UPSTREAM_KEY"), os.Getenv("AIFORM_EVAL_MODEL")
	if endpoint == "" || key == "" || modelName == "" {
		return fmt.Errorf("缺少验收上游配置")
	}
	if err = db.Model(&model.AIDefault{}).Where("id = ?", 1).Update("model", modelName).Error; err != nil {
		return err
	}
	if raw := os.Getenv("AIFORM_EVAL_CAPABILITY_MODELS"); raw != "" {
		var overrides map[string]string
		if err = json.Unmarshal([]byte(raw), &overrides); err != nil {
			return fmt.Errorf("能力模型配置不是有效 JSON")
		}
		for capability, name := range overrides {
			result := db.Model(&model.CapabilityPrice{}).Where("capability = ?", capability).Update("model", name)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("能力模型配置引用未知能力 %s", capability)
			}
		}
	}
	upstream := model.AIUpstream{Name: "billing-eval", BaseURL: endpoint, APIKey: key, Enabled: true,
		ThinkingMode: os.Getenv("AIFORM_EVAL_THINKING_MODE"), TokenLimitParameter: os.Getenv("AIFORM_EVAL_TOKEN_LIMIT_PARAMETER")}
	if err = upstream.ValidateGenerationOptions(); err != nil {
		return err
	}
	if err = db.Create(&upstream).Error; err != nil {
		return err
	}
	authSvc := auth.New(db, "local-billing-eval-secret", "local-billing-eval-pepper", nil)
	code, err := authSvc.IssueCode("fixture@example.test")
	if err != nil {
		return err
	}
	user, pair, err := authSvc.Login("fixture@example.test", code)
	if err != nil {
		return err
	}
	if err = db.Create(&model.UserSubscription{UserID: user.ID, PlanType: model.PlanTypeBase, AmountTotal: 1000, StartsAt: time.Now(), EndsAt: time.Now().Add(30 * 24 * time.Hour), Status: model.SubStatusActive}).Error; err != nil {
		return err
	}
	caps := []string{}
	for _, s := range ai.Specs() {
		caps = append(caps, s.Name)
	}
	prompts, err := ai.LoadPrompts(os.Getenv("PROMPTS_DIR"), caps)
	if err != nil {
		return err
	}
	gateway := ai.NewGateway(db, ai.NewCaller(db), prompts)
	gateway.EnableBillingV2("local-billing-eval-pepper")
	gin.SetMode(gin.ReleaseMode)
	router := server.New(db, &config.Config{}, authSvc, email.New(config.SMTPConfig{}), gateway, nil)
	// 本机只读验收快照，既不改余额也不返回业务原文或上游配置。
	router.GET("/fixture-state", func(c *gin.Context) {
		var rows []model.AIRequest
		var ledger []model.CreditLedger
		var budgets []model.AIExecutionBudget
		if err := db.Select("request_id, task_id, work_id, recovery_group_id, capability, status, failure_code, credits, billing_reason, input_tokens, output_tokens, usage_details, latency_ms").Order("id").Find(&rows).Error; err != nil {
			c.JSON(500, gin.H{"error": "读取请求快照失败"})
			log.Printf("读取验收请求失败: %v", err)
			return
		}
		if err := db.Select("request_id,task_id,delta").Order("id").Find(&ledger).Error; err != nil {
			c.JSON(500, gin.H{"error": "读取账务快照失败"})
			log.Printf("读取验收账务失败: %v", err)
			return
		}
		if err := db.Order("id").Find(&budgets).Error; err != nil {
			c.JSON(500, gin.H{"error": "读取预算快照失败"})
			log.Printf("读取验收预算失败: %v", err)
			return
		}
		c.JSON(200, gin.H{"requests": rows, "ledger": ledger, "budgets": budgets})
	})
	address := os.Getenv("AIFORM_BILLING_EVAL_ADDR")
	if address == "" {
		address = "127.0.0.1:18097"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("验收服务只允许监听本机回环地址")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"url": "http://" + listener.Addr().String(), "token": pair.AccessToken, "userId": fmt.Sprint(user.ID), "schema": schema, "model": modelName}); err != nil {
		return err
	}
	go func() {
		for range time.NewTicker(time.Second).C {
			if err := gateway.RecoverBillingV2(); err != nil {
				log.Printf("恢复费用失败: %v", err)
			}
		}
	}()
	return http.Serve(listener, router)
}
