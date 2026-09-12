package server

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/HPZS/ai-form-backend/config"
	"github.com/HPZS/ai-form-backend/internal/auth"
	"github.com/HPZS/ai-form-backend/internal/email"
	"github.com/HPZS/ai-form-backend/internal/model"
)

func TestAdapterCapabilityAndStableIdentity(t *testing.T) {
	router, db, token := setupServer(t)
	decode := func(body string) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	about := decode(do(t, router, "", "GET", "/v1/about", "").Body.String())
	if about["apiRevision"] != float64(21) || about["adapterProtocolVersion"] != float64(1) {
		t.Fatalf("适配能力必须显式协商: %v", about)
	}
	me := decode(do(t, router, token, "GET", "/v1/me", "").Body.String())
	want := strconv.FormatInt(userID(t, db), 10)
	if me["userId"] != want {
		t.Fatalf("已有登录态必须能补全稳定主体: %v", me)
	}

	authSvc := auth.New(db, "test-secret", "test-pepper", nil)
	if err := db.Model(&model.EmailCode{}).Where("email = ?", "u@example.com").Update("created_at", time.Now().Add(-2*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	code, err := authSvc.IssueCode("u@example.com")
	if err != nil {
		t.Fatal(err)
	}
	router = New(db, &config.Config{}, authSvc, email.New(config.SMTPConfig{}), nil, nil)
	check := func(path, body string) {
		t.Helper()
		w := do(t, router, "", "POST", path, body)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		out := decode(w.Body.String())
		if out["user"].(map[string]any)["userId"] != want {
			t.Fatalf("登录主体不一致: %v", out)
		}
	}
	check("/v1/auth/login", `{"email":"u@example.com","code":"`+code+`"}`)
	if err := authSvc.SetPassword(userID(t, db), "stable-password-123"); err != nil {
		t.Fatal(err)
	}
	check("/v1/auth/login-password", `{"email":"u@example.com","password":"stable-password-123"}`)
	sso, err := authSvc.IssueSsoCode(userID(t, db))
	if err != nil {
		t.Fatal(err)
	}
	check("/v1/auth/sso-exchange", `{"code":"`+sso+`"}`)
	var other model.User
	if err := db.First(&other).Error; err != nil {
		t.Fatal(err)
	}
	if strconv.FormatInt(other.ID, 10) != want {
		t.Fatal("身份读取不得修改用户")
	}
}
