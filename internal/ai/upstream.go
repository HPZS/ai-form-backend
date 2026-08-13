// ai-form-backend - AGPL-3.0
// AI 上游调用:OpenAI 兼容协议 + 按序故障切换 + 冷却熔断(技术方案 v3 §6.6)。
// 上游列表与能力模型参数存数据库(管理台增删改,即时生效,不需重启)。
// 网络错误/超时/429/5xx/401/403 → 换下一上游;连续失败 3 次冷却 60 秒;
// 输出质量问题(JSON 不合规)不在本层处理,不触发切换。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/HPZS/ai-form-backend/internal/model"
)

var ErrAllUpstreamsDown = errors.New("全部 AI 上游不可用")

const (
	coolAfterFails = 3
	coolDuration   = 60 * time.Second
	callTimeout    = 60 * time.Second
	dialTimeout    = 5 * time.Second
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CallResult struct {
	Content      string
	Upstream     string
	Model        string
	InputTokens  int
	OutputTokens int
}

type breakerState struct {
	fails     int
	coolUntil time.Time
}

type Caller struct {
	db   *gorm.DB
	http *http.Client

	mu       sync.Mutex
	breakers map[int64]*breakerState // key = 上游 ID
}

func NewCaller(db *gorm.DB) *Caller {
	return &Caller{
		db: db,
		http: &http.Client{
			Timeout: callTimeout,
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
				TLSHandshakeTimeout: dialTimeout,
			},
		},
		breakers: map[int64]*breakerState{},
	}
}

func (c *Caller) cooling(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.breakers[id]
	return ok && time.Now().Before(b.coolUntil)
}

func (c *Caller) markFail(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.breakers[id]
	if b == nil {
		b = &breakerState{}
		c.breakers[id] = b
	}
	b.fails++
	if b.fails >= coolAfterFails {
		b.coolUntil = time.Now().Add(coolDuration)
		b.fails = 0 // 冷却到期后放行探活,重新计数
	}
}

func (c *Caller) markOK(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.breakers, id)
}

// Call 按启用上游的优先级顺序尝试,自动故障切换。
func (c *Caller) Call(ctx context.Context, capability string, messages []ChatMessage) (*CallResult, error) {
	var cap model.CapabilityPrice
	if err := c.db.Where("capability = ?", capability).First(&cap).Error; err != nil {
		return nil, fmt.Errorf("能力 %s 未配置", capability)
	}
	if cap.Model == "" {
		return nil, fmt.Errorf("能力 %s 未配置模型,请在管理台设置", capability)
	}
	var ups []model.AIUpstream
	if err := c.db.Where("enabled = ?", true).Order("sort_order asc, id asc").Find(&ups).Error; err != nil {
		return nil, err
	}
	if len(ups) == 0 {
		return nil, fmt.Errorf("%w: 没有启用的 AI 上游,请在管理台添加", ErrAllUpstreamsDown)
	}
	var lastErr error
	for _, up := range ups {
		if c.cooling(up.ID) {
			continue
		}
		res, err := c.callOnce(ctx, up, cap, messages)
		if err == nil {
			c.markOK(up.ID)
			return res, nil
		}
		c.markFail(up.ID)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有上游均在冷却中")
	}
	return nil, fmt.Errorf("%w: %v", ErrAllUpstreamsDown, lastErr)
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Caller) callOnce(ctx context.Context, up model.AIUpstream, cap model.CapabilityPrice, messages []ChatMessage) (*CallResult, error) {
	body, err := json.Marshal(chatRequest{
		Model:       cap.Model,
		Messages:    messages,
		Temperature: cap.Temperature,
		MaxTokens:   cap.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(up.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+up.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上游 %s 请求失败: %w", up.Name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("上游 %s 读响应失败: %w", up.Name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游 %s 返回 %d: %.200s", up.Name, resp.StatusCode, string(data))
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return nil, fmt.Errorf("上游 %s 响应格式异常: %w", up.Name, err)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("上游 %s 返回空 choices", up.Name)
	}
	return &CallResult{
		Content:      cr.Choices[0].Message.Content,
		Upstream:     up.Name,
		Model:        cap.Model,
		InputTokens:  cr.Usage.PromptTokens,
		OutputTokens: cr.Usage.CompletionTokens,
	}, nil
}
