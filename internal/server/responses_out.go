// responses_out.go Responses 出站转换（转换 B，F8）：chat 响应 → response 对象 /
// Responses SSE 事件序列。流式转译在 responsesStreamWriter 层做，配合
// upstream.StreamHint「每次 Write 写完整一帧（io.WriteString 单帧对齐）」的写路径，
// 逐帧翻译；非 data 帧（`: ping` 心跳、error 帧原文）按需透传/转译。
package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// writeResponsesObject 非流式出口：聚合出的 chat 响应转成 response 对象写回。
func writeResponsesObject(w http.ResponseWriter, chat map[string]any, model string) {
	writeJSON(w, http.StatusOK, chatRespToResponses(chat, model))
}

// chatRespToResponses 把聚合出的 chat 响应（choices[0].message）转成 response 对象。
// usage 按 OpenAI Responses schema 重命名（input_tokens/output_tokens + 明细）。
func chatRespToResponses(chat map[string]any, model string) map[string]any {
	created := time.Now().Unix()
	if c, ok := chat["created"].(float64); ok && c > 0 {
		created = int64(c)
	}
	out := map[string]any{
		"id": "resp_" + randHex(8), "object": "response", "created_at": created,
		"status": "completed", "model": model, "output": []any{}, "error": nil,
	}
	choices, _ := chat["choices"].([]any)
	if len(choices) > 0 {
		msg, _ := choices[0].(map[string]any)
		if msg == nil {
			return out
		}
		if m, ok := msg["message"].(map[string]any); ok {
			if text, _ := m["content"].(string); text != "" {
				out["output"] = append(out["output"].([]any), messageOutputItem(text))
			}
			if tcs, ok := m["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					fn, _ := tc.(map[string]any)
					if fn == nil {
						continue
					}
					f := map[string]any{}
					if g, ok := fn["function"].(map[string]any); ok {
						f = g
					}
					callID, _ := fn["id"].(string)
					if callID == "" {
						callID = "call_" + randHex(6)
					}
					name, _ := f["name"].(string)
					args, _ := f["arguments"].(string)
					out["output"] = append(out["output"].([]any), map[string]any{
						"type": "function_call", "id": "fc_" + randHex(8),
						"call_id": callID, "name": name, "arguments": args, "status": "completed",
					})
				}
			}
		}
	}
	if u, ok := chat["usage"].(map[string]any); ok {
		out["usage"] = usageToResponses(u)
	}
	return out
}

// messageOutputItem 构造 message 输出项（非流式 / 流式共用语义）。
func messageOutputItem(text string) map[string]any {
	return map[string]any{
		"type": "message", "id": "msg_" + randHex(8), "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}
}

// usageToResponses 把 chat usage 映射成 responses usage（缺失字段省略，不填零值）。
func usageToResponses(u map[string]any) map[string]any {
	prompt := asInt64(u["prompt_tokens"])
	completion := asInt64(u["completion_tokens"])
	total := asInt64(u["total_tokens"])
	out := map[string]any{
		"input_tokens":  prompt,
		"output_tokens": completion,
		"total_tokens":  total,
	}
	inDetails := map[string]any{}
	if h, ok := u["prompt_cache_hit_tokens"].(float64); ok {
		inDetails["cached_tokens"] = asInt64(h)
	}
	if len(inDetails) > 0 {
		out["input_tokens_details"] = inDetails
	}
	outDetails := map[string]any{}
	if cd, ok := u["completion_tokens_details"].(map[string]any); ok {
		if r, ok := cd["reasoning_tokens"].(float64); ok {
			outDetails["reasoning_tokens"] = asInt64(r)
		}
	}
	if len(outDetails) > 0 {
		out["output_tokens_details"] = outDetails
	}
	return out
}

// asInt64 数值归一化：调用方可能传 int（构造的 map）或 float64 / json.Number
// （JSON 解码结果），统一转 int64；非数值返回 0。
func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return i
		}
		return 0
	default:
		return 0
	}
}

// respStreamState 流式转译的跨帧状态。
type respStreamState struct {
	respID    string
	created   int64
	model     string
	started   bool
	done      bool
	seq       int
	itemID    string
	text      strings.Builder
	usage     map[string]any
	toolCalls map[string]*respToolState // 工具 index → 状态
	toolOrder []string
}

// respToolState 流式 tool_call 分片聚合状态。
type respToolState struct {
	index      int
	id, callID string
	name, args string
}

// newRespStreamState 构造流式状态（响应 id / created_at 全程稳定，跨事件一致）。
func newRespStreamState() *respStreamState {
	return &respStreamState{
		respID:    "resp_" + randHex(8),
		created:   time.Now().Unix(),
		toolCalls: map[string]*respToolState{},
	}
}

// event 组装一个 Responses 事件（type + 递增 sequence_number + 附加字段）。
func (st *respStreamState) event(typ string, extra map[string]any) string {
	st.seq++
	m := map[string]any{"type": typ, "sequence_number": st.seq}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m) // 忽略：受控类型（string/bool/number/嵌套 map）组装，Marshal 不可失败
	return string(b)
}

// partialResponse 组装响应对象（response.created / completed 共用；id 全程一致）。
func (st *respStreamState) partialResponse(status string) map[string]any {
	text := st.text.String()
	out := []any{map[string]any{
		"id": st.itemID, "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}}
	for _, key := range st.toolOrder {
		ts := st.toolCalls[key]
		out = append(out, map[string]any{
			"id": ts.id, "type": "function_call", "call_id": ts.callID,
			"name": ts.name, "arguments": ts.args, "status": "completed",
		})
	}
	r := map[string]any{
		"id": st.respID, "object": "response", "created_at": st.created,
		"status": status, "model": st.model, "output": out, "error": nil,
	}
	if st.usage != nil {
		r["usage"] = usageToResponses(st.usage)
	}
	return r
}

// responsesStreamWriter 流式帧转译 writer：把 chat SSE data 帧翻译成 Responses 事件。
// 仅用于 /v1/responses 流式请求；依赖 StreamHint 单帧对齐写出（每次 Write 一帧），
// 非 data 帧原样透传（`: ping` 心跳、普通 JSON 错误响应）。
type responsesStreamWriter struct {
	inner http.ResponseWriter
	st    *respStreamState
}

func (w *responsesStreamWriter) Header() http.Header { return w.inner.Header() }

func (w *responsesStreamWriter) WriteHeader(code int) { w.inner.WriteHeader(code) }

// Flush 透传（StreamHint 断言 w 为 Flusher 时使用）。
func (w *responsesStreamWriter) Flush() {
	if fl, ok := w.inner.(http.Flusher); ok {
		fl.Flush()
	}
}

func (w *responsesStreamWriter) Write(p []byte) (int, error) {
	s := string(p)
	if strings.HasPrefix(s, "data: ") && strings.HasSuffix(s, "\n\n") {
		payload := strings.TrimSuffix(strings.TrimPrefix(s, "data: "), "\n\n")
		for _, ev := range w.translateFrame(payload) {
			if _, err := io.WriteString(w.inner, "data: "+ev+"\n\n"); err != nil {
				return len(p), err
			}
		}
		w.Flush()
		return len(p), nil
	}
	// 其余（心跳注释、未进入流式前的普通 JSON 错误响应等）原样透传。
	if _, err := w.inner.Write(p); err != nil {
		return len(p), err
	}
	w.Flush()
	return len(p), nil
}

// translateFrame 翻译一个 data 帧：chat chunk → 事件序列；[DONE] → 收尾事件；
// 上游 error 帧 → response.failed；非 JSON → 原样。
func (w *responsesStreamWriter) translateFrame(payload string) []string {
	if payload == "[DONE]" {
		return w.completionEvents()
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return []string{payload}
	}
	if _, hasErr := chunk["error"]; hasErr {
		return w.failedEvents(chunk)
	}
	return w.chatChunkEvents(chunk)
}

// chatChunkEvents 翻译一个 chat chunk（choices[0].delta）为 Responses 事件。
func (w *responsesStreamWriter) chatChunkEvents(chunk map[string]any) []string {
	st := w.st
	var evs []string
	if u, ok := chunk["usage"].(map[string]any); ok {
		st.usage = u
	}
	choices, _ := chunk["choices"].([]any)
	var delta map[string]any
	finish := ""
	if len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			delta, _ = c["delta"].(map[string]any)
			finish, _ = c["finish_reason"].(string)
		}
	}
	if !st.started {
		st.started = true
		st.itemID = "msg_" + randHex(8)
		st.model, _ = chunk["model"].(string)
		evs = append(evs,
			st.event("response.created", map[string]any{"response": st.partialResponse("in_progress")}),
			st.event("response.in_progress", nil),
			st.event("response.output_item.added", map[string]any{
				"output_index": 0,
				"item":         map[string]any{"id": st.itemID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}},
			}),
			st.event("response.content_part.added", map[string]any{
				"item_id": st.itemID, "output_index": 0, "content_index": 0,
				"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
			}),
		)
	}
	if delta != nil {
		if text, ok := delta["content"].(string); ok && text != "" {
			st.text.WriteString(text)
			evs = append(evs, st.event("response.output_text.delta", map[string]any{
				"item_id": st.itemID, "output_index": 0, "content_index": 0, "delta": text,
			}))
		}
		if tcs, ok := delta["tool_calls"].([]any); ok {
			evs = append(evs, w.toolCallEvents(tcs)...)
		}
	}
	if finish != "" && !st.done {
		evs = append(evs, w.completionEvents()...)
	}
	return evs
}

// toolCallEvents 聚合 delta.tool_calls 分片为 function_call item + 参数增量事件。
func (w *responsesStreamWriter) toolCallEvents(tcs []any) []string {
	st := w.st
	var evs []string
	for _, tc := range tcs {
		raw, _ := tc.(map[string]any)
		if raw == nil {
			continue
		}
		idx := 0
		if f, ok := raw["index"].(float64); ok {
			idx = int(f)
		}
		key := strconv.Itoa(idx)
		ts, ok := st.toolCalls[key]
		if !ok {
			ts = &respToolState{index: idx, id: "fc_" + randHex(8)}
			st.toolCalls[key] = ts
			st.toolOrder = append(st.toolOrder, key)
			if fn, ok := raw["function"].(map[string]any); ok {
				ts.name, _ = fn["name"].(string)
			}
			if id, ok := raw["id"].(string); ok && id != "" {
				ts.callID = id
			}
			evs = append(evs, st.event("response.output_item.added", map[string]any{
				"output_index": 0,
				"item":         map[string]any{"id": ts.id, "type": "function_call", "call_id": ts.callID, "name": ts.name, "arguments": "", "status": "in_progress"},
			}))
		}
		if fn, ok := raw["function"].(map[string]any); ok {
			if a, ok := fn["arguments"].(string); ok && a != "" {
				ts.args += a
				evs = append(evs, st.event("response.function_call_arguments.delta", map[string]any{
					"item_id": ts.id, "output_index": 0, "delta": a,
				}))
			}
		}
	}
	return evs
}

// completionEvents 收尾事件序列：output_text.done → content_part.done →
// output_item.done（message + function_call 项）→ response.completed。只发一次。
func (w *responsesStreamWriter) completionEvents() []string {
	st := w.st
	if st.done {
		return nil
	}
	st.done = true
	text := st.text.String()
	evs := []string{
		st.event("response.output_text.done", map[string]any{
			"item_id": st.itemID, "output_index": 0, "content_index": 0,
			"text": text, "annotations": []any{},
		}),
		st.event("response.content_part.done", map[string]any{
			"item_id": st.itemID, "output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		}),
		st.event("response.output_item.done", map[string]any{
			"item_id": st.itemID, "output_index": 0,
			"item": map[string]any{"id": st.itemID, "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}},
		}),
	}
	for _, key := range st.toolOrder {
		ts := st.toolCalls[key]
		evs = append(evs,
			st.event("response.function_call_arguments.done", map[string]any{
				"item_id": ts.id, "output_index": 0, "arguments": ts.args,
			}),
			st.event("response.output_item.done", map[string]any{
				"item_id": ts.id, "output_index": 0,
				"item": map[string]any{"id": ts.id, "type": "function_call", "call_id": ts.callID, "name": ts.name, "arguments": ts.args, "status": "completed"},
			}),
		)
	}
	evs = append(evs, st.event("response.completed", map[string]any{
		"response": st.partialResponse("completed"),
	}))
	return evs
}

// failedEvents 上游 error 帧 → response.failed（已开始则带已累积输出）。
func (w *responsesStreamWriter) failedEvents(chunk map[string]any) []string {
	st := w.st
	if st.done {
		return nil
	}
	st.done = true
	errObj, _ := chunk["error"].(map[string]any)
	if !st.started {
		st.model, _ = chunk["model"].(string)
	}
	evs := []string{st.event("response.failed", map[string]any{
		"response": map[string]any{
			"id": st.respID, "object": "response", "created_at": st.created,
			"status": "failed", "model": st.model, "output": []any{}, "error": errObj,
		},
	})}
	return evs
}
