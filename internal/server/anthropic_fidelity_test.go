package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
)

// TestTransformOpenAIToAnthropicStopReason 断言非流式 Anthropic 响应的 stop_reason 保真。
//
// 缺陷背景（2026-11-17）：anthropicResp 原本没有 stop_reason 字段，
// 而 Anthropic Messages API 规范里它是必需字段。上游 OpenAI 的 finish_reason
// 完全被丢弃，于是「被 max_tokens 截断」和「正常结束」在 Anthropic 客户端看来一模一样
// —— 客户端无法判断该不该续写，也不会告警。
func TestTransformOpenAIToAnthropicStopReason(t *testing.T) {
	cases := []struct {
		finish   string
		want     string
		resource string
	}{
		{"stop", "end_turn", "正常结束"},
		{"length", "max_tokens", "被 max_tokens 截断"},
		{"tool_calls", "tool_use", "上游给了工具调用"},
		{"content_filter", "end_turn", "未映射的 finish_reason 必须退到 end_turn 而不是留空"},
		{"", "end_turn", "上游没给 finish_reason（缺省不是异常）"},
	}
	for _, c := range cases {
		raw := `{"id":"chatcmpl-abc","model":"glm-5.2","choices":[{"message":{"content":"hi"},"finish_reason":"` +
			c.finish + `"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
		var o openAICompletionResp
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			t.Fatalf("前置：解析样例失败: %v", err)
		}

		got := transformOpenAIToAnthropic(o)
		if got.StopReason != c.want {
			t.Errorf("%s（finish_reason=%q）：StopReason=%q，期望 %q", c.resource, c.finish, got.StopReason, c.want)
		}

		// 只断言结构体字段不够：要确认它真的出现在 wire JSON 里（否则客户端读不到）。
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		if !strings.Contains(string(b), `"stop_reason":"`+c.want+`"`) {
			t.Errorf("%s：wire JSON 里没有 stop_reason=%q，实际: %s", c.resource, c.want, b)
		}
	}
}

// TestTransformOpenAIToAnthropicKeepsModelAndUsage 断言 model 与 usage 从上游原样带出。
//
// 缺陷背景：流式分支的 message_start 把 model 硬编码成 ""、message_delta 把
// output_tokens 硬编码成 0（见 anthropic.go 的 anthropicStream）。非流式这条
// 路径必须先把「保真」立住，才谈得上流式向它看齐。
func TestTransformOpenAIToAnthropicKeepsModelAndUsage(t *testing.T) {
	raw := `{"id":"chatcmpl-abc","model":"glm-5.2","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7}}`
	var o openAICompletionResp
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("前置：解析样例失败: %v", err)
	}

	got := transformOpenAIToAnthropic(o)
	if got.Model != "glm-5.2" {
		t.Errorf("Model=%q，期望 glm-5.2（模型名丢失会让客户端无法对账成本）", got.Model)
	}
	if got.Usage.InputTokens != 11 || got.Usage.OutputTokens != 7 {
		t.Errorf("usage=(%d,%d)，期望 (11,7)", got.Usage.InputTokens, got.Usage.OutputTokens)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "你好" {
		t.Errorf("content 未保真: %+v", got.Content)
	}
	if got.ID != "msg_chatcmpl-abc" {
		t.Errorf("ID=%q，期望 msg_ 前缀", got.ID)
	}
}

// TestStreamInterceptWriterCapturesTerminalState 断言流式拦截器捕获上游终态。
//
// 缺陷背景：原 streamInterceptWriter 只转 delta.content，不记录 model / 上游 usage /
// finish_reason。于是 anthropicStream 结尾只能写死 stop_reason=end_turn、
// output_tokens=0 —— 流式客户端永远看不到「被截断」和真实 token 数。
func TestStreamInterceptWriterCapturesTerminalState(t *testing.T) {
	rec := newRecorder()
	sw := &streamInterceptWriter{ResponseWriter: rec, flusher: nil}

	// 上游典型 SSE：先给 model + delta，最后给 finish_reason 与 usage。
	chunks := "" +
		`data: {"id":"c1","model":"glm-5.2","choices":[{"delta":{"content":"你"}}]}` + "\n\n" +
		`data: {"id":"c1","model":"glm-5.2","choices":[{"delta":{"content":"好"}}]}` + "\n\n" +
		`data: {"id":"c1","model":"glm-5.2","choices":[{"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":9}}` + "\n\n" +
		`data: [DONE]` + "\n\n"
	if _, err := sw.Write([]byte(chunks)); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}

	if sw.model != "glm-5.2" {
		t.Errorf("捕获的 model=%q，期望 glm-5.2", sw.model)
	}
	if sw.finishReason != "length" {
		t.Errorf("捕获的 finishReason=%q，期望 length", sw.finishReason)
	}
	if sw.promptTokens != 5 || sw.completionTokens != 9 {
		t.Errorf("捕获的 usage=(%d,%d)，期望 (5,9)", sw.promptTokens, sw.completionTokens)
	}
	// 内容仍然要照常转成 Anthropic delta，不能因为加了捕获就少写事件。
	if !strings.Contains(rec.body.String(), `"text":"你"`) || !strings.Contains(rec.body.String(), `"text":"好"`) {
		t.Errorf("content_block_delta 未按原样输出: %s", rec.body.String())
	}
}

// TestStreamInterceptWriterSurvivesSplitLines 断言跨 Write 的半截行不会丢内容。
//
// 原实现已在做这件事（ReadString 失败时回填），但 0% 覆盖意味着没人验证过。
// SSE 分块到达是常态，丢了半截行就是吞字。
func TestStreamInterceptWriterSurvivesSplitLines(t *testing.T) {
	rec := newRecorder()
	sw := &streamInterceptWriter{ResponseWriter: rec, flusher: nil}

	full := `data: {"choices":[{"delta":{"content":"跨块"}}]}` + "\n\n"
	// 在任意位置切开，模拟网络分块。
	for cut := 1; cut < len(full); cut++ {
		rec.reset()
		sw.lineBuf.Reset()
		sw.done = false
		if _, err := sw.Write([]byte(full[:cut])); err != nil {
			t.Fatalf("首块写失败: %v", err)
		}
		if _, err := sw.Write([]byte(full[cut:])); err != nil {
			t.Fatalf("次块写失败: %v", err)
		}
		if !strings.Contains(rec.body.String(), `"text":"跨块"`) {
			t.Fatalf("在 %d 字节处切分后内容丢失: %q", cut, rec.body.String())
		}
	}
}

// TestAnthropicStreamReportsRealTerminalState 是流式路径的端到端验证。
//
// 前面三个用例只测了纯逻辑单元；anthropicStream 是把它们编排起来的函数，
// 改完 model/stop_reason/usage 后如果只靠单元测试，编排层仍是 0% 覆盖 —— 等于
// "改了这个函数但没验证过这个函数"。这里用 fake 上游跑一遍真实链路。
//
// 牙齿：把 message_start 的 model 改回 ""、或把 message_delta 的 stop_reason 改回
// 硬编码 end_turn / output_tokens 改回 0 → 本测试 FAIL。
func TestAnthropicStreamReportsRealTerminalState(t *testing.T) {
	const upSSE = "" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":"你好"}}]}` + "\n\n" +
		// 上游明确给了 length（被 max_tokens 截断）与 usage —— 两者都必须透传，
		// 否则客户端会把截断当正常结束、把成本算成 0。
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"glm-5.2","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":9,"total_tokens":14}}` + "\n\n" +
		`data: [DONE]` + "\n\n"

	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, upSSE, true })
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})

	body := `{"model":"glm-5.2","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))

	out := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, out)
	}
	for _, want := range []struct{ label, needle string }{
		{"message_start 回显请求 model", `"model":"glm-5.2"`},
		{"content 转成 text_delta", `"text":"你好"`},
		{"stop_reason 由 finish_reason=length 映射", `"stop_reason":"max_tokens"`},
		{"output_tokens 取上游 usage", `"output_tokens":9`},
		{"message_stop 收尾", `"type":"message_stop"`},
	} {
		if !strings.Contains(out, want.needle) {
			t.Errorf("%s：输出里没有 %s\n实际输出:\n%s", want.label, want.needle, out)
		}
	}
	// 截断绝不能被报成正常结束。
	if strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Errorf("上游 finish_reason=length 时仍报 end_turn（截断被伪装成正常结束）:\n%s", out)
	}
}

// newRecorder 是最小 ResponseWriter：只收集 body/header，模拟 Flusher 为 nil。
func newRecorder() *recorder { return &recorder{h: http.Header{}} }

type recorder struct {
	h    http.Header
	body bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.h }
func (r *recorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}
func (r *recorder) WriteHeader(int) {}
func (r *recorder) reset()          { r.body.Reset() }
