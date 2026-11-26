// runtime_compact_test.go 压缩不拆 tool 配对、预算边界（ADR-029 D5）。
package opsagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeCompactionSystemPrefixAndSummary(t *testing.T) {
	msgs := []RuntimeMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("旧", 100)},
		{Role: "assistant", Content: ""},
		{Role: "user", Content: ""},
		{Role: "assistant", Content: "最近"},
	}
	res := compactRuntimeMessages(msgs, 80)
	if res.Shadowed == 0 {
		t.Fatalf("未压缩: %#v", res)
	}
	if res.Kept[0].Role != "system" || res.Kept[1].Role != "user" {
		t.Fatalf("system 未保留在摘要前: %#v", res.Kept)
	}
	if !strings.Contains(res.Summary, "tool call") && !strings.Contains(res.Summary, "empty") &&
		!strings.Contains(res.Summary, "more") {
		// 只断言摘要非空且包含数量，不锁具体措辞。
		if !strings.Contains(res.Summary, "Compacted") {
			t.Fatalf("summary=%q", res.Summary)
		}
	}
}

func TestRuntimeCompactionSystemOverBudgetNoBody(t *testing.T) {
	res := compactRuntimeMessages([]RuntimeMessage{{Role: "system", Content: strings.Repeat("长", 100)}}, 5)
	if res.Shadowed != 0 || len(res.Kept) != 1 {
		t.Fatalf("system-only 不应压缩出空 body: %#v", res)
	}
}

func TestRuntimeCompactionDisabled(t *testing.T) {
	res := compactRuntimeMessages([]RuntimeMessage{{Role: "user", Content: strings.Repeat("超", 100)}}, 0)
	if res.Shadowed != 0 {
		t.Fatalf("maxTokens<=0 不应压缩: %#v", res)
	}
}

func TestRuntimeCompactionEmptyBody(t *testing.T) {
	res := compactRuntimeMessages(nil, 10)
	if res.Shadowed != 0 || len(res.Kept) != 0 {
		t.Fatalf("res=%#v", res)
	}
}

func TestRuntimeCompactionOnlySystemNoBody(t *testing.T) {
	res := compactRuntimeMessages([]RuntimeMessage{{Role: "system", Content: "sys"}}, 10)
	if res.Shadowed != 0 || len(res.Kept) != 1 {
		t.Fatalf("res=%#v", res)
	}
}

func TestRuntimeCompactionNoopUnderBudget(t *testing.T) {
	msgs := []RuntimeMessage{{Role: "user", Content: "短"}, {Role: "assistant", Content: "也短"}}
	res := compactRuntimeMessages(msgs, 1000)
	if res.Shadowed != 0 || len(res.Kept) != 2 {
		t.Fatalf("res=%#v", res)
	}
}

func TestRuntimeCompactionKeepsToolPair(t *testing.T) {
	msgs := []RuntimeMessage{
		{Role: "user", Content: strings.Repeat("旧", 200)},
		{Role: "assistant", Content: strings.Repeat("旧", 200)},
		{Role: "assistant", ToolCalls: []RuntimeToolCall{{ID: "c1", Name: "gateway_status", Arguments: json.RawMessage(`"` + strings.Repeat("x", 500) + `"`)}}},
		{Role: "tool", ToolCallID: "c1", Name: "gateway_status", Content: "healthy=2/4"},
		{Role: "user", Content: "最近问题"},
		{Role: "assistant", Content: "最近答复"},
	}
	res := compactRuntimeMessages(msgs, 300)
	if res.Shadowed == 0 {
		t.Fatalf("未触发压缩: %#v", res)
	}
	if len(res.Kept) == 0 || res.Kept[0].Role != "user" || !strings.Contains(res.Kept[0].Content, "Earlier session summary") {
		t.Fatalf("kept 首条不是摘要: %#v", res.Kept[:1])
	}
	for i, m := range res.Kept {
		if m.Role == "tool" {
			if i == 0 || res.Kept[i-1].Role != "assistant" || len(res.Kept[i-1].ToolCalls) == 0 {
				t.Fatalf("tool 消息前没有配对 assistant: %#v", res.Kept)
			}
		}
	}
	if res.EstAfter >= res.EstBefore {
		t.Fatalf("压缩后未变小: before=%d after=%d", res.EstBefore, res.EstAfter)
	}
}

func TestRuntimeEstimateCJKAndASCII(t *testing.T) {
	cjk := estimateRuntimeText("中文四个字")
	ascii := estimateRuntimeText("abcd")
	if cjk <= ascii {
		t.Fatalf("CJK=%v ASCII=%v，CJK 应更高", cjk, ascii)
	}
}
