package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestTextStructureOptionalProtocol(t *testing.T) {
	var spec Spec
	for _, item := range Specs() {
		if item.Name == "compile_input" {
			spec = item
		}
	}
	req := &CompileInputReq{Meta: Meta{RequestID: "11111111-1111-4111-8111-111111111111"}, SourceKind: "text", SourceText: "姓名：甲\n城市：乙", SourceHash: strings.Repeat("a", 64)}
	base := `{"schemaVersion":"v1","sourceHash":"` + req.SourceHash + `","sourceKind":"text","entityName":"客户","records":[{"recordId":"1","fields":[{"name":"姓名","sourceRefs":[{"quote":"甲","occurrence":0}],"confidence":1,"explanation":"原值"},{"name":"城市","sourceRefs":[{"quote":"乙","occurrence":0}],"confidence":1,"explanation":"原值"}]}],"exclusions":[],"confidence":1,"explanation":"原始记录"`
	structure := `,"textStructure":{"schemaVersion":1,"kind":"literal-fields-v1","recordSeparator":"\n\n","fields":[{"name":"姓名","label":"姓名","delimiter":"：","suffix":"\n"},{"name":"城市","label":"城市","delimiter":"：","suffix":""}]}`
	if _, err := spec.Post(req, base+"}"); err != nil {
		t.Fatal("旧请求应保持兼容", err)
	}
	if _, err := spec.Post(req, base+structure+"}"); err == nil {
		t.Fatal("未请求的结构输出不应被采用")
	}
	req.StructureLearning = "literal-fields-v1"
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Post(req, base+structure+"}"); err != nil {
		t.Fatal(err)
	}
	for _, item := range Specs() {
		if item.Name == "audit_input_plan" {
			auditReq := &AuditInputPlanReq{Meta: req.Meta, SourceKind: req.SourceKind, SourceText: req.SourceText, SourceHash: req.SourceHash, Plan: json.RawMessage(base + structure + "}")}
			if _, err := item.Post(auditReq, `{"accepted":true,"issues":[]}`); err != nil {
				t.Fatal("带新结构的语义计划仍可独立审计", err)
			}
		}
		if item.Name == "repair_input_plan" {
			repairReq := &RepairInputPlanReq{Meta: req.Meta, SourceKind: req.SourceKind, SourceText: req.SourceText, SourceHash: req.SourceHash, StructureLearning: literalFieldsVersion, Plan: json.RawMessage(base + "}"), Audit: json.RawMessage(`{"accepted":false,"issues":[]}`)}
			if err := repairReq.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, err := item.Post(repairReq, base+structure+"}"); err != nil {
				t.Fatal("修复需要携带同一结构学习契约", err)
			}
		}
	}
	if _, err := spec.Post(req, base+`,"textStructure":null,"structureDiagnostic":"原文缺少可验证边界，需要语义理解"}`); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{
		base + "}",
		base + strings.Replace(structure, `"recordSeparator":"\n\n"`, `"recordSeparator":"甲"`, 1) + "}",
		base + strings.Replace(structure, `"label":"姓名"`, `"label":"别名"`, 1) + "}",
		base + strings.Replace(structure, `,"suffix":""`, "", 1) + "}",
		base + strings.Replace(structure, `"suffix":""`, `"suffix":null`, 1) + "}",
		base + strings.Replace(structure, `"kind":"literal-fields-v1"`, `"kind":"literal-fields-v1","source":"eval(1)"`, 1) + "}",
	} {
		if _, err := spec.Post(req, output); err == nil {
			t.Fatalf("非法字面结构应拒绝：%s", output)
		}
	}
	req.SourceKind = "json"
	if err := req.Validate(); err == nil {
		t.Fatal("JSON不能请求文本结构协议")
	}
}

func TestTextStructurePromptMatchesByokInstruction(t *testing.T) {
	prompts, err := LoadPrompts("../../prompts/private", []string{"compile_input", "repair_input_plan"})
	if err != nil {
		t.Fatal(err)
	}
	requests := map[string]Request{
		"compile_input":     &CompileInputReq{SourceKind: "text", StructureLearning: literalFieldsVersion},
		"repair_input_plan": &RepairInputPlanReq{SourceKind: "text", StructureLearning: literalFieldsVersion, Plan: json.RawMessage(`{}`), Audit: json.RawMessage(`{}`)},
	}
	for capability, req := range requests {
		_, user, version, err := prompts.Render(capability, req)
		if err != nil {
			t.Fatal(err)
		}
		at := strings.Index(user, "同时请求 literal-fields-v1 字面结构学习")
		if version != "v3" || at < 0 {
			t.Fatalf("%s 未展示可选结构契约", capability)
		}
		digest := sha256.Sum256([]byte(strings.TrimSpace(user[at:])))
		if hex.EncodeToString(digest[:]) != "0c876f20e676edd326d2b2c32e54f9290d4dda3b12ef225faf1504b841ed0b73" {
			t.Fatalf("%s 与 BYOK 正式条款不一致", capability)
		}
	}
	_, legacy, _, err := prompts.Render("compile_input", &CompileInputReq{SourceKind: "text"})
	if err != nil || strings.Contains(legacy, "同时请求 literal-fields-v1") {
		t.Fatal("旧客户端不应收到新增输出约定", err)
	}
}
