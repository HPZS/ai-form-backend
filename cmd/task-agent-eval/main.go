// task-agent-eval 使用生产提示词和输出校验运行真实模型评测，不接触业务数据库或计费账户。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/HPZS/ai-form-backend/internal/ai"
)

func run() error {
	var input struct {
		Capability string          `json:"capability"`
		Payload    json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1024*1024)).Decode(&input); err != nil {
		return err
	}
	var spec *ai.Spec
	for _, candidate := range ai.Specs() {
		if candidate.Name == input.Capability {
			copy := candidate
			spec = &copy
			break
		}
	}
	if spec == nil {
		return fmt.Errorf("未知评测能力")
	}
	req := spec.NewReq()
	dec := json.NewDecoder(bytes.NewReader(input.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	dir := os.Getenv("PROMPTS_DIR")
	if dir == "" {
		dir = "prompts/private"
	}
	prompts, err := ai.LoadPrompts(dir, []string{spec.Name})
	if err != nil {
		return err
	}
	system, user, version, err := prompts.Render(spec.Name, req)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(os.Getenv("AIFORM_UPSTREAM_URL"), "/")
	key, model := os.Getenv("AIFORM_UPSTREAM_KEY"), os.Getenv("AIFORM_EVAL_MODEL")
	if !strings.HasPrefix(endpoint, "https://") || key == "" || model == "" {
		return fmt.Errorf("需要 HTTPS 上游地址、评测密钥和模型配置")
	}
	messages := []ai.ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}
	started := time.Now()
	var result any
	temperature, maxTokens := ai.CapabilityGenerationParams(spec.Name)
	attempts, promptTokens, completionTokens := 0, 0, 0
	for attempt := 0; attempt < 2; attempt++ {
		attempts++
		payload, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "temperature": temperature, "max_tokens": maxTokens})
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			cancel()
			return err
		}
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			cancel()
			return fmt.Errorf("模型连接失败: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
		response.Body.Close()
		cancel()
		if err != nil {
			return err
		}
		if response.StatusCode != 200 {
			return fmt.Errorf("模型接口 HTTP %d", response.StatusCode)
		}
		var completion struct {
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &completion); err != nil || len(completion.Choices) == 0 {
			return fmt.Errorf("模型没有返回有效内容")
		}
		if completion.Choices[0].FinishReason == "length" {
			return fmt.Errorf("模型输出被截断")
		}
		promptTokens += completion.Usage.PromptTokens
		completionTokens += completion.Usage.CompletionTokens
		result, err = spec.Post(req, completion.Choices[0].Message.Content)
		if err == nil {
			break
		}
		if attempt == 1 {
			return fmt.Errorf("两次模型输出均未通过生产协议校验: %w", err)
		}
		messages = append(messages, ai.ChatMessage{Role: "user", Content: "上次结果未通过协议校验，请重新检查工具参数、上下文绑定和互斥状态，返回符合定义的 JSON。"})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var output map[string]any
	if err = json.Unmarshal(encoded, &output); err != nil {
		return err
	}
	output["meta"] = map[string]any{"capability": spec.Name, "promptVersion": version, "schemaVersion": "v1", "model": model, "latencyMs": time.Since(started).Milliseconds(), "evaluation": true, "attempts": attempts, "promptTokens": promptTokens, "completionTokens": completionTokens, "temperature": temperature, "maxTokens": maxTokens}
	return json.NewEncoder(os.Stdout).Encode(output)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
