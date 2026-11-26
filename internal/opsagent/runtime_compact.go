// runtime_compact.go 上下文压缩（ADR-029 D5）。
//
// 确定性机械摘要：保留 system + 最近消息，把更早消息压成角色/摘要清单。
// 不调用模型做摘要——压缩本身不能成为新的故障源。切点遇到 tool 消息时
// 回退到配对的 assistant，避免产生孤立的 tool_call_id。
package opsagent

import (
	"fmt"
	"strings"
)

type runtimeCompactionResult struct {
	Kept      []RuntimeMessage
	Summary   string
	Shadowed  int
	EstBefore int
	EstAfter  int
}

// estimateRuntimeTokens 粗估消息 token：CJK/全角按 1，ASCII 按 ~0.28。
// 只用于预算，不用于计费；宁高勿低。
func estimateRuntimeTokens(msgs []RuntimeMessage) int {
	total := 0.0
	for _, m := range msgs {
		total += 4
		total += estimateRuntimeText(m.Content)
		for _, tc := range m.ToolCalls {
			total += 6
			total += estimateRuntimeText(string(tc.Arguments))
		}
	}
	return int(total)
}

func estimateRuntimeText(s string) float64 {
	total := 0.0
	for _, r := range s {
		if r > 127 {
			total += 1.0
		} else {
			total += 0.28
		}
	}
	return total
}

// compactRuntimeMessages 在超预算时压缩。maxTokens<=0 表示不压缩。
func compactRuntimeMessages(msgs []RuntimeMessage, maxTokens int) runtimeCompactionResult {
	before := estimateRuntimeTokens(msgs)
	res := runtimeCompactionResult{Kept: cloneRuntimeMessages(msgs), EstBefore: before, EstAfter: before}
	if maxTokens <= 0 || before <= maxTokens || len(msgs) == 0 {
		return res
	}
	prefix, body := splitRuntimeSystem(msgs)
	if len(body) == 0 {
		return res
	}
	budget := maxTokens * 3 / 4
	start := len(body)
	for start > 0 {
		if estimateRuntimeTokens(append(cloneRuntimeMessages(prefix), body[start-1:]...)) > budget && start < len(body) {
			break
		}
		start--
	}
	// 切点不能是孤立 tool：回退到带 tool_calls 的 assistant。
	for start > 0 && body[start].Role == "tool" {
		start--
		if body[start].Role == "assistant" && len(body[start].ToolCalls) > 0 {
			break
		}
	}
	if start == 0 {
		return res
	}
	shadowed := body[:start]
	summary := runtimeCompactionSummary(shadowed)
	summaryMsg := RuntimeMessage{Role: "user", Content: "Earlier session summary:\n" + summary}
	kept := cloneRuntimeMessages(prefix)
	kept = append(kept, summaryMsg)
	kept = append(kept, cloneRuntimeMessages(body[start:])...)
	res.Kept = kept
	res.Summary = summary
	res.Shadowed = len(shadowed)
	res.EstAfter = estimateRuntimeTokens(kept)
	return res
}

func splitRuntimeSystem(msgs []RuntimeMessage) (prefix, body []RuntimeMessage) {
	i := 0
	for i < len(msgs) && msgs[i].Role == "system" {
		i++
	}
	return cloneRuntimeMessages(msgs[:i]), cloneRuntimeMessages(msgs[i:])
}

func runtimeCompactionSummary(dropped []RuntimeMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Compacted %d earlier messages.", len(dropped))
	shown := 0
	for _, m := range dropped {
		if shown >= 6 {
			break
		}
		text := strings.TrimSpace(m.Content)
		if len([]rune(text)) > 80 {
			text = string([]rune(text)[:80]) + "…"
		}
		if text == "" && len(m.ToolCalls) > 0 {
			text = fmt.Sprintf("%d tool call(s)", len(m.ToolCalls))
		}
		if text == "" {
			text = "(empty)"
		}
		fmt.Fprintf(&b, "\n- %s: %s", m.Role, text)
		shown++
	}
	if len(dropped) > shown {
		fmt.Fprintf(&b, "\n- … %d more", len(dropped)-shown)
	}
	return b.String()
}
