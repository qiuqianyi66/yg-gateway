package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
)

// TestAnthropicStreamSurfacesUpstreamFailure 守护的是流式路径最坏的一种失败：
// 上游报错，但客户端看到的是一个「正常结束的空回答」。
//
// 改前的链路：
//  1. anthropicStream 先写 message_start（HTTP 200 已经隐式发出，无法再改状态码）
//  2. chatCompletions 失败时调 w.WriteHeader(503) —— 被 streamInterceptWriter.WriteHeader
//     的空实现吞掉（该方法是 0% 覆盖的死防御）
//  3. 随后写入的错误 JSON body 不含 `data: ` 前缀 —— 被 Write 里的 SSE 解析丢弃
//  4. 客户端收到 message_start → message_stop，content 为空，stop_reason=end_turn
//
// 结果：一次上游故障被客户端读成「模型回答了个空」。它不报错，所以最难发现。
//
// Anthropic 流式规范里这种情况应发 `event: error`。本用例要求：
// 流里必须出现 error 事件，且 stop_reason 不能是 end_turn。
func TestAnthropicStreamSurfacesUpstreamFailure(t *testing.T) {
	const upErr = `{"error":{"message":"quota exhausted","type":"upstream_error"}}`
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return http.StatusServiceUnavailable, upErr, false
	})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})

	body := `{"model":"glm-5.2","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))

	out := rec.Body.String()

	// 关键断言 1：必须有 error 事件，客户端才知道这次流失败了。
	if !strings.Contains(out, "event: error") {
		t.Errorf("上游失败时流里必须发 error 事件，实际输出:\n%s", out)
	}
	// 关键断言 2：不能把失败伪装成正常收尾。
	if strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Errorf("上游失败不该以 end_turn 收尾（客户端会读成「模型答了空」）:\n%s", out)
	}
	// 关键断言 3：错误必须可排障 —— 要么带上游原文，要么带网关错误码。
	// 这里池里只有一个号，上游 503 触发换号重试后池耗尽，chatCompletions 写的是
	// 503 no_healthy_account 信封，所以断言落在错误码上（这是实测行为）。
	if !strings.Contains(out, "no_healthy_account") {
		t.Errorf("error 事件应带可排障的错误码:\n%s", out)
	}
	// 关键断言 4：message 必须是可读文本，不能是转义后的嵌套 JSON。
	if strings.Contains(out, `\"error\":{\"message\"`) {
		t.Errorf("error message 不应是转义后的嵌套 JSON（客户端得二次解析）:\n%s", out)
	}
}

// TestAnthropicStreamSurfacesMidStreamError 守护「流已经开始、上游中途报错」这条路径。
//
// 改前：`Write` 只认 `data: ` 前缀再 Unmarshal 成 OpenAI chunk；错误帧
// `data: {"error":{...}}` 能成功 Unmarshal（都是合法 JSON），但解析出的 chunk
// 没有 choices，于是被当成空 chunk 直接跳过 —— **错误帧被静默丢弃**。
// 上游随后断开、不发 [DONE]，`done` 保持 false、`finishReason` 为空，
// 收尾走 anthropicStopReason("") → end_turn，客户端读成「模型正常答完」。
//
// 这条比整段失败更隐蔽：客户端已经收到部分内容了，更容易相信结果完整。
func TestAnthropicStreamSurfacesMidStreamError(t *testing.T) {
	midStream := `data: {"model":"glm-5.2","choices":[{"delta":{"content":"你好"},"finish_reason":""}]}` + "\n\n" +
		`data: {"error":{"message":"upstream reset by peer","type":"stream_error"}}` + "\n\n"
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, midStream, true })
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})

	body := `{"model":"glm-5.2","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))

	out := rec.Body.String()

	if !strings.Contains(out, "event: error") {
		t.Errorf("流中途上游报错必须发 error 事件，实际输出:\n%s", out)
	}
	if !strings.Contains(out, "upstream reset by peer") {
		t.Errorf("错误帧里的 message 应传给客户端（否则丢了唯一的排障线索）:\n%s", out)
	}
	// 已收到的正文不能因为报错被丢掉 —— 部分内容对客户端仍有价值。
	if !strings.Contains(out, "你好") {
		t.Errorf("已产出的 content 不应因后续报错而丢失:\n%s", out)
	}
}

// TestUpstreamErrorMessage 覆盖错误体压缩的三条分支：信封可解析、信封不可解析但有原文、
// 空 body 只剩状态码。三条都要求「不丢排障信息」——这条路径的作用就是让客户端
// 从一行 message 里看懂上游到底怎么了。
func TestUpstreamErrorMessage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "信封带 code 与 message",
			status: 503,
			body:   `{"error":{"code":"no_healthy_account","message":"all accounts are temporarily unavailable"}}`,
			want:   "no_healthy_account: all accounts are temporarily unavailable",
		},
		{
			name:   "信封只有 message",
			status: 502,
			body:   `{"error":{"message":"upstream stream contained no valid data events"}}`,
			want:   "upstream stream contained no valid data events",
		},
		{
			name:   "不是 JSON，退回原文",
			status: 500,
			body:   "panic: runtime error",
			want:   "panic: runtime error",
		},
		{
			name:   "空 body，只剩状态码",
			status: 504,
			body:   "",
			want:   "upstream returned status 504",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := upstreamErrorMessage(c.status, []byte(c.body)); got != c.want {
				t.Errorf("upstreamErrorMessage() = %q, want %q", got, c.want)
			}
		})
	}
}
