package session

import "testing"

// TestExtractKeyOptDeriveSwitch 内容派生开关（ADR-004）：
//   - allowDerive=true  → 无显式标识时按内容派生（既有行为，零回归）
//   - allowDerive=false → 无显式标识返回空串（不粘性，回落加权轮换）
//   - 两种模式下**显式会话标识都必须命中**（派生开关不该影响显式键）
//
// 牙齿：把 allowDerive 判断去掉（恒返回 deriveKey）→ 关闭分支的用例 FAIL。
func TestExtractKeyOptDeriveSwitch(t *testing.T) {
	// 无任何显式会话标识：只有「system + 首条 user」内容可派生。
	derivable := `{"model":"m","messages":[
		{"role":"system","content":"You are a helpful assistant."},
		{"role":"user","content":"hi"}]}`

	if got := ExtractKeyOpt([]byte(derivable), true); got == "" {
		t.Fatal("allowDerive=true 时应派生出版本键（既有行为）")
	} else if got[0] != 'd' { // derivedKeyPrefix = "d-"
		t.Fatalf("派生键应带 d- 前缀，got %q", got)
	}
	if got := ExtractKeyOpt([]byte(derivable), false); got != "" {
		t.Fatalf("allowDerive=false 时不应派生（got %q）——否则无关对话会被钉到同一账号", got)
	}

	// 显式会话标识：两种模式都必须命中（开关只作用于派生回退）。
	explicit := map[string]string{
		"metadata.conversation_id": `{"model":"m","metadata":{"conversation_id":"conv-1"},"messages":[]}`,
		"metadata.conversationId":  `{"model":"m","metadata":{"conversationId":"conv-2"},"messages":[]}`,
		"conversation_id":          `{"model":"m","conversation_id":"conv-3","messages":[]}`,
		"conversationId":           `{"model":"m","conversationId":"conv-4","messages":[]}`,
		"prompt_cache_key":         `{"model":"m","prompt_cache_key":"cache-5","messages":[]}`,
	}
	for name, body := range explicit {
		on := ExtractKeyOpt([]byte(body), true)
		off := ExtractKeyOpt([]byte(body), false)
		if on == "" || off == "" {
			t.Errorf("%s：显式标识在两种模式下都应命中（on=%q off=%q）", name, on, off)
		}
		if on != off {
			t.Errorf("%s：派生开关不应改变显式键的取值（on=%q off=%q）", name, on, off)
		}
	}
}

// TestExtractKeyOptDefaultKeepsLegacy ExtractKey 等价于 allowDerive=true（零回归守门）。
func TestExtractKeyOptDefaultKeepsLegacy(t *testing.T) {
	bodies := []string{
		`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`,
		`{"model":"m","metadata":{"conversation_id":"c1"},"messages":[]}`,
		`{"model":"m","messages":[]}`,
		`{"model":"m","prompt_cache_key":"k1","messages":[]}`,
	}
	for _, b := range bodies {
		if ExtractKey([]byte(b)) != ExtractKeyOpt([]byte(b), true) {
			t.Errorf("ExtractKey 必须等价于 ExtractKeyOpt(true)（body=%s）", b)
		}
	}
}

// TestExtractKeyOptShortOpeningCollision 复现问题场景：固定 system + 相同短开场
// 的两段**无关**对话会派生出同一个键（这正是"流量全钉一个号"的机制）。
//
// 该测试固化「为什么需要这个开关」——它断言的是**问题的存在**，
// 从而证明 allowDerive=false 的必要性（关掉后两者都返回空、不再互相粘）。
func TestExtractKeyOptShortOpeningCollision(t *testing.T) {
	// 两段对话：只有 system 相同、首条 user 相同（短开场），后续消息不同。
	convA := `{"model":"m","messages":[
		{"role":"system","content":"You are a helpful assistant."},
		{"role":"user","content":"你好"},
		{"role":"assistant","content":"你好！有什么可以帮你？"},
		{"role":"user","content":"帮我写一个排序"}]}`
	convB := `{"model":"m","messages":[
		{"role":"system","content":"You are a helpful assistant."},
		{"role":"user","content":"你好"},
		{"role":"assistant","content":"你好！有什么可以帮你？"},
		{"role":"user","content":"帮我查个天气"}]}`

	// 开启派生：两者被判为**同一会话**（问题本身）。
	if ExtractKeyOpt([]byte(convA), true) != ExtractKeyOpt([]byte(convB), true) {
		t.Fatal("预期两段对话派生同一键（这是该问题的机制）")
	}
	// 关闭派生：两者都不粘性，各自回落加权轮换。
	if ExtractKeyOpt([]byte(convA), false) != "" || ExtractKeyOpt([]byte(convB), false) != "" {
		t.Fatal("关闭派生后无显式标识的请求应返回空串（不粘性）")
	}
}
