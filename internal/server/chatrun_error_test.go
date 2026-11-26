// chatrun_error_test.go 客户端取消与上游 5xx 的末端语义回归。
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/upstream"
)

// 客户端取消不是上游故障：不能喂 NoteFailures、不能把 503 记成上游错误。
func TestClientCancelNotPenalized(t *testing.T) {
	acct := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	p := testPoolWith(acct)
	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})
	run := &chatRun{h: h, st: newChatStat(time.Now(), "m", true), heldUID: "u1"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)).WithContext(ctx)

	if got := run.handleTransportError(r, acct, context.Canceled, 0, time.Now()); got != errHandled {
		t.Fatalf("outcome=%v want errHandled（客户端已走，不再写响应）", got)
	}
	if run.st.status != statusClientClosedRequest || run.st.outcome != reqlog.OutcomeInterrupted {
		t.Fatalf("status/outcome=%d/%q", run.st.status, run.st.outcome)
	}
	if run.heldUID != "" {
		t.Fatalf("heldUID=%q，租约未释放", run.heldUID)
	}
	for _, st := range p.List() {
		if st.UID == "u1" && (st.ConsecutiveFails != 0 || st.ErrTotal != 0) {
			t.Fatalf("客户端取消被计成账号失败: fails=%d err_total=%d", st.ConsecutiveFails, st.ErrTotal)
		}
	}
}

// 上游 5xx 的原文不能因轮转收尾丢成 no_healthy_account。
func TestUpstream503BodyPassthrough(t *testing.T) {
	const upstreamBody = `{"code":50001,"msg":"upstream overloaded","requestId":"up-503-1"}`
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 503, upstreamBody, false })
	acct := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	h := NewHandler(Config{Pool: testPoolWith(acct), Upstream: up})
	rec := doChat(t, h, "", `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":false}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "upstream overloaded") || !strings.Contains(body, "up-503-1") {
		t.Fatalf("上游 5xx 原文/requestId 丢失: %s", body)
	}
	if strings.Contains(body, "no_healthy_account") {
		t.Fatalf("上游 5xx 被误报成本地无可用号: %s", body)
	}
}

func TestUpstreamMessageText(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"CodeBuddy 信封", `{"code":50001,"msg":"upstream overloaded","requestId":"up-1"}`, "50001: upstream overloaded (requestId up-1)"},
		{"OpenAI 信封", `{"error":{"message":"quota exhausted","code":"upstream_error"}}`, "upstream_error: quota exhausted"},
		{"非 JSON 原文", "panic: runtime error", "panic: runtime error"},
		{"空体", "  ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := upstreamMessageText(c.raw); got != c.want {
				t.Errorf("upstreamMessageText(%q)=%q want %q", c.raw, got, c.want)
			}
		})
	}
}
