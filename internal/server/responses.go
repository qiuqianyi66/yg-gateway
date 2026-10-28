// responses.go OpenAI Responses API 兼容端点（F8，REQ-responses-endpoint-2026-10-28）。
//
// 设计：转换 A（responses 请求 → chat body）后复用既有 chatCompletions 管线
// （鉴权 / 选号 / 提示词改写 / 轮转 / 用量 / reqlog 全复用），出口按 outProto
// 分支做转换 B（见 responses_out.go）。流式转译在 ResponseWriter 包装层做帧级
// 转换（responsesStreamWriter），不触碰 upstream/sse.go 热路径（零回归）。
//
// v1 明确不支持（忽略不报错，语义丢失，README 已声明）：store、
// previous_response_id、metadata、reasoning。
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// respOutCtxKey 标记本请求为 /v1/responses（chatCompletions 据此设 run.outProto）。
type respOutCtxKey struct{}

// isResponsesOut 判断请求是否走 /v1/responses 出口（转换 B 分支）。
func isResponsesOut(r *http.Request) bool {
	if r == nil {
		return false
	}
	v, _ := r.Context().Value(respOutCtxKey{}).(bool)
	return v
}

// responses POST /v1/responses（OpenAI Responses API 兼容）。
//
// 请求体先整读（与 chat 管线同 body 上限口径，F-001），转成等价 chat body 后
// 换进 r.Body，打 outProto 标记，再调既有 chatCompletions。流式请求额外包一层
// responsesStreamWriter 做帧级事件转译。
func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
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
	chatBody, err := responsesToChat(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(chatBody))
	r.ContentLength = int64(len(chatBody))
	r = r.WithContext(context.WithValue(r.Context(), respOutCtxKey{}, true))
	if streamOf(body) {
		w = &responsesStreamWriter{inner: w, st: newRespStreamState()}
	}
	h.chatCompletions(w, r)
}

// streamOf 从原始 responses 请求体取 stream 布尔（转换 A 前判定是否包流式转译层）。
func streamOf(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return false
	}
	return v.Stream
}

// responsesToChat 把 Responses 请求体转成等价 chat/completions body（转换 A）。
func responsesToChat(body []byte) ([]byte, error) {
	var req struct {
		Model           json.RawMessage `json:"model"`
		Instructions    string          `json:"instructions"`
		Input           json.RawMessage `json:"input"`
		Stream          bool            `json:"stream"`
		MaxOutputTokens *int            `json:"max_output_tokens"`
		Tools           json.RawMessage `json:"tools"`
		ToolChoice      json.RawMessage `json:"tool_choice"`
		Temperature     *float64        `json:"temperature"`
		TopP            *float64        `json:"top_p"`
		Seed            *int            `json:"seed"`
		Stop            json.RawMessage `json:"stop"`
		ResponseFormat  json.RawMessage `json:"response_format"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid responses request: %v", err)
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		return nil, errors.New("missing required field: input")
	}
	msgs, err := inputToMessages(req.Input)
	if err != nil {
		return nil, err
	}
	if req.Instructions != "" {
		msgs = append([]map[string]any{{"role": "system", "content": req.Instructions}}, msgs...)
	}
	out := map[string]any{
		"messages": msgs,
		"stream":   req.Stream,
	}
	// 字段级直通：RawMessage 原样搬（缺省/null 跳过，与 chat 端点缺失语义一致）。
	if len(req.Model) > 0 && string(req.Model) != "null" {
		out["model"] = json.RawMessage(req.Model)
	}
	if req.MaxOutputTokens != nil {
		out["max_tokens"] = *req.MaxOutputTokens
	}
	if len(req.Tools) > 0 && string(req.Tools) != "null" {
		out["tools"] = json.RawMessage(req.Tools)
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		out["tool_choice"] = json.RawMessage(req.ToolChoice)
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.Seed != nil {
		out["seed"] = *req.Seed
	}
	if len(req.Stop) > 0 && string(req.Stop) != "null" {
		out["stop"] = json.RawMessage(req.Stop)
	}
	if len(req.ResponseFormat) > 0 && string(req.ResponseFormat) != "null" {
		out["response_format"] = json.RawMessage(req.ResponseFormat)
	}
	return json.Marshal(out)
}

// inputToMessages 把 responses input（string | items 数组）转成 chat messages。
func inputToMessages(input json.RawMessage) ([]map[string]any, error) {
	var s string
	if err := json.Unmarshal(input, &s); err == nil {
		return []map[string]any{{"role": "user", "content": s}}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, errors.New("input must be a string or an array of items")
	}
	var msgs []map[string]any
	for _, raw := range items {
		var it struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
			Output    string          `json:"output"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return nil, fmt.Errorf("invalid input item: %v", err)
		}
		switch it.Type {
		case "", "message":
			role := it.Role
			if role == "" {
				role = "user"
			}
			if role != "user" && role != "assistant" && role != "system" {
				return nil, fmt.Errorf("unsupported message role %q", role)
			}
			content, err := contentToChat(it.Content)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, map[string]any{"role": role, "content": content})
		case "function_call":
			// 历史中的函数调用 → assistant tool_calls（回放给上游保持工具循环上下文）。
			callID := it.CallID
			if callID == "" {
				callID = "call_" + randHex(6)
			}
			msgs = append(msgs, map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id": callID, "type": "function",
					"function": map[string]any{"name": it.Name, "arguments": it.Arguments},
				}},
			})
		case "function_call_output":
			msgs = append(msgs, map[string]any{
				"role": "tool", "tool_call_id": it.CallID, "content": it.Output,
			})
		case "reasoning":
			// 推理摘要不回放给上游（上游自行计算）；跳过。
		default:
			// 未知 item 类型：宽容跳过（不把客户端搞挂），REQ 已记录。
		}
	}
	if len(msgs) == 0 {
		return nil, errors.New("input contained no usable messages")
	}
	return msgs, nil
}

// contentToChat 把 responses content（string | parts 数组）转成 chat content。
func contentToChat(raw json.RawMessage) (any, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, errors.New("invalid content: must be a string or an array of parts")
	}
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		typ, _ := p["type"].(string)
		switch typ {
		case "input_text", "output_text", "text":
			t, _ := p["text"].(string)
			out = append(out, map[string]any{"type": "text", "text": t})
		case "input_image":
			url := ""
			if img, ok := p["image_url"].(map[string]any); ok {
				if u, ok := img["url"].(string); ok {
					url = u
				}
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		default:
			// 未识别 part：原样透传（保持宽容），由上游裁决。
			out = append(out, p)
		}
	}
	return out, nil
}

// randHex 生成 n 字节随机数的 hex 串（id 用）。熵源故障时回退时间戳（不可恢复故障，
// 不回退业务错误）。
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
