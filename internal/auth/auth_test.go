// ai-form-backend - AGPL-3.0
package auth

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/internal/model"
)

func setup(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := model.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	return New(db, "test-secret", "test-pepper", []string{"admin@example.com"}), db
}

func TestLoginFlowAndTrial(t *testing.T) {
	svc, db := setup(t)
	// 播种试用套餐
	db.Create(&model.SubscriptionPlan{Name: "试用", TotalCredits: 100, DurationDays: 14, Trial: true})

	code, err := svc.IssueCode("User@Example.com ")
	if err != nil {
		t.Fatal(err)
	}
	user, pair, err := svc.Login("user@example.com", code)
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if user.Email != "user@example.com" {
		t.Fatalf("邮箱应归一化,实际 %s", user.Email)
	}
	// 注册应发试用桶
	var subs []model.UserSubscription
	db.Find(&subs, "user_id = ?", user.ID)
	if len(subs) != 1 || subs[0].AmountTotal != 100 {
		t.Fatalf("应发放试用桶 100 分,实际 %+v", subs)
	}
	// access token 可验证
	uid, err := svc.VerifyAccess(pair.AccessToken)
	if err != nil || uid != user.ID {
		t.Fatalf("access token 校验失败: %v", err)
	}
	// 验证码已消费,重放失败
	if _, _, err := svc.Login("user@example.com", code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("验证码重放应失败,实际 %v", err)
	}
}

func TestCodeAttemptLockout(t *testing.T) {
	svc, _ := setup(t)
	code, err := svc.IssueCode("a@b.com")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := svc.Login("a@b.com", "000000"); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("错码应报 ErrCodeInvalid: %v", err)
		}
	}
	// 5 次错后正确码也失效
	if _, _, err := svc.Login("a@b.com", code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("超限后正确码应作废,实际 %v", err)
	}
}

func TestNewCodeInvalidatesOld(t *testing.T) {
	svc, _ := setup(t)
	old, _ := svc.IssueCode("a@b.com")
	// 绕开 1 分钟频控:直接把旧记录时间改早
	svc.db.Model(&model.EmailCode{}).Where("1=1").
		Update("created_at", time.Now().Add(-2*time.Minute))
	newer, err := svc.IssueCode("a@b.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login("a@b.com", old); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("旧码应作废,实际 %v", err)
	}
	if _, _, err := svc.Login("a@b.com", newer); err != nil {
		t.Fatalf("新码应可用: %v", err)
	}
}

func TestPasswordLogin(t *testing.T) {
	svc, _ := setup(t)
	code, _ := svc.IssueCode("a@b.com")
	user, _, err := svc.Login("a@b.com", code)
	if err != nil {
		t.Fatal(err)
	}
	// 未设密码时密码登录应明确报 ErrNoPassword
	if _, _, err := svc.LoginPassword("a@b.com", "whatever123"); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("未设密码应报 ErrNoPassword,实际 %v", err)
	}
	// 弱密码拒绝
	if err := svc.SetPassword(user.ID, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("弱密码应拒绝,实际 %v", err)
	}
	if err := svc.SetPassword(user.ID, "password123"); err != nil {
		t.Fatal(err)
	}
	// 正确密码可登录
	u2, pair, err := svc.LoginPassword("A@B.com ", "password123")
	if err != nil || u2.ID != user.ID || pair.AccessToken == "" {
		t.Fatalf("密码登录失败: %v", err)
	}
	// 错误密码与不存在账号统一报 ErrPasswordInvalid(不暴露账号存在性)
	if _, _, err := svc.LoginPassword("a@b.com", "wrongpass1"); !errors.Is(err, ErrPasswordInvalid) {
		t.Fatalf("错密码应报 ErrPasswordInvalid,实际 %v", err)
	}
	if _, _, err := svc.LoginPassword("nobody@b.com", "whatever123"); !errors.Is(err, ErrPasswordInvalid) {
		t.Fatalf("不存在账号应报 ErrPasswordInvalid,实际 %v", err)
	}
	// 重设密码后旧密码失效
	if err := svc.SetPassword(user.ID, "newpassword456"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.LoginPassword("a@b.com", "password123"); !errors.Is(err, ErrPasswordInvalid) {
		t.Fatalf("旧密码应失效,实际 %v", err)
	}
}

func TestSsoCode(t *testing.T) {
	svc, _ := setup(t)
	code, _ := svc.IssueCode("a@b.com")
	user, _, err := svc.Login("a@b.com", code)
	if err != nil {
		t.Fatal(err)
	}
	sso, err := svc.IssueSsoCode(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	u2, pair, err := svc.ExchangeSsoCode(sso)
	if err != nil || u2.ID != user.ID || pair.AccessToken == "" {
		t.Fatalf("SSO 换取失败: %v", err)
	}
	// 单次使用:二次兑换失败
	if _, _, err := svc.ExchangeSsoCode(sso); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("SSO 码应单次使用,实际 %v", err)
	}
	// 过期码失败
	sso2, _ := svc.IssueSsoCode(user.ID)
	svc.db.Model(&model.SsoCode{}).Where("used_at IS NULL").
		Update("expires_at", time.Now().Add(-time.Second))
	if _, _, err := svc.ExchangeSsoCode(sso2); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("过期 SSO 码应失败,实际 %v", err)
	}
}

func TestRefreshRotationAndLeakDetection(t *testing.T) {
	svc, db := setup(t)
	code, _ := svc.IssueCode("a@b.com")
	_, pair, err := svc.Login("a@b.com", code)
	if err != nil {
		t.Fatal(err)
	}
	pair2, err := svc.Refresh(pair.RefreshToken)
	if err != nil {
		t.Fatalf("旋转失败: %v", err)
	}
	// 宽限期内旧 token 复用:401 但不撤 family(新 token 仍可用)
	if _, err := svc.Refresh(pair.RefreshToken); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("旧 token 应 401,实际 %v", err)
	}
	pair3, err := svc.Refresh(pair2.RefreshToken)
	if err != nil {
		t.Fatalf("宽限期内 family 不应被撤销: %v", err)
	}
	// 把旧 token 的 revoked_at 改到宽限期之外,复用 → 泄露信号,整个 family 撤销
	db.Model(&model.RefreshToken{}).Where("token_hash = ?", sha256Hex(pair2.RefreshToken)).
		Update("revoked_at", time.Now().Add(-time.Minute))
	if _, err := svc.Refresh(pair2.RefreshToken); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("超宽限复用应 401: %v", err)
	}
	if _, err := svc.Refresh(pair3.RefreshToken); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("family 应被整体撤销,实际 %v", err)
	}
}
