package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// anthropicStreamIntent 解析请求体的 stream 字段，返回「是否流式」与一条**诊断说明**（note）。
//
// 三态语义（与 REQ-usage-unknown-admission 同一条纪律：**未知不得冒充某个具体结论**）：
//   - 字段合法（true/false）：note 为空；
//   - 字段缺失：保持既有默认（非流式），note 为空——缺省不是异常；
//   - 字段**类型非法**（如 `"stream":"true"` 字符串）或 body 非法 JSON：**未知**。
//     行为保持既有（走非流式），但必须返回 note 让调用方留痕：
//     旧实现 `_ = json.Unmarshal(body, &peek)` 把类型错静默吞掉，于是「我们不知道客户端想要什么」
//     被记成了「客户端要非流式」，客户端也不会知道自己的请求被降级了。
//
// 为什么只留痕不改分支：改分支选择是**行为变更**（可能把非流式请求改派到流式路径），
// 应当单独走 PRD + 冒烟，不混在可观测性修复里。
func anthropicStreamIntent(body []byte) (stream bool, note string) {
	var peek struct {
		Stream json.RawMessage `json:"stream"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		return false, "body 不是合法 JSON（" + err.Error() + "）：按非流式处理，后续 transform 会返回 400"
	}
	if len(peek.Stream) == 0 {
		return false, ""
	}
	var s bool
	if err := json.Unmarshal(peek.Stream, &s); err != nil {
		return false, "stream 字段类型非法（" + strings.TrimSpace(string(peek.Stream)) + "）：按非流式处理；" +
			"若客户端本意是流式，请把该字段改成布尔值"
	}
	return s, ""
}

// anthropicReq / anthropicResp 是 Anthropic Messages API 的最小子集。
// 只实现 MVP 非流式所需字段；其余字段（tools / stop_sequences / metadata）忽略。
// 文档：https://docs.anthropic.com/en/api/messages
type anthropicReq struct {
	Model     string         `json:"model"`
	Messages  []anthropicMsg `json:"messages"`
	MaxTokens int64          `json:"max_tokens"`
	System    string         `json:"system,omitempty"`
	Stream    bool           `json:"stream,omitempty"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResp struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Role  string `json:"role"`
	Model string `json:"model"`
	// StopReason 是 Anthropic 规范的**必需**字段（end_turn / max_tokens / tool_use / stop_sequence）。
	// 2026-11-17 前这里没有该字段：上游 finish_reason 被丢弃，于是「被 max_tokens 截断」
	// 和「正常结束」在 Anthropic 客户端看来完全一样，客户端既不知道要不要续写，也不会告警。
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// openAICompletionResp 是 OpenAI chat completions 响应的最小子集（只取转换所需字段）。
type openAICompletionResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

// anthropicStopReason 把 OpenAI finish_reason 映射成 Anthropic stop_reason。
//
// 未知值一律退到 end_turn：规范里 stop_reason 必填，留空会让客户端解析失败；
// 而 end_turn 是"没有更具体的结论可用"时最保守的取值（与早前硬编码行为一致，
// 区别只是现在 len/工具调用能被正确识别）。缺省（上游未给）同样是 end_turn。
func anthropicStopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return "end_turn"
	}
}

// transformAnthropicToOpenAI 把 Anthropic 请求 body 转成 OpenAI chat completions body。
// 规则：
//   - system 字段 → messages 最前面插一条 role=system
//   - messages 原样透传（role/content 一一对应）
//   - max_tokens 透传
//   - model 透传（网关选号按裸名路由，Anthropic 客户端传的 model 名原样走）
func transformAnthropicToOpenAI(body []byte) ([]byte, error) {
	var areq anthropicReq
	if err := json.Unmarshal(body, &areq); err != nil {
		return nil, err
	}

	// model 是 Anthropic Messages API 的必需字段（2026-11-18 补）。
	//
	// 改前不校验：缺 model 的请求会被原样转成 `{"model":"",...}`，而下游也不校验空 model，
	// 于是网关照常选号、照常打上游、照常返回 200 —— 实测客户端拿到的是**某个真实回答**，
	// 但它指定的模型从未被使用。这是静默降级：客户端以为用上了自己的模型，
	// 账单与效果却都不对，而且没有任何信号。
	if strings.TrimSpace(areq.Model) == "" {
		return nil, errors.New("model 必填：Anthropic Messages API 要求 model 字段，" +
			"空 model 会让网关在不告知的情况下选用上游默认模型")
	}

	msgs := make([]map[string]string, 0, len(areq.Messages)+1)
	if areq.System != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": areq.System})
	}
	for _, m := range areq.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}

	oreq := map[string]any{
		"model":      areq.Model,
		"messages":   msgs,
		"max_tokens": areq.MaxTokens,
		"stream":     areq.Stream,
	}
	return json.Marshal(oreq)
}

// transformOpenAIToAnthropic 把 OpenAI 响应转成 Anthropic 响应。
func transformOpenAIToAnthropic(o openAICompletionResp) anthropicResp {
	var text, finish string
	if len(o.Choices) > 0 {
		text = o.Choices[0].Message.Content
		finish = o.Choices[0].FinishReason
	}
	return anthropicResp{
		ID:         "msg_" + o.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      o.Model,
		StopReason: anthropicStopReason(finish),
		Content: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{
			{Type: "text", Text: text},
		},
		Usage: struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		}{
			InputTokens:  o.Usage.PromptTokens,
			OutputTokens: o.Usage.CompletionTokens,
		},
	}
}

// anthropicMessages POST /v1/messages 端点（Anthropic 协议）。
// stream=false：转换 body → 内存拦截 chatCompletions → 转换响应。
// stream=true：逐 chunk 拦截 OpenAI SSE，实时转 Anthropic SSE 事件序列。
func (h *Handler) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	// 入站 body 上限同 chatCompletions（F-001）：MaxBodyBytes>0 时 MaxBytesReader
	// 截断，超限就地 413——此前裸 io.ReadAll 让 /v1/messages 绕过该 DoS 防护。
	var body []byte
	var err error
	if h.cfg.MaxBodyBytes > 0 {
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	} else {
		body, err = io.ReadAll(r.Body)
	}
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("request body exceeds %d bytes", mbe.Limit))
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}

	streamReq, streamNote := anthropicStreamIntent(body)
	if streamNote != "" {
		// 未知留痕：不再静默降级（见 anthropicStreamIntent 注释）。
		log.Printf("WARN: [server] anthropic /v1/messages: %s", streamNote)
	}

	openAIBody, err := transformAnthropicToOpenAI(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "transform: "+err.Error())
		return
	}

	internalReq := r.Clone(r.Context())
	internalReq.Body = io.NopCloser(bytes.NewReader(openAIBody))
	internalReq.Header.Set("Content-Type", "application/json")

	if streamReq {
		// Anthropic 流式要求 message_start 里带 model，而该事件必须先于上游首 chunk 发出，
		// 此时还拿不到上游 model —— 回显请求里的 model（与 Anthropic 官方语义一致：
		// 响应 model 就是客户端请求的模型）。openAIBody 由本包 Marshal 产生，解析必成功；
		// 仍显式处理 error，不吞（R10）。
		var echo struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(openAIBody, &echo); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "echo model: "+err.Error())
			return
		}
		h.anthropicStream(w, internalReq, echo.Model)
		return
	}

	buf := &bytes.Buffer{}
	iw := &interceptWriter{ResponseWriter: w, buf: buf}
	h.chatCompletions(iw, internalReq)

	if iw.status >= 400 {
		// interceptWriter 把错误状态与错误信封都吞进了 buf（WriteHeader 不穿透、
		// Write 只进 buf）——直接返回会让客户端拿到 200 空体。透传捕获的状态码
		// 与错误信封原文，与 chatCompletions 的错误口径一致。
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(iw.status)
		if _, werr := w.Write(buf.Bytes()); werr != nil {
			// 客户端已断连：错误信封送不到，无补救动作。
		}
		return
	}

	var oresp openAICompletionResp
	if err := json.Unmarshal(buf.Bytes(), &oresp); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error",
			"parse upstream response: "+err.Error())
		return
	}
	aresp := transformOpenAIToAnthropic(oresp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(aresp)
}

// anthropicStream 流式分支：把 OpenAI SSE 输出逐 chunk 转 Anthropic SSE 事件。
// echoModel 用于 message_start 回显（见 anthropicMessages 里的说明）。
func (h *Handler) anthropicStream(w http.ResponseWriter, r *http.Request, echoModel string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// 开场事件：message_start + content_block_start。
	flusher, _ := w.(http.Flusher)
	writeSSEEvent(w, flusher, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":      "msg_" + strconv.FormatInt(time.Now().UnixNano(), 10),
			"type":    "message",
			"role":    "assistant",
			"model":   echoModel,
			"content": []any{},
			"usage":   map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})
	writeSSEEvent(w, flusher, "content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]string{"type": "text", "text": ""},
	})

	sw := &streamInterceptWriter{ResponseWriter: w, flusher: flusher}
	h.chatCompletions(sw, r)

	// 上游失败必须发 error 事件（2026-11-18 修）。
	//
	// 改前：status 与错误体都被吞，客户端收到 message_start → 空 content_block →
	// message_delta(end_turn) → message_stop，把一次上游故障读成「模型回答了个空」。
	// 它不报错，所以是最难发现的一类失败。这里已无法回头改 HTTP 状态码（200 随
	// message_start 已发出），改用 Anthropic 规范的流内 error 事件，
	// 并直接返回 —— 不再补 message_stop 伪装正常收尾。
	if sw.status >= http.StatusBadRequest || len(sw.errBody) > 0 {
		writeSSEEvent(w, flusher, "error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "upstream_error", "message": upstreamErrorMessage(sw.status, sw.errBody)},
		})
		return
	}

	// 结尾事件用上游真实终态：model 来自首个带 model 的 chunk，stop_reason 由
	// finish_reason 映射（缺省时 anthropicStopReason 已退到 end_turn），output_tokens
	// 取上游 usage（捕获不到为 0）。不能再像旧实现那样无条件硬编码。
	stopReason := anthropicStopReason(sw.finishReason)
	writeSSEEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSEEvent(w, flusher, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]string{"stop_reason": stopReason},
		"usage": map[string]int64{"output_tokens": sw.completionTokens},
	})
	writeSSEEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
	if flusher != nil {
		flusher.Flush()
	}
}

// upstreamErrorMessage 把上游/网关错误体压成一句可直接展示的文本。
//
// chatCompletions 写出的错误体是 `{"error":{"message":"...","code":"..."}}`，
// 直接塞进 SSE 的 message 字段会变成转义后的嵌套 JSON（客户端还得二次解析）。
// 这里优先取 error.message，取不到就退回原文或状态码描述 —— 不丢排障信息。
func upstreamErrorMessage(status int, body []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Message != "" {
		if env.Error.Code != "" {
			return env.Error.Code + ": " + env.Error.Message
		}
		return env.Error.Message
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		return s
	}
	return fmt.Sprintf("upstream returned status %d", status)
}

// writeSSEEvent 写一个 Anthropic SSE 事件块（event: xxx\ndata: {...}\n\n）。
func writeSSEEvent(w http.ResponseWriter, f http.Flusher, event string, data any) {
	// 忽略：data 都是网关自建的事件结构（无可序列化失败的类型），Marshal 失败只会发出空 data，
	// 不影响流继续推进与已写事件。
	b, _ := json.Marshal(data)
	w.Write([]byte("event: " + event + "\ndata: " + string(b) + "\n\n"))
	if f != nil {
		f.Flush()
	}
}

// streamInterceptWriter 逐行拦截 OpenAI SSE，把 delta.content 转成 Anthropic content_block_delta。
//
// 同时捕获上游终态（model / usage / finish_reason）：这四项决定了 Anthropic SSE 里
// message_start 的 model、message_delta 的 stop_reason 与 output_tokens。
// 2026-11-17 前它们被硬编码成 ""/0/0 —— 流式客户端既看不到真实模型名，
// 也永远看不到「被 max_tokens 截断」，成本核算与续写判断都失去了依据。
type streamInterceptWriter struct {
	http.ResponseWriter
	flusher http.Flusher
	lineBuf bytes.Buffer
	done    bool
	// 捕获项（最后出现的值生效：上游通常只在最后一个 chunk 给 usage/finish_reason）。
	model            string
	finishReason     string
	promptTokens     int64
	completionTokens int64
	// status / errBody：上游失败留痕。HTTP 200 已随 message_start 隐式发出、改不了状态码，
	// 但失败不能就此沉默 —— 否则客户端会收到一个「正常结束的空回答」（2026-11-18 修）。
	status  int
	errBody []byte
}

func (sw *streamInterceptWriter) Write(b []byte) (int, error) {
	// 上游失败：chatCompletions 写的是 JSON 错误体（没有 `data: ` 前缀），
	// SSE 解析会把它当垃圾行丢弃。原样留存，供 anthropicStream 发 error 事件。
	if sw.status >= http.StatusBadRequest {
		sw.errBody = append(sw.errBody, b...)
		return len(b), nil
	}
	if sw.done {
		return len(b), nil
	}
	sw.lineBuf.Write(b)
	for {
		line, err := sw.lineBuf.ReadString('\n')
		if err != nil {
			// 半截行：留着等下次 Write。
			sw.lineBuf.WriteString(line)
			break
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sw.done = true
			break
		}
		// 解析 OpenAI chunk：delta.content 用于转换，model/finish_reason/usage 用于终态。
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Model != "" {
			sw.model = chunk.Model
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != "" {
			sw.finishReason = chunk.Choices[0].FinishReason
		}
		if chunk.Usage != nil {
			sw.promptTokens = chunk.Usage.PromptTokens
			sw.completionTokens = chunk.Usage.CompletionTokens
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			writeSSEEvent(sw.ResponseWriter, sw.flusher, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]string{"type": "text_delta", "text": chunk.Choices[0].Delta.Content},
			})
		}
	}
	return len(b), nil
}

func (sw *streamInterceptWriter) Header() http.Header {
	return sw.ResponseWriter.Header()
}

func (sw *streamInterceptWriter) WriteHeader(code int) {
	// 状态码不穿透（HTTP 200 已随 message_start 隐式发出，改不了），
	// 但必须记下来：改前这里是空实现，上游 5xx 被彻底吞成一个「正常空流」。
	sw.status = code
}

// interceptWriter 拦截 ResponseWriter：记录状态码 + body 到内存，供转换后再写。
type interceptWriter struct {
	http.ResponseWriter
	buf    *bytes.Buffer
	status int
}

func (iw *interceptWriter) WriteHeader(code int) {
	iw.status = code
}

func (iw *interceptWriter) Write(b []byte) (int, error) {
	if iw.status == 0 {
		iw.status = 200
	}
	return iw.buf.Write(b)
}

func (iw *interceptWriter) Header() http.Header {
	return iw.ResponseWriter.Header()
}
