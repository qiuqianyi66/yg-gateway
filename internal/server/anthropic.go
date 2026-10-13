package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []struct {
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
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
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
	var text string
	if len(o.Choices) > 0 {
		text = o.Choices[0].Message.Content
	}
	return anthropicResp{
		ID:    "msg_" + o.ID,
		Type:  "message",
		Role:  "assistant",
		Model: o.Model,
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}

	var peek struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &peek)

	openAIBody, err := transformAnthropicToOpenAI(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "transform: "+err.Error())
		return
	}

	internalReq := r.Clone(r.Context())
	internalReq.Body = io.NopCloser(bytes.NewReader(openAIBody))
	internalReq.Header.Set("Content-Type", "application/json")

	if peek.Stream {
		h.anthropicStream(w, internalReq)
		return
	}

	buf := &bytes.Buffer{}
	iw := &interceptWriter{ResponseWriter: w, buf: buf}
	h.chatCompletions(iw, internalReq)

	if iw.status >= 400 {
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
func (h *Handler) anthropicStream(w http.ResponseWriter, r *http.Request) {
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
			"model":   "",
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

	// 结尾事件。
	writeSSEEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSEEvent(w, flusher, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]string{"stop_reason": "end_turn"},
		"usage": map[string]int{"output_tokens": 0},
	})
	writeSSEEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
	if flusher != nil {
		flusher.Flush()
	}
}

// writeSSEEvent 写一个 Anthropic SSE 事件块（event: xxx\ndata: {...}\n\n）。
func writeSSEEvent(w http.ResponseWriter, f http.Flusher, event string, data any) {
	b, _ := json.Marshal(data)
	w.Write([]byte("event: " + event + "\ndata: " + string(b) + "\n\n"))
	if f != nil {
		f.Flush()
	}
}

// streamInterceptWriter 逐行拦截 OpenAI SSE，把 delta.content 转成 Anthropic content_block_delta。
type streamInterceptWriter struct {
	http.ResponseWriter
	flusher http.Flusher
	lineBuf bytes.Buffer
	done    bool
}

func (sw *streamInterceptWriter) Write(b []byte) (int, error) {
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
		// 解析 OpenAI delta：{"choices":[{"delta":{"content":"xxx"}}]}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
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
	// 流式 200 已隐式发出；不覆盖。
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
