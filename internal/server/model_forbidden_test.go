// model_forbidden_test.go 11158 账号类型无模型权限：400 原文透传、不罚号、不伪装 503。
package server

import (
	"net/http"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
)

func TestModelForbiddenDoesNotPenalizeOr503(t *testing.T) {
	const upErr = `{"code":11158,"msg":"model [hy3] is not allowed for user type [personal_free_user]","requestId":"req-11158"}`
	up := newFakeUpstream(t, func(string) (int, string, bool) { return http.StatusBadRequest, upErr, false })
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := doChat(t, h, "", `{"model":"m","messages":[]}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "11158") || !strings.Contains(body, "not allowed for user type") {
		t.Fatalf("11158 原文/requestId 丢失: %s", body)
	}
	if strings.Contains(body, "no_healthy_account") {
		t.Fatalf("11158 被伪装成池故障: %s", body)
	}
	for _, st := range p.List() {
		if st.ConsecutiveFails != 0 || st.ErrTotal != 0 {
			t.Fatalf("11158 被罚号 %s: fails=%d err_total=%d", st.UID, st.ConsecutiveFails, st.ErrTotal)
		}
	}
}
