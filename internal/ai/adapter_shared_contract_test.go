package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSharedAdapterContractVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/adapter-contract-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	// 与插件 tests/fixtures 的固定向量相同，单仓 CI 不依赖另一 checkout。
	if fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))) != "d18bb5da53c0875e42fa82ae5ff79d697cf79cf8f860c20eb2b7887e6efb3d42" {
		t.Fatal("共享协议向量变化，必须同步前后端版本与两侧测试")
	}
	var vectors struct {
		Request          CompileAdapterReq `json:"request"`
		Response         json.RawMessage   `json:"response"`
		InvalidResponses []struct {
			Name  string `json:"name"`
			Path  []any  `json:"path"`
			Value any    `json:"value"`
		} `json:"invalidResponses"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if err := vectors.Request.Validate(); err != nil {
		t.Fatal(err)
	}
	valid, err := validateCompileAdapterOutput(&vectors.Request, string(vectors.Response))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(valid.Modules[0].Source, `kind:"tool"`) || valid.Effects[0].Progress == nil {
		t.Fatal("共享向量未覆盖业务工具和数值进展")
	}
	for _, test := range vectors.InvalidResponses {
		t.Run(test.Name, func(t *testing.T) {
			var response any
			if err := json.Unmarshal(vectors.Response, &response); err != nil {
				t.Fatal(err)
			}
			current := response
			for _, part := range test.Path[:len(test.Path)-1] {
				switch key := part.(type) {
				case string:
					current = current.(map[string]any)[key]
				case float64:
					current = current.([]any)[int(key)]
				}
			}
			switch key := test.Path[len(test.Path)-1].(type) {
			case string:
				current.(map[string]any)[key] = test.Value
			case float64:
				current.([]any)[int(key)] = test.Value
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateCompileAdapterOutput(&vectors.Request, string(encoded)); err == nil {
				t.Fatal("接受共享非法向量")
			}
		})
	}
	for _, phase := range []string{"", "runtime", "source-analysis"} {
		meta := Meta{RequestID: vectors.Request.RequestID, CallPhase: phase}
		if err := meta.validateMeta(); err != nil {
			t.Fatalf("旧客户端或正常调用阶段不兼容: %v", err)
		}
	}
	vectors.Request.Parameters = make([]AdapterParameter, 65)
	for i := range vectors.Request.Parameters {
		vectors.Request.Parameters[i] = AdapterParameter{ID: fmt.Sprintf("parameter%d", i), Type: "string"}
	}
	if vectors.Request.Validate() == nil {
		t.Fatal("参数上限必须与宿主64一致")
	}
}
