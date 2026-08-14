// ai-form-backend - AGPL-3.0
package ai

// CapMeta 能力的展示元信息:管理台按中文名和说明呈现,key 只作次要标识。
type CapMeta struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// CapabilityMetas 与 Specs() 同源同序的能力清单;新增能力时两处同时登记。
func CapabilityMetas() []CapMeta {
	return []CapMeta{
		{"assess_page", "页面甄别", "判断当前页面是可录入的表单还是搜索栏,避免把数据填进搜索框"},
		{"analyze_form", "表单识别", "解析页面表单的字段结构与按钮(核心计费点)"},
		{"name_fields", "字段起名", "页面提取不出字段名(Input/file)时,按周边上下文给字段起可读名字"},
		{"pick_open_button", "找开表单按钮", "在列表页上挑出「新增/添加」按钮,自动打开录入表单"},
		{"pick_form", "表单选型", "多张已认识的表单中,判断这份数据该录进哪一张"},
		{"match_columns", "建立映射", "把数据列对应到表单字段(核心计费点)"},
		{"suggest_profile", "资料判型", "在已保存的资料方案中挑选适配当前数据的一个"},
		{"detect_grouping", "层级判断", "识别两层数据(上级/明细)结构,自动安排两步录入"},
		{"generate_rule", "生成规则", "为字段编写本地转换规则,写一次后每条数据本地秒算(计费点)"},
		{"generate_field", "生成字段", "按要求为每条数据生成/润色字段内容(计费点,按次计)"},
		{"explain_failure", "失败诊断", "分析录入失败的具体原因并给出修复建议"},
		{"classify_failure", "失败分诊", "快速判断失败类型,决定跳过还是重试"},
		{"parse_command", "指令解析", "把用户的一句大白话解析成字段填法修改"},
	}
}
