// sse_aggregate.go 非流式聚合的状态机：Aggregate 的读取循环留在 sse.go，
// 全部可变状态与合并规则集中在这里（结构体方法，无闭包捕获）。
package upstream

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// sseAggregator 承载一次非流式聚合的全部可变状态。
//
// 拆分动机：原 Aggregate 是 211 代码行的单体函数，状态（id/model/created/content/
// reasoning/role/finishReason/usage/validEvents/sawDone/toolCalls/toolOrder/toolSeq/
// idIndex）与四条合并规则（content latch、tool_calls 按 index 归位、message 整条并入、
// 体量上限计量）全挤在一个栈帧里，改任何一条都要先读完整个函数。结构体化后每条规则
// 是一个方法，读取循环只剩「读行 → 计体量 → 消费 → 判 EOF」。
type sseAggregator struct {
	id, model     string
	created       float64
	content       strings.Builder
	reasoning     strings.Builder
	role          string
	finishReason  string
	usage         map[string]any
	gotAnyContent bool
	validEvents   int
	sawDone       bool // 上游显式发过 data: [DONE]（正常收尾）
	toolCalls     map[int]map[string]any
	toolOrder     []int
	// toolSeq 缺 index 的 tool_call 的分配序号源：跨帧延续「最近分配」槽位，
	// 同帧内递增（见 mergeToolCallsChunk 注释）。
	toolSeq int
	// idIndex id → 已分配的 index：跨帧持续（延续碎片的 id 常出现在后续帧），
	// 供缺 index 时按 id 归位既有调用（见 mergeToolCallsChunk 注释）。
	idIndex map[string]int
}

// newSSEAggregator 返回装配好默认值的聚合器（role=assistant、finish_reason=stop，
// 两张表已初始化——逐位保留原 Aggregate 的零值语义）。
func newSSEAggregator() *sseAggregator {
	return &sseAggregator{
		role:         "assistant",
		finishReason: "stop",
		toolCalls:    map[int]map[string]any{},
		idIndex:      map[string]int{},
	}
}

// trackedBytes 计入聚合上限的流内体量：content + reasoning + 全部 tool_calls
// arguments 拼接总长。此前只量 content——推理链（reasoning_content）与工具参数
// （function.arguments 逐片拼接）是另两条无界增长路径，F-705 的 16MiB 上限必须对
// 三者同时成立，否则一个不吐 content 的畸形流可以把网关吃满。
func (a *sseAggregator) trackedBytes() int64 {
	n := int64(a.content.Len()) + int64(a.reasoning.Len())
	for _, tc := range a.toolCalls {
		if fn, ok := tc["function"].(map[string]any); ok {
			if args, ok := fn["arguments"].(string); ok {
				n += int64(len(args))
			}
		}
	}
	return n
}

// appendContent 是「已取到正文」（gotAnyContent latch）的唯一写入点：delta 与
// message 两路 content 都必须经此并入，规约只有一份（issue #142）——
//
//	S1 空串不算「已取到正文」、不占 latch 名额：OpenAI 风格 role-only 首帧
//	   （delta.content=""）和整条空 message 帧是常态帧，若空串置位 latch，
//	   后续真正文会被 message 回退分支的 !gotAnyContent 守卫静默拒绝；
//	S2 空串本身也无可追加字节，跳过 WriteString 与追加语义自洽。
func (a *sseAggregator) appendContent(txt string) {
	if txt == "" {
		return
	}
	a.content.WriteString(txt)
	a.gotAnyContent = true
}

// nextToolIndex 分配下一个不冲突的缺 index 序号：从 toolSeq 起递增跳过既有
// index（合规流的 index 是 0..N-1，缺 index 的补位不能覆盖它们）。
func (a *sseAggregator) nextToolIndex() int {
	for {
		idx := a.toolSeq
		a.toolSeq++
		if _, used := a.toolCalls[idx]; !used {
			return idx
		}
	}
}

// mergeToolCallsChunk 把一段 tool_calls 数组按 index 合并进累计表。
// delta（流式分片，按 index 累积）与 message（非 delta 整条）共用同一合并逻辑，
// 保证「上游给的身份/函数名不丢、arguments 拼接语义一致」。
//
// index 缺失兼容：OpenAI 规范要求 delta 帧的 tool_call 带 index（标记分片归属），
// 但部分上游省略它。此前缺 index 一律归 0——多调用场景下不同 call 被合并进同一
// index 槽，arguments 串联、name 互相覆盖（数据污染）。修法按「id 优先、lastIdx 兜底」：
//   - 带 index → 按 index 累积（合规形态，零改动）；
//   - 缺 index 带 id 且 id 已见过 → 延续该 id 所在 index；
//   - 缺 index 带 id 且 id 是新的 → 开新序号（多调用不合并）；
//   - 缺 index 无 id → 追加到最近收到碎片的 index（单个调用的延续分片无 id
//     是标准形态），无既往则开新号。
//
// 带 index 的碎片照常按 index 累积，不受影响。
func (a *sseAggregator) mergeToolCallsChunk(tcs []any) {
	for _, tc := range tcs {
		call, ok := tc.(map[string]any)
		if !ok {
			continue
		}
		idx := -1
		if v, ok := call["index"].(float64); ok {
			idx = int(v)
		} else if cid, _ := call["id"].(string); cid != "" {
			if mid, seen := a.idIndex[cid]; seen {
				idx = mid // 该 id 已归位过：延续既有调用（跨帧有效）
			} else {
				idx = a.nextToolIndex()
			}
		} else if len(a.toolOrder) > 0 {
			idx = a.toolOrder[len(a.toolOrder)-1] // 无 id 碎片：延续最近调用
		} else {
			idx = a.nextToolIndex()
		}
		merged, seen := a.toolCalls[idx]
		if !seen {
			merged = map[string]any{"index": idx}
			a.toolCalls[idx] = merged
			a.toolOrder = append(a.toolOrder, idx)
		}
		if cid, _ := call["id"].(string); cid != "" {
			a.idIndex[cid] = idx
		}
		if cid, _ := merged["id"].(string); cid != "" {
			a.idIndex[cid] = idx
		}
		mergeToolCallDelta(merged, call)
	}
}

// mergeMessageFields 把非 delta 的完整 message 内容并入聚合（message 是整条下发，
// 非流式拼接，content 只取一次）。role/reasoning_content/tool_calls 与 delta 分支
// 同构透出；content 同样置 gotAnyContent，与 delta 路径的 latch 语义一致
// （一帧整条 message 之后，后续 delta 帧不重复追加）。
func (a *sseAggregator) mergeMessageFields(msg map[string]any) {
	if r2, ok := msg["role"].(string); ok && r2 != "" {
		a.role = r2
	}
	if txt, ok := msg["content"].(string); ok {
		a.appendContent(txt)
	}
	if rc, ok := msg["reasoning_content"].(string); ok {
		a.reasoning.WriteString(rc)
	}
	if tcs, ok := msg["tool_calls"].([]any); ok {
		a.mergeToolCallsChunk(tcs)
	}
}

// consumeLine 处理一行已去行尾的 SSE 文本：data: 行解析并合并，遇到 [DONE]
// 返回 done=true（调用方停止读取）。非 data: 行（注释/event:/id: 等）静默跳过。
func (a *sseAggregator) consumeLine(line string) (done bool) {
	if !strings.HasPrefix(line, "data: ") {
		return false
	}
	payload := strings.TrimPrefix(line, "data: ")
	if payload == "[DONE]" {
		// 上游显式结束：停止读取，DONE 之后的任何数据一律忽略。
		a.sawDone = true
		return true
	}
	var chunk map[string]any
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return false // 解析失败沿用静默跳过（不计入 validEvents）
	}
	// 有效事件计数：仅 JSON 解析成功的数据帧计入。
	a.validEvents++
	a.consumeChunk(chunk)
	return false
}

// consumeChunk 把一条解析成功的 JSON chunk 并入聚合状态（顶层标量 + choices 循环）。
func (a *sseAggregator) consumeChunk(chunk map[string]any) {
	if v, ok := chunk["id"].(string); ok && a.id == "" {
		a.id = v
	}
	if v, ok := chunk["model"].(string); ok && a.model == "" {
		a.model = v
	}
	if v, ok := chunk["created"].(float64); ok && a.created == 0 {
		a.created = v
	}
	if u, ok := chunk["usage"].(map[string]any); ok {
		a.usage = u
	}
	ch, ok := chunk["choices"].([]any)
	if !ok {
		return
	}
	for _, ci := range ch {
		c, _ := ci.(map[string]any)
		if c == nil {
			continue
		}
		if fr, ok := c["finish_reason"].(string); ok && fr != "" {
			a.finishReason = fr
		}
		if delta, ok := c["delta"].(map[string]any); ok {
			if r2, ok := delta["role"].(string); ok && r2 != "" {
				a.role = r2
			}
			if txt, ok := delta["content"].(string); ok {
				a.appendContent(txt)
			}
			if rc, ok := delta["reasoning_content"].(string); ok {
				a.reasoning.WriteString(rc)
			}
			if tcs, ok := delta["tool_calls"].([]any); ok {
				a.mergeToolCallsChunk(tcs)
			}
		}
		// 有的上游把完整消息放在 message 里（非 delta）：整条并入（role/content/
		// reasoning_content/tool_calls），与 delta 分支同构。delta 已取过正文
		// （gotAnyContent）则跳过（避免与 delta 路径重复拼接——PR #134 的 latch 语义）。
		if msg, ok := c["message"].(map[string]any); ok && !a.gotAnyContent {
			a.mergeMessageFields(msg)
		}
	}
}

// build 组装最终的 chat.completion 响应（原 Aggregate 收尾段的逐位搬运）。
// 调用方须先用 validEvents 判定非空流，否则不应调用（空流应返回 errEmptyStream）。
func (a *sseAggregator) build() map[string]any {
	id := a.id
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	created := a.created
	if created == 0 {
		created = float64(time.Now().Unix())
	}
	message := map[string]any{
		"role":    a.role,
		"content": a.content.String(),
	}
	if a.reasoning.Len() > 0 {
		message["reasoning_content"] = a.reasoning.String()
	}
	if len(a.toolOrder) > 0 {
		sort.Ints(a.toolOrder)
		calls := make([]map[string]any, 0, len(a.toolOrder))
		for _, idx := range a.toolOrder {
			calls = append(calls, a.toolCalls[idx])
		}
		// P1b：流被截断时 tool_call 的 arguments 是残缺 JSON（解析失败），不把脏参数
		// 交给客户端——残留分片会被客户端解析成非法 JSON 卡死会话。截断的两个来源：
		//   - finish_reason=="length"（模型因 max_tokens 提前中止）；
		//   - 上游连接中断（EOF 收尾但未发 data: [DONE]，sawDone=false）。
		// 完整参数原样保留（正例零改动）；空参数（无参工具）不是截断，同样保留。
		// 此前只认 finish_reason=="length"，EOF 截断的 tool_calls 残缺参数被原样下发。
		if a.finishReason == "length" || !a.sawDone {
			calls = dropTruncatedToolCalls(calls)
		}
		if len(calls) > 0 {
			message["tool_calls"] = calls
		}
	}
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": int64(created),
		"model":   a.model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": a.finishReason,
			},
		},
	}
	if a.usage != nil {
		// OpenAI 非流式 usage 必含 total_tokens。上游若只发 prompt_tokens +
		// completion_tokens（部分上游末帧缺 total），网关合成补齐——否则严格按
		// schema 校验的客户端收不到 total_tokens。已有 total 或二者缺一不补
		// （不臆造：单边有值无法合成可信的 total）。
		resp["usage"] = normalizeUsageCacheAliases(ensureUsageTotal(a.usage))
	}
	return resp
}
