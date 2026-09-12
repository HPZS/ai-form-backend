package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 评测证据仅含正式协议和模型内容，不记录请求认证头、环境变量或密钥。
type evalTrace struct {
	Secret        string           `json:"-"`
	Path          string           `json:"-"`
	Capability    string           `json:"capability"`
	Payload       json.RawMessage  `json:"payload"`
	Model         string           `json:"model"`
	PromptVersion string           `json:"promptVersion"`
	StartedAt     string           `json:"startedAt"`
	Attempts      []map[string]any `json:"attempts"`
	Result        any              `json:"result,omitempty"`
	Error         string           `json:"error,omitempty"`
}

func newEvalTrace(directory string) (*evalTrace, error) {
	if directory == "" {
		return nil, nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(directory, "model-attempts-*.json")
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(f.Name())
	if err != nil {
		return nil, err
	}
	return &evalTrace{Path: path, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Attempts: []map[string]any{}}, nil
}

func (trace *evalTrace) save() error {
	if trace == nil {
		return nil
	}
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		return err
	}
	if trace.Secret != "" {
		encoded, _ := json.Marshal(trace.Secret)
		data = bytes.ReplaceAll(data, encoded[1:len(encoded)-1], []byte("[REDACTED]"))
	}
	return os.WriteFile(trace.Path, data, 0600)
}

func (trace *evalTrace) finish(runErr *error) {
	if trace == nil {
		return
	}
	if *runErr != nil {
		trace.Error = (*runErr).Error()
	}
	if err := trace.save(); err != nil {
		if *runErr == nil {
			*runErr = fmt.Errorf("真实模型评测证据写入失败: %w", err)
		} else {
			*runErr = fmt.Errorf("%w；评测证据写入失败: %v", *runErr, err)
		}
	}
}
