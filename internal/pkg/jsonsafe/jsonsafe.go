// Package jsonsafe 对 `encoding/json` 解到 `any` 之后的 `map[string]any` 做安全取值。
//
// 唯一纪律：**缺失 ≠ 零值**。所有取值函数返回 (值, ok)，ok=false 表示"不知道"，
// 调用方必须显式决定如何处理，禁止用零值兜底。
//
// 为什么值得单独成包（实测依据：docs/prd/REQ-yg-gateway-code-reuse-2026-11-02.md）：
//   - 全仓 25+ 处 `x, _ := m["k"].(T)`，30+ 处手写嵌套 map 断言；
//   - **已经出过一次生产语义事故**：`c, _ := usage["credit"].(float64)` 把「上游没给
//     credit」当成「credit=0（免费）」，写进成本台账的观测样本 → 付费号被判成免费号
//     优先选号。这类错误不报错，只让选号决策被噪声污染（AGENTS.md「不报错但会伪造结论」）。
//
// 与 `logfmt`（日志脱敏）分家：本包只管"从 JSON 值里安全地取"，不碰日志与展示。
package jsonsafe

import "encoding/json"

// Map 取嵌套对象。key 缺失或值不是对象（含 null）都返回 ok=false。
func Map(m map[string]any, key string) (map[string]any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m[key].(map[string]any)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// Slice 取数组。key 缺失或值不是 `[]any` 都返回 ok=false。
//
// 注意：只认 `[]any`（json 解到 any 的形态）。`[]string` 之类是**调用方另解**的产物，
// 这里不做转换——静默转换会把"类型不符"变成"取到了"。
func Slice(m map[string]any, key string) ([]any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m[key].([]any)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// String 取字符串。key 缺失或值不是 string 都返回 ok=false（**不返回空串冒充**）。
func String(m map[string]any, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m[key].(string)
	return v, ok
}

// Bool 取布尔。key 缺失或值不是 bool 都返回 ok=false。
//
// 三值纪律：本函数只回答"这个字段是不是 true/false"，**不回答"字段缺失是否等于 false"**
// ——缺失就是 ok=false，由调用方决定当"未知"处理（见 AGENTS.md「禁止用 !field 判否」）。
func Bool(m map[string]any, key string) (bool, bool) {
	if m == nil {
		return false, false
	}
	v, ok := m[key].(bool)
	return v, ok
}

// Float 取浮点数。**严格**：只认 `float64`（json 解到 any 的标准形态）。
//
// 为什么不顺手接受 json.Number / 字符串数字：本函数的既有消费方（usage 的 credit 解析）
// 依赖"字符串数字 → 未知"这一行为。放宽会把「上游给了个怪形态」从"未知"变成"已知值"，
// 直接改变成本台账的三态语义——那是行为变更，不是安全化。
// 需要宽松数值转换请用 Int（它显式覆盖 float32/int/int64/json.Number）。
func Float(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key].(float64)
	return v, ok
}

// Int 取整数，接受 json 解到 any 的数值族：float64 / float32 / int / int64 / json.Number。
//
// 口径与 internal/server/logging.go 原有的 read() 闭包逐字一致（浮点截断、json.Number
// 走 Int64()），迁移时不得改变 token 计数结果。字符串数字一律 ok=false。
func Int(m map[string]any, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}
