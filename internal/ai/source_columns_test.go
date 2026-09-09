package ai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWideColumnsReachMappingModel(t *testing.T) {
	var prompt string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		prompt = string(body)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"raw159"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, _, _ := setupGateway(t, upstream)
	headers := make([]string, 160)
	row := map[string]string{}
	for i := range headers {
		headers[i] = fmt.Sprintf("raw%d", i)
		row[headers[i]] = fmt.Sprint(i)
	}
	body, err := json.Marshal(map[string]any{"requestId": "77777777-7777-4777-8777-777777777777", "headers": headers, "sampleRow": row, "fields": []FormFieldBrief{{Index: 0, Label: "编号", Type: "text", Tag: "input"}}})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(string(body))))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "raw159") || !strings.Contains(prompt, "raw159") {
		t.Fatalf("末列必须送达模型并保留映射，HTTP %d: %s", w.Code, w.Body.String())
	}
}

// 宽资料必须贯穿列名、样本行和条件规则，不能导入后再被某个接口的旧上限拦住。
func TestWideSourceColumns(t *testing.T) {
	for _, count := range []int{101, 160, 1024, 1025} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			headers := make([]string, count)
			row := map[string]string{}
			counts := map[string]int{}
			values := map[string][]string{}
			for i := range headers {
				headers[i] = fmt.Sprintf("raw%d", i)
				row[headers[i]] = fmt.Sprint(i)
				counts[headers[i]] = 1
				values[headers[i]] = []string{fmt.Sprint(i)}
			}
			meta := Meta{RequestID: "44444444-4444-4444-8444-444444444444"}
			checks := map[string]func() error{
				"headers":     func() error { return checkHeaders(headers) },
				"row":         func() error { return checkRow("row", row, 200) },
				"rule":        (&GenerateRuleReq{Meta: meta, Headers: headers, SampleRows: []map[string]string{row}}).Validate,
				"instruction": (&ParseCommandReq{Meta: meta, Headers: headers}).Validate,
				"grouping":    (&DetectGroupingReq{Meta: meta, Headers: headers, DistinctCounts: counts, SampleRows: []map[string]string{row}}).Validate,
				"identity":    (&DetectIdentityReq{Meta: meta, Headers: headers, ValueCounts: counts, SampleValues: values}).Validate,
			}
			for name, check := range checks {
				if err := check(); (err == nil) != (count <= 1024) {
					t.Errorf("%s: %d 列校验结果不正确: %v", name, count, err)
				}
			}
		})
	}
}
