package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
)

// 这三个用例补的是 anthropicMessages 的错误路径（改前 68.2% 覆盖，缺的正是这些分支）。
// 它们不是"为了凑覆盖率"——每条都是客户端真会遇到的情形：
// 发坏 JSON、上游 5xx、上游返回非 JSON。

// TestAnthropicMessagesRejectsNonJSONBody：body 不是合法 JSON 时必须 400，且错误来自 transform。
//
// 注意 anthropicStreamIntent 对坏 JSON 只是留一条 WARN 不阻断（Unknown 不得冒充结论），
// 真正拒绝发生在 transformAnthropicToOpenAI。若这里变成 500 或 200 空体，
// 客户端会以为请求被受理了。
func TestAnthropicMessagesRejectsNonJSONBody(t *testing.T) {
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, `{}`, false }),
	})
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader("not-json")))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "transform") {
		t.Errorf("错误信封应指明是 transform 阶段失败，实际: %s", rec.Body.String())
	}
}

// TestAnthropicMessagesRejectsMissingModel：缺 model 必须 400。
//
// 这是本轮真发现的缺陷，不是形式主义：改前不校验 model，缺 model 的请求会被转成
// `{"model":"",...}` 原样打上游并返回 200 + 真实回答（实测）—— 客户端以为用上了
// 自己指定的模型，实际是网关的默认模型。静默降级比报错危险：账单与效果都不对，
// 而且没有任何信号。
func TestAnthropicMessagesRejectsMissingModel(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})

	body := `{"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 model 应 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model") {
		t.Errorf("错误信封应指出缺 model，实际: %s", rec.Body.String())
	}
	// 否定断言：不能返回任何 Anthropic 成功响应结构。
	if strings.Contains(rec.Body.String(), `"type":"message"`) {
		t.Error("缺 model 的请求被当成功处理，返回了 message 结构")
	}
}

// TestAnthropicMessagesPassesThroughPoolExhausted：池里没有可用账号时，
// chatCompletions 的 503 必须透传（而不是被 interceptWriter 吞成 200 空体）。
//
// 这条覆盖的是 anthropicMessages 里 `iw.status >= 400` 的透传分支 ——
// 改前该分支无测试，一旦回归就是「失败被伪装成成功」。
func TestAnthropicMessagesPassesThroughPoolExhausted(t *testing.T) {
	h := NewHandler(Config{
		Pool:     testPoolWith(), // 空池
		Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true }),
	})

	body := `{"model":"glm-5.2","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("空池应 503，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusOK {
		t.Error("失败被伪装成 200")
	}
}

// TestAnthropicMessagesUnparseableUpstreamBody：上游 200 但 body 不是 JSON → **502**
// （upstream_parse），而不是 200 空响应、也不是 500。
//
// 502 的语义比 500 更准：故障责任在上游（上游给了不可解析的响应），
// 客户端据此知道「重试可能有用」。实测这条路径就是 502，断言按真实行为写。
//
// 为什么必须断言「不是 200」：空 Anthropic 响应会让客户端以为模型无话可说，
// 从而把一个网关侧故障读成一次正常但空洞的回答。
func TestAnthropicMessagesUnparseableUpstreamBody(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, "this is not json", false })
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})

	body := `{"model":"glm-5.2","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("上游 body 不可解析应 502，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("错误信封不是合法 JSON: %v，body=%s", err, rec.Body.String())
	}
	if env.Error.Code != "upstream_parse" {
		t.Errorf("错误码应为 upstream_parse（区分「上游给了坏数据」与「网关自己出错」），实际 %q", env.Error.Code)
	}
}
