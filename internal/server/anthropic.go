package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// anthropicReq / anthropicResp 是 Anthropic Messages API 的最小子集。
// 只实现 MVP 非流式所需字段；其余字段（tools / stop_sequences / metadata）忽略。
// 文档：https://docs.anthropic.com/en/api/messages
type anthropicReq struct {
	Model     string          `json:"model"`
	Messages  []anthropicMsg  `json:"messages"`
	MaxTokens int64           `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
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

// anthropicMessages POST /v1/messages 端点（Anthropic 协议 MVP，非流式）。
// 实现：转换 body → 内存 ResponseWriter 拦截内部 chatCompletions → 转换响应。
// 流式（stream=true）当前返回 501，后续迭代。
func (h *Handler) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}

	// stream=true 暂不支持（SSE 事件转换是另一块工作）。
	var peek struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &peek)
	if peek.Stream {
		writeOpenAIError(w, http.StatusNotImplemented, "not_implemented",
			"streaming not yet supported on /v1/messages; use stream=false")
		return
	}

	openAIBody, err := transformAnthropicToOpenAI(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "transform: "+err.Error())
		return
	}

	// 构造内部请求：复用 chatCompletions 的准入/选号/上游逻辑。
	// 用一个内存 ResponseWriter 拦截输出，再转换回 Anthropic 格式。
	internalReq := r.Clone(r.Context())
	internalReq.Body = io.NopCloser(bytes.NewReader(openAIBody))
	internalReq.Header.Set("Content-Type", "application/json")

	buf := &bytes.Buffer{}
	iw := &interceptWriter{ResponseWriter: w, buf: buf}
	h.chatCompletions(iw, internalReq)

	if iw.status >= 400 {
		// 内部错误（401/429/500 等）：原样透传，不做 Anthropic 格式转换
		// （Anthropic 客户端能读 OpenAI 错误信封里的 message 字段）。
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
