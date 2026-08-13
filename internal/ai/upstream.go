// ai-form-backend - AGPL-3.0
// AI 上游调用:OpenAI 兼容协议 + 按序故障切换 + 冷却熔断(技术方案 v3 §6.6)。
// 网络错误/超时/429/5xx/401/403 → 换下一候选;连续失败 3 次冷却 60 秒;
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

	"github.com/HPZS/ai-form-backend/config"
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
	cfg  *config.AIConfig
	http *http.Client

	mu       sync.Mutex
	breakers map[string]*breakerState // key = upstream 名
}

func NewCaller(cfg *config.AIConfig) *Caller {
	return &Caller{
		cfg: cfg,
		http: &http.Client{
			Timeout: callTimeout,
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
				TLSHandshakeTimeout: dialTimeout,
			},
		},
		breakers: map[string]*breakerState{},
	}
}

func (c *Caller) cooling(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.breakers[name]
	return ok && time.Now().Before(b.coolUntil)
}

func (c *Caller) markFail(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.breakers[name]
	if b == nil {
		b = &breakerState{}
		c.breakers[name] = b
	}
	b.fails++
	if b.fails >= coolAfterFails {
		b.coolUntil = time.Now().Add(coolDuration)
		b.fails = 0 // 冷却到期后放行探活,重新计数
	}
}

func (c *Caller) markOK(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.breakers, name)
}

// Call 按能力的候选链调用,自动切换。retrySameOnce 时对解析失败等场景由调用方自行重调。
func (c *Caller) Call(ctx context.Context, capability string, messages []ChatMessage) (*CallResult, error) {
	cc, ok := c.cfg.Capabilities[capability]
	if !ok {
		return nil, fmt.Errorf("能力 %s 未配置上游", capability)
	}
	var lastErr error
	for _, cand := range cc.Candidates {
		if c.cooling(cand.Upstream) {
			continue
		}
		res, err := c.callOnce(ctx, cand, cc, messages)
		if err == nil {
			c.markOK(cand.Upstream)
			return res, nil
		}
		c.markFail(cand.Upstream)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有候选均在冷却中")
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

func (c *Caller) callOnce(ctx context.Context, cand config.AICandidate, cc config.AICapability, messages []ChatMessage) (*CallResult, error) {
	up := c.cfg.Upstreams[cand.Upstream]
	body, err := json.Marshal(chatRequest{
		Model:       cand.Model,
		Messages:    messages,
		Temperature: cc.Temperature,
		MaxTokens:   cc.MaxTokens,
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
	req.Header.Set("Authorization", "Bearer "+up.APIKey())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上游 %s 请求失败: %w", cand.Upstream, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("上游 %s 读响应失败: %w", cand.Upstream, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游 %s 返回 %d: %.200s", cand.Upstream, resp.StatusCode, string(data))
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return nil, fmt.Errorf("上游 %s 响应格式异常: %w", cand.Upstream, err)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("上游 %s 返回空 choices", cand.Upstream)
	}
	return &CallResult{
		Content:      cr.Choices[0].Message.Content,
		Upstream:     cand.Upstream,
		Model:        cand.Model,
		InputTokens:  cr.Usage.PromptTokens,
		OutputTokens: cr.Usage.CompletionTokens,
	}, nil
}
