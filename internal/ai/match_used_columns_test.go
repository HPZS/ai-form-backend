// ai-form-backend - AGPL-3.0
package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUsedColumnsAcceptedAndReachesModel
//
// 2026-08-19 Shopee 取证包:dynamic 是**增量**匹配,插件只送新出现的字段,模型看不到
// 已经建好的映射,于是「数量」先给了商品属性区的「数量」、规格表出现后又给了「商品数量」,
// 「价格」同样两处。后果是同一份数据填两个地方,交接卡上出现两条一模一样的待办。
//
// usedColumns 把"这列已经有人要了"这个事实交给模型。它是**加请求字段**——
// 老后端严格解码会整体 400,所以 APIRevision 必须同时 +1(守则 #16)。
func TestUsedColumnsAcceptedAndReachesModel(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":null}]}`)(w, r)
	}))
	defer upstream.Close()
	router, _, _ := setupGateway(t, upstream)

	body := `{"requestId":"44444444-4444-4444-8444-444444444444","context":"dynamic","fields":[` +
		`{"index":0,"label":"数量","tag":"input","type":"text","name":"","placeholder":"请输入",` +
		`"context":"数量 | 商品属性完成：3 / 25 | 品牌"}` +
		`],"headers":["数量","价格"],"sampleRow":{"数量":"20、25","价格":"10、15"},` +
		`"usedColumns":["数量"]}`

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("带 usedColumns 的请求必须被接受,实际 %d: %s", w.Code, w.Body.String())
	}

	var chatReq struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &chatReq); err != nil {
		t.Fatalf("解析上游请求失败: %v (%s)", err, upstreamBody)
	}
	var prompt string
	for _, m := range chatReq.Messages {
		prompt += m.Content
	}
	if !strings.Contains(prompt, `"usedColumns"`) {
		t.Fatalf("usedColumns 应出现在送给模型的提示词里,实际: %s", prompt)
	}
}

// TestUsedColumnsOmittedWhenEmpty
//
// omitempty:initial 语境下没有"已占用"这回事,不该凭空多出一个空数组——
// 模型会把它当成一条真信号来读(而且计费键与提示词都因此变了形)。
func TestUsedColumnsOmittedWhenEmpty(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		chatOK(`{"mapping":[{"fieldIndex":0,"column":"数量"}]}`)(w, r)
	}))
	defer upstream.Close()
	router, _, _ := setupGateway(t, upstream)

	body := `{"requestId":"55555555-5555-4555-8555-555555555555","fields":[` +
		`{"index":0,"label":"数量","tag":"input","type":"text","name":"","placeholder":""}` +
		`],"headers":["数量"],"sampleRow":{"数量":"20"}}`

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("不带 usedColumns 的请求照旧被接受,实际 %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(upstreamBody, "usedColumns") {
		t.Fatalf("没有已占用列时不该出现这个键,实际: %s", upstreamBody)
	}
}

// TestUsedColumnsRespectsHeaderLimit 已占用列与 headers 用同一把尺子,不该另有一套上限。
func TestUsedColumnsRespectsHeaderLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(chatOK(`{"mapping":[]}`)))
	defer upstream.Close()
	router, _, _ := setupGateway(t, upstream)

	cols := make([]string, 101)
	for i := range cols {
		cols[i] = "c"
	}
	used, _ := json.Marshal(cols)
	body := `{"requestId":"66666666-6666-4666-8666-666666666666","fields":[],` +
		`"headers":["数量"],"sampleRow":{"数量":"20"},"usedColumns":` + string(used) + `}`

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/ai/match-columns", strings.NewReader(body)))
	if w.Code != 400 {
		t.Fatalf("超限的 usedColumns 必须被 400 拦下,实际 %d: %s", w.Code, w.Body.String())
	}
}
