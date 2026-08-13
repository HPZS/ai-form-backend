// ai-form-backend - AGPL-3.0
// 提示词加载器。提示词是私有运行时配置(AGPL 公开仓库只含本加载器与示例占位),
// 目录结构: <PROMPTS_DIR>/<capability>.yaml → {version, system, user}
// system/user 均为 Go text/template,数据为 {Req(请求结构体) Today(YYYY-MM-DD)}。
package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

type PromptDef struct {
	Version string `yaml:"version"`
	System  string `yaml:"system"`
	User    string `yaml:"user"`

	sysTpl  *template.Template
	userTpl *template.Template
}

type PromptStore struct {
	prompts map[string]*PromptDef
}

var tplFuncs = template.FuncMap{
	// {{json .Req.Headers}} 紧凑序列化;{{jsonIndent .Req.Fields}} 带缩进
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"jsonIndent": func(v any) (string, error) {
		b, err := json.MarshalIndent(v, "", " ")
		return string(b), err
	},
}

// LoadPrompts 加载目录下全部 <capability>.yaml,并校验 capabilities 全覆盖。
func LoadPrompts(dir string, capabilities []string) (*PromptStore, error) {
	store := &PromptStore{prompts: map[string]*PromptDef{}}
	for _, cap := range capabilities {
		path := filepath.Join(dir, cap+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("能力 %s 缺少提示词文件 %s: %w", cap, path, err)
		}
		var def PromptDef
		if err := yaml.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("解析提示词 %s 失败: %w", path, err)
		}
		if def.Version == "" || def.System == "" || def.User == "" {
			return nil, fmt.Errorf("提示词 %s 缺少 version/system/user", path)
		}
		if def.sysTpl, err = template.New(cap + ".sys").Funcs(tplFuncs).Parse(def.System); err != nil {
			return nil, fmt.Errorf("提示词 %s system 模板错误: %w", path, err)
		}
		if def.userTpl, err = template.New(cap + ".user").Funcs(tplFuncs).Parse(def.User); err != nil {
			return nil, fmt.Errorf("提示词 %s user 模板错误: %w", path, err)
		}
		store.prompts[cap] = &def
	}
	return store, nil
}

type tplData struct {
	Req   any
	Today string
}

// Render 渲染某能力的 system/user 消息,返回消息与提示词版本。
func (s *PromptStore) Render(capability string, req any) (system, user, version string, err error) {
	def, ok := s.prompts[capability]
	if !ok {
		return "", "", "", fmt.Errorf("能力 %s 无提示词", capability)
	}
	// 本地时区"今天"(生成类能力的日期基准,勿用 UTC)
	data := tplData{Req: req, Today: time.Now().Format("2006-01-02")}
	var sb, ub bytes.Buffer
	if err := def.sysTpl.Execute(&sb, data); err != nil {
		return "", "", "", fmt.Errorf("渲染 system 失败: %w", err)
	}
	if err := def.userTpl.Execute(&ub, data); err != nil {
		return "", "", "", fmt.Errorf("渲染 user 失败: %w", err)
	}
	return sb.String(), ub.String(), def.Version, nil
}
