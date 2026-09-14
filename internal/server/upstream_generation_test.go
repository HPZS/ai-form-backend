package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/HPZS/ai-form-backend/internal/model"
)

func TestUpstreamGenerationOptionsAreExplicitAndPreserved(t *testing.T) {
	router, db, token := setupServer(t)
	if err := db.Model(&model.User{}).Where("id = ?", userID(t, db)).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	w := do(t, router, token, "POST", "/v1/admin/upstreams", `{"name":"test","baseUrl":"https://example.test/v1","apiKey":"private-secret","thinkingMode":"disabled","tokenLimitParameter":"max_completion_tokens"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct{ ID int64 }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/v1/admin/upstreams/%d", created.ID)
	check := func(thinking, limit string) {
		t.Helper()
		w := do(t, router, token, "GET", "/v1/admin/upstreams", "")
		var out struct{ Upstreams []map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(out.Upstreams) != 1 || out.Upstreams[0]["thinkingMode"] != thinking || out.Upstreams[0]["tokenLimitParameter"] != limit {
			t.Fatalf("生成参数未正确保存: %s", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-secret") {
			t.Fatal("列表不得披露密钥")
		}
	}
	check("disabled", "max_completion_tokens")
	w = do(t, router, token, "PUT", path, `{"name":"renamed","apiKey":""}`)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	check("disabled", "max_completion_tokens")
	for _, body := range []string{`{"thinkingMode":"off"}`, `{"tokenLimitParameter":"arbitrary"}`} {
		w = do(t, router, token, "PUT", path, body)
		if w.Code != 400 {
			t.Fatalf("必须拒绝未知参数: %d %s", w.Code, w.Body.String())
		}
		check("disabled", "max_completion_tokens")
	}
	w = do(t, router, token, "PUT", path, `{"thinkingMode":"","tokenLimitParameter":""}`)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	check("", "")
	var row model.AIUpstream
	if err := db.First(&row, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.APIKey != "private-secret" {
		t.Fatal("改生成设置不能清除原密钥")
	}
}
