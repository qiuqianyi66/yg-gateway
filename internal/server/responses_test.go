// responses_test.go /v1/responses 端点（F8）测试：转换 A（responses→chat）、
// 转换 B（chat→response 对象 / 流式事件）、端到端 handler（非流式 + 流式）。
package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
)

// TestResponsesToChatString 转换 A：input=string + instructions + tools 直通。
func TestResponsesToChatString(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","instructions":"你是网关","input":"你好","stream":false,"max_output_tokens":32,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"temperature":0.2,"response_format":{"type":"json_object"}}`)
	out, err := responsesToChat(body)
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	var chat map[string]any
	if err := json.Unmarshal(out, &chat); err != nil {
		t.Fatalf("chat body not JSON: %v", err)
	}
	if chat["model"] != "glm-5.2" {
		t.Errorf("model = %v", chat["model"])
	}
	msgs, _ := chat["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages (system+user), got %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	usr := msgs[1].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "你是网关" {
		t.Errorf("system msg = %v", sys)
	}
	if usr["role"] != "user" || usr["content"] != "你好" {
		t.Errorf("user msg = %v", usr)
	}
	if chat["max_tokens"] != float64(32) {
		t.Errorf("max_tokens = %v", chat["max_tokens"])
	}
	if chat["stream"] != false {
		t.Errorf("stream = %v", chat["stream"])
	}
	if _, ok := chat["tools"]; !ok {
		t.Error("tools not passed through")
	}
	if _, ok := chat["response_format"]; !ok {
		t.Error("response_format not passed through")
	}
}

// TestResponsesToChatItems 转换 A：input items（message/function_call/function_call_output）。
func TestResponsesToChatItems(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":"问"},{"type":"function_call","call_id":"c1","name":"get_weather","arguments":"{\"city\":\"bj\"}"},{"type":"function_call_output","call_id":"c1","output":"{\"temp\":20}"}]}`)
	out, err := responsesToChat(body)
	if err != nil {
		t.Fatalf("responsesToChat: %v", err)
	}
	var chat map[string]any
	_ = json.Unmarshal(out, &chat)
	msgs, _ := chat["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	if msgs[1].(map[string]any)["role"] != "assistant" {
		t.Errorf("function_call should map to assistant with tool_calls: %v", msgs[1])
	}
	tc, _ := msgs[1].(map[string]any)["tool_calls"].([]any)
	if len(tc) != 1 {
		t.Fatalf("tool_calls len = %d", len(tc))
	}
	fn := tc[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("tool name = %v", fn["name"])
	}
	if msgs[2].(map[string]any)["role"] != "tool" {
		t.Errorf("function_call_output should map to tool msg: %v", msgs[2])
	}
}

// TestResponsesToChatErrors 转换 A：缺 input / 非法 input 报错。
func TestResponsesToChatErrors(t *testing.T) {
	if _, err := responsesToChat([]byte(`{"model":"m"}`)); err == nil {
		t.Error("missing input must error")
	}
	if _, err := responsesToChat([]byte(`{"model":"m","input":42}`)); err == nil {
		t.Error("non-string/array input must error")
	}
	if _, err := responsesToChat([]byte(`{not json`)); err == nil {
		t.Error("bad json must error")
	}
}

// TestChatRespToResponses 转换 B（非流式）：正文 + usage + tool_calls → response 对象。
func TestChatRespToResponses(t *testing.T) {
	chat := map[string]any{
		"model": "glm-5.2",
		"choices": []any{map[string]any{"message": map[string]any{
			"content": "你好",
			"tool_calls": []any{map[string]any{
				"id": "call_x", "type": "function",
				"function": map[string]any{"name": "f", "arguments": `{"a":1}`},
			}},
		}}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
	r := chatRespToResponses(chat, "glm-5.2")
	if r["object"] != "response" || r["status"] != "completed" {
		t.Errorf("object/status = %v/%v", r["object"], r["status"])
	}
	out, _ := r["output"].([]any)
	if len(out) != 2 {
		t.Fatalf("want 2 output items (message+function_call), got %d", len(out))
	}
	msg := out[0].(map[string]any)
	if msg["type"] != "message" || msg["role"] != "assistant" {
		t.Errorf("msg item = %v", msg)
	}
	content := msg["content"].([]any)[0].(map[string]any)
	if content["type"] != "output_text" || content["text"] != "你好" {
		t.Errorf("content part = %v", content)
	}
	fc := out[1].(map[string]any)
	if fc["type"] != "function_call" || fc["name"] != "f" || fc["arguments"] != `{"a":1}` {
		t.Errorf("fc item = %v", fc)
	}
	u, _ := r["usage"].(map[string]any)
	if u["input_tokens"] != int64(10) || u["output_tokens"] != int64(5) {
		t.Errorf("usage = %v", u)
	}
}

// TestResponsesStreamWriter 转换 B（流式）：chat chunk → 事件序列，
// [DONE] → 收尾事件，且只 completed 一次。
func TestResponsesStreamWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &responsesStreamWriter{inner: rec, st: newRespStreamState()}
	frames := []string{
		`{"id":"1","model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":"你"}}]}`,
		`{"id":"1","model":"glm-5.2","choices":[{"index":0,"delta":{"content":"好"}}]}`,
		`{"id":"1","model":"glm-5.2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		`[DONE]`,
	}
	for _, f := range frames {
		if _, err := w.Write([]byte("data: " + f + "\n\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"type":"response.created"`) {
		t.Error("missing response.created")
	}
	if !strings.Contains(out, `"type":"response.output_text.delta"`) {
		t.Error("missing output_text.delta")
	}
	if strings.Count(out, `"type":"response.completed"`) != 1 {
		t.Errorf("response.completed count = %d, want 1", strings.Count(out, `"type":"response.completed"`))
	}
	if strings.Contains(out, `data: [DONE]`) {
		t.Error("chat [DONE] must be swallowed")
	}
	if strings.Contains(out, `"object":"chat.completion.chunk"`) {
		t.Error("chat chunk format leaked into responses stream")
	}
	// 逐帧 delta 事件独立下发；拼接只在 done/completed 的最终 text 上。
	if !strings.Contains(out, `"delta":"你"`) || !strings.Contains(out, `"delta":"好"`) {
		t.Error("text deltas not passed through")
	}
	if !strings.Contains(out, `"text":"你好"`) {
		t.Error("text not aggregated in final item/completed")
	}
	if !strings.Contains(out, `"input_tokens":3`) {
		t.Error("usage not mapped in completed response")
	}
}

// TestResponsesStreamWriterToolCalls 流式 tool_call 分片 → function_call item 事件。
func TestResponsesStreamWriterToolCalls(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &responsesStreamWriter{inner: rec, st: newRespStreamState()}
	frames := []string{
		`{"id":"1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`,
		`{"id":"1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"id":"1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	}
	for _, f := range frames {
		_, _ = w.Write([]byte("data: " + f + "\n\n"))
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"type":"function_call"`) {
		t.Error("missing function_call item")
	}
	if !strings.Contains(out, `"type":"response.function_call_arguments.delta"`) {
		t.Error("missing arguments delta")
	}
	if !strings.Contains(out, `"arguments":"{\"a\":1}"`) {
		t.Errorf("arguments not aggregated: %s", out)
	}
}

// TestResponsesHandlerNonStream 端到端：/v1/responses 非流式 → response 对象。
func TestResponsesHandlerNonStream(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1753600000,"model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":"{\"ok\": true}"}}]}` + "\n\n" +
			`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}` + "\n\n" +
			"data: [DONE]\n\n", true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	reqBody := `{"model":"glm-5.2","input":"返回 JSON","stream":false}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(reqBody))))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v body=%s", err, rec.Body.String())
	}
	if resp["object"] != "response" {
		t.Fatalf("object = %v", resp["object"])
	}
	out, _ := resp["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("output len = %d", len(out))
	}
	text := out[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if text != `{"ok": true}` {
		t.Errorf("text = %v", text)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %s", ct)
	}
}

// TestResponsesHandlerStream 端到端：/v1/responses 流式 → Responses 事件流。
func TestResponsesHandlerStream(t *testing.T) {
	const sseFrames = "data: " + `{"id":"chatcmpl-1","model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}` + "\n\n" +
		"data: " + `{"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n" +
		"data: [DONE]\n\n"
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseFrames, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	reqBody := `{"model":"glm-5.2","input":"hello","stream":true}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(reqBody))))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"response.created", "response.output_text.delta", "response.output_item.done", "response.completed"} {
		if !strings.Contains(body, `"type":"`+want+`"`) {
			t.Errorf("stream missing event %s", want)
		}
	}
	if strings.Contains(body, `"object":"chat.completion.chunk"`) {
		t.Error("chat chunk leaked into responses stream")
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Error("[DONE] must be swallowed")
	}
}

// TestResponsesHandlerValidation 端到端：缺 input → 400 错误信封（不触上游）。
func TestResponsesHandlerValidation(t *testing.T) {
	var calls int
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls++
		return 200, "data: [DONE]\n\n", true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", io.NopCloser(strings.NewReader(`{"model":"glm-5.2"}`))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "input") {
		t.Errorf("error body should mention input: %s", rec.Body.String())
	}
	if calls != 0 {
		t.Errorf("upstream touched on invalid request: calls=%d", calls)
	}
}
