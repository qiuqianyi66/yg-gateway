package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestChatGlobalAdmission429 全局准入超限时返回 429 + Retry-After（ADR-003 D-401）。
//
// 牙齿：把 chatCompletions 里的 AcquireGlobal 判定删掉 → 请求会继续走到选号，
// 拿到 503 而非 429 → 本测试 FAIL。
func TestChatGlobalAdmission429(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	p := testPoolWith()
	p.SetGlobalMaxInFlight(1)
	h := NewHandler(Config{Pool: p, Upstream: up})

	// 先占满唯一名额（模拟一个在途请求）。
	if !p.AcquireGlobal() {
		t.Fatal("precondition: 应能占用第一个名额")
	}
	defer p.ReleaseGlobal()

	rec := doChat(t, h, "", `{"model":"m","messages":[]}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超限应 429，got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("应带 Retry-After: 1，got %q", got)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON 信封: %v", err)
	}
	if body.Error.Code != "server_busy" {
		t.Fatalf("error.code = %q, want server_busy", body.Error.Code)
	}
}

// TestChatGlobalAdmissionRelease 正常请求完成后名额必须释放（否则最终永远 429）。
//
// 这是 D-401 配对的端到端验证：连续多次请求都必须成功（若泄漏，第 2 次就 429）。
func TestChatGlobalAdmissionRelease(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	p := testPoolWith()
	p.SetGlobalMaxInFlight(1) // 只允许 1 个在途
	h := NewHandler(Config{Pool: p, Upstream: up})

	for i := 0; i < 5; i++ {
		rec := doChat(t, h, "", `{"model":"m","messages":[]}`)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("第 %d 次请求被 429：名额未释放（泄漏）", i+1)
		}
	}
	if got := p.GlobalInFlight(); got != 0 {
		t.Fatalf("全部请求完成后在途应归零，got %d", got)
	}
}

// TestChatGlobalAdmissionDisabled 未配置上限时不影响任何请求（零回归）。
func TestChatGlobalAdmissionDisabled(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	p := testPoolWith() // 默认 globalMaxInFlight = 0
	h := NewHandler(Config{Pool: p, Upstream: up})

	for i := 0; i < 5; i++ {
		rec := doChat(t, h, "", `{"model":"m","messages":[]}`)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("未配置上限时不应 429（零回归），第 %d 次", i+1)
		}
	}
}
