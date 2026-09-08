package ai

import (
	"strings"
	"testing"
)

func TestMatchRepresentativeSamples(t *testing.T) {
	req := &MatchColumnsReq{Meta: Meta{RequestID: "44444444-4444-4444-8444-444444444444"}, Headers: []string{"姓名"}, SampleRows: []map[string]string{{"姓名": ""}, {"姓名": "甲"}}}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	store, err := LoadPrompts("../../prompts/private", []string{"match_columns"})
	if err != nil {
		t.Fatal(err)
	}
	_, user, _, err := store.Render("match_columns", req)
	if err != nil || !strings.Contains(user, "甲") || !strings.Contains(user, "多条真实记录") {
		t.Fatalf("样本未进入提示词: %v", err)
	}
	req.SampleRows = make([]map[string]string, 6)
	if req.Validate() == nil {
		t.Fatal("样本数量超限应拒绝")
	}
}
