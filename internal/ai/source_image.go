package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// 资料图像与页面截图分开授权、校验；不能对资料套用页面敏感控件遮罩。
type SourceImageReq struct {
	Meta
	SchemaVersion string `json:"schemaVersion"`
	SourceHash    string `json:"sourceHash"`
	ImageDataURL  string `json:"imageDataUrl"`
	Purpose       string `json:"purpose"`
}

func (r *SourceImageReq) Validate() error {
	if err := r.validateMeta(); err != nil {
		return err
	}
	if r.SchemaVersion != "v1" || (r.Purpose != "extract" && r.Purpose != "verify") {
		return fmt.Errorf("资料图像版本或读取用途无效")
	}
	encoded := ""
	for _, prefix := range []string{"data:image/png;base64,", "data:image/jpeg;base64,"} {
		if strings.HasPrefix(r.ImageDataURL, prefix) {
			encoded = strings.TrimPrefix(r.ImageDataURL, prefix)
		}
	}
	if encoded == "" || len(encoded) > 720*1024 {
		return fmt.Errorf("资料图像仅支持限额内的 PNG/JPEG")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > 540*1024 {
		return fmt.Errorf("资料图像编码无效或超限")
	}
	sum := sha256.Sum256([]byte(r.ImageDataURL))
	if r.SourceHash != fmt.Sprintf("%x", sum) {
		return fmt.Errorf("资料图像版本与内容不一致")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 16_000_000 {
		return fmt.Errorf("资料图像像素无效或超限")
	}
	return nil
}

type SourceImageRegion struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
	Uncertain bool    `json:"uncertain"`
}
type SourceImageOutput struct {
	SchemaVersion string              `json:"schemaVersion"`
	SourceHash    string              `json:"sourceHash"`
	Regions       []SourceImageRegion `json:"regions"`
	Explanation   string              `json:"explanation"`
}

func sourceImageSpec() Spec {
	return Spec{Name: "read_source_image", NewReq: func() Request { return &SourceImageReq{} },
		Messages: func(req Request, system, user string) ([]ChatMessage, error) {
			r := req.(*SourceImageReq)
			return []ChatMessage{{Role: "system", Content: system}, {Role: "user", ContentParts: []ChatContentPart{{Type: "text", Text: user}, {Type: "image_url", ImageURL: &ChatImageURL{URL: r.ImageDataURL, Detail: "high"}}}}}, nil
		},
		// 资料识别属于自动录入内部成本，计量照常，不新增用户收费。
		PriceFor: func(_ Request, _ int64) int64 { return 0 },
		Post: func(req Request, content string) (any, error) {
			var output SourceImageOutput
			raw, err := extractJSONObject(content)
			if err != nil {
				return nil, fmt.Errorf("资料图像输出不是合法 JSON")
			}
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&output); err != nil {
				return nil, fmt.Errorf("资料图像输出协议无效")
			}
			var shape struct {
				Regions []map[string]json.RawMessage `json:"regions"`
			}
			if err := json.Unmarshal([]byte(raw), &shape); err != nil {
				return nil, fmt.Errorf("资料图像输出协议无效")
			}
			for _, region := range shape.Regions {
				for _, key := range []string{"id", "text", "x", "y", "width", "height", "uncertain"} {
					if value, exists := region[key]; !exists || string(value) == "null" {
						return nil, fmt.Errorf("资料图像区域缺少必需事实")
					}
				}
			}
			if output.SchemaVersion != "v1" || output.SourceHash != req.(*SourceImageReq).SourceHash || len(output.Regions) > 160 {
				return nil, fmt.Errorf("资料图像输出版本或区域数量无效")
			}
			ids := map[string]bool{}
			total := 0
			for _, region := range output.Regions {
				total += len(region.Text)
				if !taskCallID.MatchString(region.ID) || ids[region.ID] || strings.TrimSpace(region.Text) == "" || len(region.Text) > 12000 || region.X < 0 || region.Y < 0 || region.Width <= 0 || region.Height <= 0 || region.X+region.Width > 1.001 || region.Y+region.Height > 1.001 {
					return nil, fmt.Errorf("资料图像区域内容或范围无效")
				}
				ids[region.ID] = true
			}
			if total > 120*1024 || len(output.Explanation) > 2000 {
				return nil, fmt.Errorf("资料图像识别文本超限")
			}
			return output, nil
		},
	}
}
