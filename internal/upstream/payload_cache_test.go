package upstream

import (
	"encoding/json"
	"testing"
)

// TestPrepareBodySingleRoundTripEquivalence 单次往返（F-702）必须与旧的两趟版本
// **逐位等价**：先 PrepareBodyOptWithEffortsAndDefault，再 InjectPromptCacheKey。
//
// 这是出站管线的守门测试：出站 body 直接进上游，任何字段差异都会变成真实请求
// 差异（改写了 tool 配对、丢了 cache key、或 cache key 取到不同的会话源）。
//
// 牙齿：把注入时机挪到规范化之前（此时 conversation_id 还在但别的字段未归一）、
// 或漏掉「客户端已带 key 不覆盖」的优先级 → 对应用例 FAIL。
func TestPrepareBodySingleRoundTripEquivalence(t *testing.T) {
	efforts := map[string][]string{"glm-5.2": {"low", "high"}}
	defs := map[string]string{"glm-5.2": "high"}

	cases := []struct {
		name string
		body string
		uid  string
		conv string
	}{
		{"plain", `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`, "user-abc-123", "conv-1"},
		// 客户端自带 cache key：优先级 1 不覆盖
		{"client key wins", `{"model":"glm-5.2","messages":[],"prompt_cache_key":"client-set"}`, "user-abc-123", "conv-1"},
		// body 自带 conversation_id：优先于入站解析值
		{"body conv wins", `{"model":"glm-5.2","messages":[],"conversation_id":"conv-body"}`, "user-abc-123", "conv-param"},
		{"body camel conv", `{"model":"glm-5.2","messages":[],"conversationId":"conv-camel"}`, "user-abc-123", "conv-param"},
		// 无会话源：uid 段仍在，哈希段由 uid 单独派生
		{"no conv", `{"model":"glm-5.2","messages":[]}`, "user-abc-123", ""},
		{"empty uid", `{"model":"glm-5.2","messages":[]}`, "", "conv-1"},
		// effort 降级 + cache key 并存
		{"effort downgrade", `{"model":"glm-5.2","messages":[],"reasoning_effort":"max"}`, "user-abc-123", "conv-1"},
		// tool 配对修复路径（孤儿 tool_calls 被剔除）与 cache key 并存
		{"orphan tool cleanup", `{"model":"glm-5.2","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}],"conversation_id":"conv-t"}`, "user-abc-123", ""},
		// max_completion_tokens 别名翻译与 cache key 并存
		{"alias translate", `{"model":"glm-5.2","messages":[],"max_completion_tokens":2048,"conversation_id":"conv-a"}`, "user-abc-123", ""},
		// 畸形 body：两个版本都应原样返回
		{"broken json", `{broken`, "user-abc-123", "conv-1"},
		{"empty body", ``, "user-abc-123", "conv-1"},
		{"array body", `["not","object"]`, "user-abc-123", "conv-1"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := []byte(c.body)
			for _, sanitize := range []bool{false, true} {
				// 旧两趟版本（基准）。
				want := InjectPromptCacheKey(
					PrepareBodyOptWithEffortsAndDefault(src, sanitize, efforts, defs),
					c.uid, c.conv)
				// 新单趟版本。
				got := PrepareBodyOptWithEffortsAndDefaultAndCache(src, sanitize, efforts, defs, c.uid, c.conv)
				if string(want) != string(got) {
					t.Errorf("sanitize=%v:\n single=%s\n two-pass=%s", sanitize, got, want)
				}
			}
		})
	}
}

// TestPrepareBodySingleRoundTripInjects 单趟版本确实注入了 cache key，
// 且格式与 buildCacheKey 一致（wb2a-<uid8>-<hash>）。
//
// 牙齿：把 promptCacheKeyFrom 的调用去掉（只规范化不注入）→ 用例 FAIL。
func TestPrepareBodySingleRoundTripInjects(t *testing.T) {
	out := PrepareBodyOptWithEffortsAndDefaultAndCache(
		[]byte(`{"model":"glm-5.2","messages":[]}`), false, nil, nil, "1234567890abcdef", "conv-fmt")
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, _ := obj["prompt_cache_key"].(string)
	if got == "" {
		t.Fatal("cache key not injected")
	}
	want := buildCacheKey("1234567890abcdef", "conv-fmt")
	if got != want {
		t.Errorf("cache key = %q want %q", got, want)
	}
}

// TestPromptCacheKeyFromPriorities AST 版 cache key 的三个优先级与字节版一致。
func TestPromptCacheKeyFromPriorities(t *testing.T) {
	cases := []struct {
		obj  map[string]any
		uid  string
		conv string
		want string
	}{
		// 优先级 1：客户端自带
		{map[string]any{"prompt_cache_key": "client"}, "u1", "c1", "client"},
		// 优先级 1 的空串不算「已带」（与 strField 判空一致）
		{map[string]any{"prompt_cache_key": ""}, "u1", "c1", buildCacheKey("u1", "c1")},
		// 优先级 2：conversation_id 优先于入站参数
		{map[string]any{"conversation_id": "body-conv"}, "u1", "param-conv", buildCacheKey("u1", "body-conv")},
		// 优先级 2 的 camel 形态
		{map[string]any{"conversationId": "camel-conv"}, "u1", "param-conv", buildCacheKey("u1", "camel-conv")},
		// 优先级 3：都无 → 用入站参数
		{map[string]any{}, "u1", "param-conv", buildCacheKey("u1", "param-conv")},
		// 非字符串值不参与（strField 判空）
		{map[string]any{"conversation_id": 123}, "u1", "param-conv", buildCacheKey("u1", "param-conv")},
	}
	for i, c := range cases {
		if got := promptCacheKeyFrom(c.obj, c.uid, c.conv); got != c.want {
			t.Errorf("case %d: promptCacheKeyFrom = %q want %q", i, got, c.want)
		}
	}
}
