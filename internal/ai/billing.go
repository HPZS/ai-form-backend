// ai-form-backend - AGPL-3.0
// 计费组派生(积分与订阅计费方案 §6.2/§6.4):计费键由服务端从请求内容算出,
// 不依赖客户端诚实;同一方案/同一格天然只扣一次,跨日跨任务依然防重
// (落到 credit_ledgers 的 uq_ledger_billing_group 部分唯一索引兜底并发)。
package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// hashGroup 计算带版本前缀的计费组 id(列宽 64,"m1:"+40 hex 充分防碰撞)。
func hashGroup(prefix string, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return prefix + ":" + hex.EncodeToString(h[:])[:40]
}

// matchColumnsBillingGroup 方案费计费键 = hash(userID + 表单字段签名 + 列签名)。
// 字段签名用 label/name 的排序集合(对字段顺序与 index 漂移不敏感),列签名用排序后的列名集合:
// 同一张表单 × 同一种资料 = 同一方案,永不重复收费。
func matchColumnsBillingGroup(userID int64, r *MatchColumnsReq) string {
	fieldSig := make([]string, 0, len(r.Fields))
	for _, f := range r.Fields {
		fieldSig = append(fieldSig, f.Label+"\x00"+f.Name)
	}
	sort.Strings(fieldSig)
	headers := append([]string(nil), r.Headers...)
	sort.Strings(headers)
	return hashGroup("m1", fmt.Sprintf("%d", userID),
		strings.Join(fieldSig, "\x01"), strings.Join(headers, "\x01"))
}

// generateFieldBillingGroup AI 生成格计费键 = hash(userID + 字段 + 行内容指纹):
// 同一行同一字段重新生成,经计费组防重自动免单。
func generateFieldBillingGroup(userID int64, r *GenerateFieldReq) string {
	keys := make([]string, 0, len(r.Row))
	for k := range r.Row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rowSig := make([]string, 0, len(keys))
	for _, k := range keys {
		rowSig = append(rowSig, k+"\x00"+r.Row[k])
	}
	return hashGroup("g1", fmt.Sprintf("%d", userID),
		r.Field.Label+"\x00"+r.Field.Name, strings.Join(rowSig, "\x01"))
}
