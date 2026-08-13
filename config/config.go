// ai-form-backend - AGPL-3.0
// 环境变量与 AI 上游配置加载。密钥只经环境变量进入,绝不写入代码或仓库。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
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

	AIConfigPath string
	PromptsDir   string
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
		AIConfigPath: env("AI_CONFIG", "config/private/ai.yaml"),
		PromptsDir:   env("PROMPTS_DIR", "prompts/private"),
	}
	if c.JWTSecret == "" {
		return nil, fmt.Errorf("必须设置 JWT_SECRET")
	}
	if c.HashPepper == "" {
		return nil, fmt.Errorf("必须设置 HASH_PEPPER")
	}
	return c, nil
}

// ===== AI 上游配置 =====

type AIUpstream struct {
	BaseURL   string `yaml:"baseUrl"`
	APIKeyEnv string `yaml:"apiKeyEnv"`
	apiKey    string
}

func (u *AIUpstream) APIKey() string { return u.apiKey }

// SetAPIKey 供 LoadAI 与测试注入密钥。
func (u *AIUpstream) SetAPIKey(k string) { u.apiKey = k }

type AICandidate struct {
	Upstream string `yaml:"upstream"`
	Model    string `yaml:"model"`
}

type AICapability struct {
	Candidates  []AICandidate `yaml:"candidates"`
	Temperature float64       `yaml:"temperature"`
	MaxTokens   int           `yaml:"maxTokens"`
}

type AIConfig struct {
	Upstreams    map[string]*AIUpstream  `yaml:"upstreams"`
	Capabilities map[string]AICapability `yaml:"capabilities"`
}

func LoadAI(path string) (*AIConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 AI 配置 %s 失败: %w", path, err)
	}
	var c AIConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析 AI 配置失败: %w", err)
	}
	for name, up := range c.Upstreams {
		if up.BaseURL == "" {
			return nil, fmt.Errorf("上游 %s 缺少 baseUrl", name)
		}
		up.SetAPIKey(os.Getenv(up.APIKeyEnv))
		if up.apiKey == "" {
			return nil, fmt.Errorf("上游 %s 的密钥环境变量 %s 为空", name, up.APIKeyEnv)
		}
	}
	for cap, cc := range c.Capabilities {
		if len(cc.Candidates) == 0 {
			return nil, fmt.Errorf("能力 %s 没有配置候选上游", cap)
		}
		for _, cand := range cc.Candidates {
			if _, ok := c.Upstreams[cand.Upstream]; !ok {
				return nil, fmt.Errorf("能力 %s 引用了不存在的上游 %s", cap, cand.Upstream)
			}
		}
	}
	return &c, nil
}
