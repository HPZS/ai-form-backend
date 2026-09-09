// 动态计费控制台验收：内存数据库和本机模拟上游，不访问生产数据。
package main

import (
	"encoding/json"
	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/ai"
	"github.com/HPZS/ai-form-backend/internal/auth"
	"github.com/HPZS/ai-form-backend/internal/email"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/server"
	"github.com/HPZS/ai-form-backend/internal/subscription"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	db, err := model.OpenMemory()
	if err != nil {
		return err
	}
	if err = subscription.SeedDefaults(db); err != nil {
		return err
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "function transform(row) { return row['姓名']; }"}}}}); err != nil {
			log.Printf("模拟上游响应失败: %v", err)
		}
	}))
	defer upstream.Close()
	if err = db.Create(&model.AIUpstream{Name: "local-fixture", BaseURL: upstream.URL, APIKey: "fixture", Enabled: true}).Error; err != nil {
		return err
	}
	if err = db.Model(&model.AIDefault{}).Where("id = ?", 1).Update("model", "fixture").Error; err != nil {
		return err
	}
	authSvc := auth.New(db, "local-config-fixture-secret", "local-config-fixture-pepper", nil)
	code, err := authSvc.IssueCode("fixture@example.test")
	if err != nil {
		return err
	}
	user, pair, err := authSvc.Login("fixture@example.test", code)
	if err != nil {
		return err
	}
	if err = db.Model(user).Update("role", "admin").Error; err != nil {
		return err
	}
	if err = db.Create(&model.UserSubscription{UserID: user.ID, PlanType: model.PlanTypeBase, AmountTotal: 1000, StartsAt: time.Now(), EndsAt: time.Now().Add(24 * time.Hour), Status: model.SubStatusActive}).Error; err != nil {
		return err
	}
	caps := []string{}
	for _, spec := range ai.Specs() {
		caps = append(caps, spec.Name)
	}
	prompts, err := ai.LoadPrompts("prompts/private", caps)
	if err != nil {
		return err
	}
	gateway := ai.NewGateway(db, ai.NewCaller(db), prompts)
	gateway.EnableBillingV2("local-config-fixture-pepper")
	gin.SetMode(gin.ReleaseMode)
	router := server.New(db, &config.Config{}, authSvc, email.New(config.SMTPConfig{}), gateway, nil)
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": "http://127.0.0.1:18097", "token": pair.AccessToken}); err != nil {
		return err
	}
	return router.Run("127.0.0.1:18097")
}
