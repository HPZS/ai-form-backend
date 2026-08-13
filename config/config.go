// ai-form-backend - AGPL-3.0
// 环境变量与 AI 上游配置加载。密钥只经环境变量进入,绝不写入代码或仓库。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr     string
	TrustedProxies []string
	CallbackAddr   string // 易支付回调基址
	ConsoleAddr    string // 支付完成跳回地址

	DBDSN string // 空 = 本地 sqlite(仅开发)

	JWTSecret  string
	HashPepper string

	AdminEmails []string

	SMTP SMTPConfig
	Epay EpayConfig

	PromptsDir string
}

type SMTPConfig struct {
	Server     string
	Port       int
	Account    string
	From       string
	Token      string
	SSL        bool
	SystemName string
}

type EpayConfig struct {
	URL string
	PID string
	Key string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func Load() (*Config, error) {
	port, err := strconv.Atoi(env("SMTP_PORT", "465"))
	if err != nil {
		return nil, fmt.Errorf("SMTP_PORT 不是数字: %w", err)
	}
	c := &Config{
		ListenAddr:     env("LISTEN_ADDR", ":8080"),
		TrustedProxies: splitCSV(os.Getenv("TRUSTED_PROXIES")),
		CallbackAddr:   strings.TrimRight(env("CALLBACK_ADDRESS", "http://localhost:8080"), "/"),
		ConsoleAddr:    strings.TrimRight(env("CONSOLE_ADDRESS", "http://localhost:8080"), "/"),
		DBDSN:          os.Getenv("DB_DSN"),
		JWTSecret:      os.Getenv("JWT_SECRET"),
		HashPepper:     os.Getenv("HASH_PEPPER"),
		AdminEmails:    splitCSV(strings.ToLower(os.Getenv("ADMIN_EMAILS"))),
		SMTP: SMTPConfig{
			Server:     os.Getenv("SMTP_SERVER"),
			Port:       port,
			Account:    os.Getenv("SMTP_ACCOUNT"),
			From:       os.Getenv("SMTP_FROM"),
			Token:      os.Getenv("SMTP_TOKEN"),
			SSL:        env("SMTP_SSL", "true") == "true",
			SystemName: env("SYSTEM_NAME", "AI智能录入助手"),
		},
		Epay: EpayConfig{
			URL: os.Getenv("EPAY_URL"),
			PID: os.Getenv("EPAY_PID"),
			Key: os.Getenv("EPAY_KEY"),
		},
		PromptsDir: env("PROMPTS_DIR", "prompts/private"),
	}
	if c.JWTSecret == "" {
		return nil, fmt.Errorf("必须设置 JWT_SECRET")
	}
	if c.HashPepper == "" {
		return nil, fmt.Errorf("必须设置 HASH_PEPPER")
	}
	return c, nil
}

// AI 上游与能力模型参数不在此处:它们存数据库,由管理台维护(密钥常换,即改即生效)。
