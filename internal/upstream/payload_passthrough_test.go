// payload_passthrough_test.go 出站 body 未处理键的透传锚定（REQ-structured-output P1）。
//
// 背景（2026-10-24 链路实测）：出站 body 是 AST 级重组——prepareBodyAST 只显式
// 删除/改写已知键（max_completion_tokens/tools/tool_choice/reasoning_effort 等），
// 其余键随 obj 原样 marshal 回去。response_format 因此"碰巧"透传，但没有任何
// 测试锚定：prepareBodyAST 将来若改成字段白名单式重组，该键会**静默消失**，
// 依赖结构化输出的 agent 客户端解析破裂且不报错。本测试把"碰巧对"变成"被保证对"。
package upstream

import (
	"encoding/json"
	"testing"
)

// TestPrepareBodyPreservesResponseFormat 断言 response_format（json_object 与
// json_schema 两种形态）在完整出站管线（sanitize 开/关 × cache key 注入）后原样保留。
func TestPrepareBodyPreservesResponseFormat(t *testing.T) {
	cases := []struct {
		name string
		rf   map[string]any
	}{
		{"json_object", map[string]any{"type": "json_object"}},
		{"json_schema", map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "reply",
				"schema": map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}},
			},
		}},
	}
	for _, sanitize := range []bool{false, true} {
		for _, c := range cases {
			src := map[string]any{
				"model":           "glm-5.3-flash",
				"messages":        []any{map[string]any{"role": "user", "content": "hi"}},
				"response_format": c.rf,
			}
			raw, err := json.Marshal(src)
			if err != nil {
				t.Fatal(err)
			}
			out := PrepareBodyOptWithEffortsAndDefaultAndCache(raw, sanitize, nil, nil, "u1", "c1")
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("%s/sanitize=%v: 出站 body 不可解析: %v", c.name, sanitize, err)
			}
			rf, ok := got["response_format"]
			if !ok {
				t.Fatalf("%s/sanitize=%v: response_format 被出站管线静默删除（白名单化回归？出站 body=%s）", c.name, sanitize, out)
			}
			want, _ := json.Marshal(c.rf)
			gotRaw, _ := json.Marshal(rf)
			if string(gotRaw) != string(want) {
				t.Fatalf("%s/sanitize=%v: response_format 被改写: got %s want %s", c.name, sanitize, gotRaw, want)
			}
		}
	}
}
