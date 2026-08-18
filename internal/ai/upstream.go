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
	"log"
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
	Content string
	// 输出被 maxTokens 截断(finish_reason=length)。JSON 类能力靠解析失败自保,
	// 纯文本能力没有这道保险——半句话会被当成完整答案填进业务系统,必须显式判无效。
	Truncated    bool
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

// markFail 记一次失败;返回是否因此进入冷却(调用方据此告警)。
func (c *Caller) markFail(id int64) bool {
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
		return true
	}
	return false
}

func (c *Caller) markOK(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.breakers, id)
}

// 温度与 maxTokens 由代码按能力定死,不是运营配置项:
// 温度——结构化输出一律 0(确定性),只有生成/润色字段内容要一点随机性;
// maxTokens——取决于各能力输出结构的体量,配小了会截断 JSON 造成故障。
var capTemperature = map[string]float64{
	"generate_field": 0.3,
}

const defaultMaxTokens = 2000

// 每个能力都必须登记(有 TestCapMaxTokensCoverage 把关):漏登记会静默落到默认值,
// match_columns 就曾因此在宽表单上必然截断 JSON,重试再截断,陷入 422 死循环。
var capMaxTokens = map[string]int{
	"assess_page":      300,
	"analyze_form":     500,
	"pick_open_button": 500,
	"pick_form":        500,
	"suggest_profile":  500,
	"detect_grouping":  800,
	// 只回一个字段名加一句理由(理由服务端截到 60 字),输出天然很小
	"detect_identity": 300,
	// 输出体量最大的能力:上限 200 字段,每条 mapping 约 30~40 token
	"match_columns":    8000,
	"generate_rule":    4000,
	"generate_field":   1000,
	"explain_failure":  1000,
	"classify_failure": 300,
	"parse_command":    1500,
}

// callParams 本次调用实际生效的模型参数:模型可按能力覆盖,未覆盖用全局默认。
type callParams struct {
	Model       string
	Temperature float64
	MaxTokens   int
}

// resolveParams 合成生效参数;全局默认与能力覆盖都没配模型时报错(不静默降级)。
func resolveParams(def model.AIDefault, cap model.CapabilityPrice) (callParams, error) {
	p := callParams{Model: def.Model, Temperature: capTemperature[cap.Capability], MaxTokens: defaultMaxTokens}
	if cap.Model != "" {
		p.Model = cap.Model
	}
	if mt, ok := capMaxTokens[cap.Capability]; ok {
		p.MaxTokens = mt
	}
	if p.Model == "" {
		return p, fmt.Errorf("未配置默认模型,请在管理台「能力配置」设置")
	}
	return p, nil
}

// Call 按启用上游的优先级顺序尝试,自动故障切换。
func (c *Caller) Call(ctx context.Context, capability string, messages []ChatMessage) (*CallResult, error) {
	var cap model.CapabilityPrice
	if err := c.db.Where("capability = ?", capability).First(&cap).Error; err != nil {
		return nil, fmt.Errorf("能力 %s 未配置", capability)
	}
	var def model.AIDefault
	if err := c.db.First(&def, 1).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	params, err := resolveParams(def, cap)
	if err != nil {
		return nil, err
	}
	var ups []model.AIUpstream
	if err := c.db.Where("enabled = ?", true).Order("sort_order asc, id asc").Find(&ups).Error; err != nil {
		return nil, err
	}
	if len(ups) == 0 {
		return nil, fmt.Errorf("%w: 没有启用的 AI 上游,请在管理台添加", ErrAllUpstreamsDown)
	}
	// 切换过程必须全程留痕:跳过谁、谁失败了、谁被熔断——这三件事此前一件都不落日志,
	// 线上"AI 时快时慢/偶发 503"完全无从复盘(守则 3.2)。
	// 错因也要逐家收集:只留最后一家的 lastErr 会把前面几家的真实原因
	//(401 密钥失效 / 超时 / 响应格式异常)全部丢掉,而它们往往才是根因。
	var errs []string
	for _, up := range ups {
		if c.cooling(up.ID) {
			log.Printf("[AI-UPSTREAM] 跳过冷却中的上游 name=%s capability=%s", up.Name, capability)
			errs = append(errs, fmt.Sprintf("上游 %s 冷却中", up.Name))
			continue
		}
		res, err := c.callOnce(ctx, up, params, messages)
		if err == nil {
			c.markOK(up.ID)
			return res, nil
		}
		log.Printf("[AI-UPSTREAM] 上游调用失败,切换下一家 name=%s capability=%s err=%v", up.Name, capability, err)
		if c.markFail(up.ID) {
			log.Printf("[AI-UPSTREAM-ALERT] 上游连续失败 %d 次,进入 %v 冷却 name=%s", coolAfterFails, coolDuration, up.Name)
		}
		errs = append(errs, err.Error())
	}
	log.Printf("[AI-UPSTREAM-ALERT] 全部上游不可用 capability=%s 上游数=%d 错因=%s",
		capability, len(ups), strings.Join(errs, " | "))
	return nil, fmt.Errorf("%w: %s", ErrAllUpstreamsDown, strings.Join(errs, " | "))
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"` // 上游实际服务的模型:聚合网关可能静默换模
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Caller) callOnce(ctx context.Context, up model.AIUpstream, params callParams, messages []ChatMessage) (*CallResult, error) {
	body, err := json.Marshal(chatRequest{
		Model:       params.Model,
		Messages:    messages,
		Temperature: params.Temperature,
		MaxTokens:   params.MaxTokens,
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
	// 上游换了模型:JSON 能力的输出质量与计量归因都会跟着变,审计字段记的是"意愿"而非事实
	if cr.Model != "" && cr.Model != params.Model {
		log.Printf("[AI] 上游 %s 实际服务的模型与请求不符 请求=%s 实际=%s", up.Name, params.Model, cr.Model)
	}
	return &CallResult{
		Content:      cr.Choices[0].Message.Content,
		Truncated:    cr.Choices[0].FinishReason == "length",
		Upstream:     up.Name,
		Model:        params.Model,
		InputTokens:  cr.Usage.PromptTokens,
		OutputTokens: cr.Usage.CompletionTokens,
	}, nil
}
