// ai-form-backend - AGPL-3.0
// 认证:邮箱验证码登录(免密)、JWT access token、refresh token 旋转。
// 加固规则(技术方案 v3 §4.3/§6.2):
//   验证码 HMAC(pepper) 存储、同码验错 5 次作废、新码作废旧码、验证+消费原子;
//   refresh 旧记录不删(revoked_at/replaced_by/family),旧 token 复用超宽限期即撤销整个 family。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/HPZS/ai-form-backend/internal/subscription"
)

const (
	codeTTL          = 10 * time.Minute
	codeMaxAttempts  = 5
	accessTTL        = 30 * time.Minute
	refreshTTL       = 30 * 24 * time.Hour
	rotationGrace    = 30 * time.Second // 并发刷新宽限:此窗口内旧 token 复用不判泄露
	sendMinInterval  = time.Minute
	sendDailyPerMail = 10
)

var (
	ErrTooFrequent   = errors.New("发送太频繁,请稍后再试")
	ErrCodeInvalid   = errors.New("验证码错误或已失效")
	ErrTokenInvalid  = errors.New("登录已失效,请重新登录")
	ErrUserBanned    = errors.New("账号已被禁用")
)

type Service struct {
	db          *gorm.DB
	jwtSecret   []byte
	pepper      []byte
	adminEmails map[string]bool
}

func New(db *gorm.DB, jwtSecret, pepper string, adminEmails []string) *Service {
	admins := make(map[string]bool, len(adminEmails))
	for _, e := range adminEmails {
		admins[NormalizeEmail(e)] = true
	}
	return &Service{db: db, jwtSecret: []byte(jwtSecret), pepper: []byte(pepper), adminEmails: admins}
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *Service) hmacHex(v string) string {
	m := hmac.New(sha256.New, s.pepper)
	m.Write([]byte(v))
	return hex.EncodeToString(m.Sum(nil))
}

func sha256Hex(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// ===== 验证码 =====

func randomDigits(n int) (string, error) {
	var b strings.Builder
	for i := 0; i < n; i++ {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%d", d.Int64())
	}
	return b.String(), nil
}

// IssueCode 生成并落库验证码,返回明文交由邮件模块发送。频控:同邮箱 1 分钟 1 条、每日 10 条。
func (s *Service) IssueCode(email string) (string, error) {
	email = NormalizeEmail(email)
	now := time.Now()
	var recent int64
	if err := s.db.Model(&model.EmailCode{}).
		Where("email = ? AND created_at > ?", email, now.Add(-sendMinInterval)).
		Count(&recent).Error; err != nil {
		return "", err
	}
	if recent > 0 {
		return "", ErrTooFrequent
	}
	var daily int64
	if err := s.db.Model(&model.EmailCode{}).
		Where("email = ? AND created_at > ?", email, now.Add(-24*time.Hour)).
		Count(&daily).Error; err != nil {
		return "", err
	}
	if daily >= sendDailyPerMail {
		return "", ErrTooFrequent
	}
	code, err := randomDigits(6)
	if err != nil {
		return "", err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 新码作废旧码
		if err := tx.Model(&model.EmailCode{}).
			Where("email = ? AND used_at IS NULL", email).
			Update("used_at", now).Error; err != nil {
			return err
		}
		return tx.Create(&model.EmailCode{
			Email:     email,
			CodeHash:  s.hmacHex(code),
			ExpiresAt: now.Add(codeTTL),
			CreatedAt: now,
		}).Error
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// consumeCode 校验并原子消费验证码;失败计入 attempts,超限作废。
func (s *Service) consumeCode(email, code string) error {
	email = NormalizeEmail(email)
	now := time.Now()
	var rec model.EmailCode
	err := s.db.Where("email = ? AND used_at IS NULL AND expires_at > ? AND attempts < ?",
		email, now, codeMaxAttempts).
		Order("created_at desc").First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrCodeInvalid
	}
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(rec.CodeHash), []byte(s.hmacHex(code))) {
		s.db.Model(&model.EmailCode{}).Where("id = ?", rec.ID).
			Update("attempts", gorm.Expr("attempts + 1"))
		return ErrCodeInvalid
	}
	// 原子消费:guard 防并发重放
	r := s.db.Model(&model.EmailCode{}).
		Where("id = ? AND used_at IS NULL", rec.ID).
		Update("used_at", now)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return ErrCodeInvalid
	}
	return nil
}

// ===== 登录/注册 =====

type TokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// Login 验证码正确即登录;新邮箱自动注册并发放试用桶。
func (s *Service) Login(email, code string) (*model.User, *TokenPair, error) {
	email = NormalizeEmail(email)
	if err := s.consumeCode(email, code); err != nil {
		return nil, nil, err
	}
	var user model.User
	err := s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("email = ?", email).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			user = model.User{Email: email, Role: "user", Status: "active", CreatedAt: time.Now()}
			if s.adminEmails[email] {
				user.Role = "admin"
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			return subscription.AssignTrial(tx, user.ID)
		}
		if err != nil {
			return err
		}
		if s.adminEmails[email] && user.Role != "admin" {
			user.Role = "admin"
			return tx.Model(&user).Update("role", "admin").Error
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if user.Status != "active" {
		return nil, nil, ErrUserBanned
	}
	pair, err := s.issueTokens(user.ID, uuid.NewString())
	if err != nil {
		return nil, nil, err
	}
	return &user, pair, nil
}

func (s *Service) issueTokens(userID int64, familyID string) (*TokenPair, error) {
	now := time.Now()
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": fmt.Sprintf("%d", userID),
		"iat": now.Unix(),
		"exp": now.Add(accessTTL).Unix(),
	}).SignedString(s.jwtSecret)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	refresh := hex.EncodeToString(raw)
	rec := model.RefreshToken{
		ID:        uuid.NewString(),
		UserID:    userID,
		TokenHash: sha256Hex(refresh),
		FamilyID:  familyID,
		ExpiresAt: now.Add(refreshTTL),
		CreatedAt: now,
	}
	if err := s.db.Create(&rec).Error; err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: access, RefreshToken: refresh}, nil
}

// Refresh 旋转刷新。已撤销 token 在宽限期内复用只报 401;超期复用视为泄露,撤销整个 family。
func (s *Service) Refresh(refreshToken string) (*TokenPair, error) {
	now := time.Now()
	var rec model.RefreshToken
	err := s.db.Where("token_hash = ?", sha256Hex(refreshToken)).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if rec.RevokedAt != nil {
		if now.Sub(*rec.RevokedAt) > rotationGrace {
			// 泄露信号:撤销整个 family
			s.db.Model(&model.RefreshToken{}).
				Where("family_id = ? AND revoked_at IS NULL", rec.FamilyID).
				Update("revoked_at", now)
		}
		return nil, ErrTokenInvalid
	}
	if now.After(rec.ExpiresAt) {
		return nil, ErrTokenInvalid
	}
	// 原子旋转:并发刷新只有一个赢家
	r := s.db.Model(&model.RefreshToken{}).
		Where("id = ? AND revoked_at IS NULL", rec.ID).
		Update("revoked_at", now)
	if r.Error != nil {
		return nil, r.Error
	}
	if r.RowsAffected == 0 {
		return nil, ErrTokenInvalid // 输家:token 刚被另一请求旋转,走宽限逻辑
	}
	pair, err := s.issueTokens(rec.UserID, rec.FamilyID)
	if err != nil {
		return nil, err
	}
	// 标记指向关系,便于审计
	var newRec model.RefreshToken
	if err := s.db.Where("token_hash = ?", sha256Hex(pair.RefreshToken)).First(&newRec).Error; err == nil {
		s.db.Model(&model.RefreshToken{}).Where("id = ?", rec.ID).Update("replaced_by", newRec.ID)
	}
	return pair, nil
}

// Logout 撤销该 token 所在的整个 family(全端登出该会话链)。
func (s *Service) Logout(refreshToken string) error {
	var rec model.RefreshToken
	err := s.db.Where("token_hash = ?", sha256Hex(refreshToken)).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.db.Model(&model.RefreshToken{}).
		Where("family_id = ? AND revoked_at IS NULL", rec.FamilyID).
		Update("revoked_at", time.Now()).Error
}

// VerifyAccess 校验 access token,返回 userID。
func (s *Service) VerifyAccess(tokenString string) (int64, error) {
	tok, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("签名算法不符")
		}
		return s.jwtSecret, nil
	})
	if err != nil || !tok.Valid {
		return 0, ErrTokenInvalid
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return 0, ErrTokenInvalid
	}
	sub, _ := claims["sub"].(string)
	var id int64
	if _, err := fmt.Sscanf(sub, "%d", &id); err != nil || id <= 0 {
		return 0, ErrTokenInvalid
	}
	return id, nil
}
