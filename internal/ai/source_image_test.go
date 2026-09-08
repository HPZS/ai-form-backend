package ai

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestSourceImageBoundToRealImage(t *testing.T) {
	data := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="
	req := &SourceImageReq{Meta: Meta{RequestID: "44444444-4444-4444-8444-444444444444"}, SchemaVersion: "v1", SourceHash: fmt.Sprintf("%x", sha256.Sum256([]byte(data))), ImageDataURL: data, Purpose: "extract"}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	spec := sourceImageSpec()
	valid := fmt.Sprintf(`{"schemaVersion":"v1","sourceHash":"%s","regions":[{"id":"r1","text":"001","x":0,"y":0,"width":1,"height":1,"uncertain":false}],"explanation":""}`, req.SourceHash)
	if _, err := spec.Post(req, valid); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Post(req, strings.Replace(valid, `"width":1`, `"width":2`, 1)); err == nil {
		t.Fatal("越界区域不能接受")
	}
	if _, err := spec.Post(req, strings.Replace(valid, req.SourceHash, "wrong", 1)); err == nil {
		t.Fatal("来源版本错配不能接受")
	}
	if spec.PriceFor(req, 30) != 0 {
		t.Fatal("内部资料识别不得新增收费")
	}
	messages, err := spec.Messages(req, "system", "读取")
	if err != nil || messages[1].ContentParts[1].ImageURL.Detail != "high" {
		t.Fatal("未发送真实图像")
	}
	req.SourceHash = "wrong"
	if req.Validate() == nil {
		t.Fatal("图像内容与版本不符必须拒绝")
	}
}
