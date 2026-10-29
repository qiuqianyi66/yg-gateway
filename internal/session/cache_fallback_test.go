package session

import "testing"

// TestCacheConversationFallback 内容派生回退（缓存键会话段）：
//   - 同会话重复请求 → 同值（同会话多轮前缀可递增复用）
//   - 不同会话（不同 system / 首条 user）→ 不同值（互不覆盖缓存槽）
//   - 无 user 消息 / nil 视图 → ""（调用方保持空会话语义）
//   - 与路由粘性键（ExtractKeyOpt allowDerive=false）**解耦**：路由关派生不影响缓存回退
//
// 牙齿：把 CacheConversationFallback 删掉 return 或改回 "" → 不同会话用例 FAIL。
func TestCacheConversationFallback(t *testing.T) {
	convA := `{"model":"m","messages":[
		{"role":"system","content":"sys-A"},
		{"role":"user","content":"task-A start"}]}`
	convB := `{"model":"m","messages":[
		{"role":"system","content":"sys-A"},
		{"role":"user","content":"task-B start"}]}`
	convA2 := `{"model":"m","messages":[
		{"role":"system","content":"sys-A"},
		{"role":"user","content":"task-A start"},
		{"role":"assistant","content":"round-1"},
		{"role":"user","content":"round-2"}]}`

	// 同会话稳定：首条 user 恒定，后续轮派生键不变。
	a1 := CacheConversationFallbackView(convA)
	a2 := CacheConversationFallbackView(convA2)
	if a1 == "" {
		t.Fatal("有 system+user 的会话应派生出非空键")
	}
	if a1 != a2 {
		t.Fatalf("同会话（首条 user 恒定）派生键应稳定: %q vs %q", a1, a2)
	}

	// 跨会话区分：首条 user 不同 → 键不同（不共享缓存槽）。
	b := CacheConversationFallbackView(convB)
	if b == "" || b == a1 {
		t.Fatalf("不同会话应派生出不同键: A=%q B=%q", a1, b)
	}

	// 无 user 消息 → 空串（无从归属会话）。
	noUser := `{"model":"m","messages":[{"role":"system","content":"sys"}]}`
	if got := CacheConversationFallbackView(noUser); got != "" {
		t.Fatalf("无 user 消息应返回空串, got %q", got)
	}

	// 路由派生开关不影响缓存回退（解耦契约）。
	if got := ExtractKeyOpt([]byte(convA), false); got == "" {
		// 路由关派生 → 不粘性（空）——这正是本函数要补的缓存侧回退。
		if CacheConversationFallbackView(convA) == "" {
			t.Fatal("路由关派生时缓存回退仍应派生出键（缓存复用不被路由开关牺牲）")
		}
	}
}

// CacheConversationFallbackView 测试辅助：body 字符串 → 视图 → 回退值。
func CacheConversationFallbackView(body string) string {
	v, ok := ParseRequestView([]byte(body))
	if !ok {
		return ""
	}
	return CacheConversationFallback(&v)
}
