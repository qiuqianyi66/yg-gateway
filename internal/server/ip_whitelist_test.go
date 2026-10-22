package server

import (
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/livecfg"
)

// cidr 解析辅助（测试内）。
func mustIPNet(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return n
}

// doChatFromRemote 发一次 chat，显式指定来源 RemoteAddr 与 XFF 头。
func doChatFromRemote(t *testing.T, h *Handler, authz, remoteAddr, xff, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestIPWhitelist_EmptyMeansUnrestricted：空白名单行为零变化，任意来源 200。
func TestIPWhitelist_EmptyMeansUnrestricted(t *testing.T) {
	keys := []httpauth.Key{{ID: "k1", Digest: sha256OfE2E("sk-a")}}
	h, _, _ := newMultiKeyHandler(t, keys, false)
	for _, addr := range []string{"8.8.8.8:5000", "1.2.3.4:5000", "203.0.113.9:5000"} {
		if rec := doChatFromRemote(t, h, "Bearer sk-a", addr, "", `{"model":"m","messages":[]}`); rec.Code != 200 {
			t.Fatalf("空白名单应恒放行，remote=%s got %d body=%s", addr, rec.Code, rec.Body.String())
		}
	}
}

// TestIPWhitelist_FailClosed：名单外来源 403 + ip_not_allowed；名单内 200。
func TestIPWhitelist_FailClosed(t *testing.T) {
	keys := []httpauth.Key{{
		ID: "k1", Digest: sha256OfE2E("sk-a"),
		AllowedCIDRs: []*net.IPNet{mustIPNet(t, "10.0.0.0/8")},
	}}
	h, _, _ := newMultiKeyHandler(t, keys, false)

	// 名单内来源
	if rec := doChatFromRemote(t, h, "Bearer sk-a", "10.1.2.3:5000", "", `{"model":"m","messages":[]}`); rec.Code != 200 {
		t.Fatalf("名单内来源应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// 名单外来源 → 403
	rec := doChatFromRemote(t, h, "Bearer sk-a", "8.8.8.8:5000", "", `{"model":"m","messages":[]}`)
	if rec.Code != 403 {
		t.Fatalf("名单外来源应 403, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ip_not_allowed") {
		t.Fatalf("错误码应为 ip_not_allowed, body=%s", rec.Body.String())
	}
}

// TestIPWhitelist_TrustedProxyUsesRealIP：配置受信代理后，真实来源取 XFF 首个非受信段，
// 名单按真实客户端 IP 判定（不被客户端伪造的 RemoteAddr/XFF 首段绕过）。
func TestIPWhitelist_TrustedProxyUsesRealIP(t *testing.T) {
	keys := []httpauth.Key{{
		ID: "k1", Digest: sha256OfE2E("sk-a"),
		AllowedCIDRs: []*net.IPNet{mustIPNet(t, "8.8.8.0/24")},
	}}
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{
		Pool:         testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		Upstream:     up,
		Live:         livecfg.New(livecfg.Snapshot{AuthIndex: httpauth.BuildIndex(keys)}),
		TrustedProxy: NewTrustedProxyMatcher("127.0.0.1"),
	})
	// RemoteAddr=受信代理 127.0.0.1，XFF 追加语义：客户端伪造首段 10.9.9.9，真实客户端 8.8.8.8。
	// 取首个非受信段 = 8.8.8.8 → 命中名单 → 200。
	rec := doChatFromRemote(t, h, "Bearer sk-a", "127.0.0.1:5000", "10.9.9.9, 8.8.8.8", `{"model":"m","messages":[]}`)
	if rec.Code != 200 {
		t.Fatalf("受信代理后真实 IP 8.8.8.8 应命中名单, got %d body=%s", rec.Code, rec.Body.String())
	}
	// 换真实客户端为名单外 9.9.9.9 → 403。
	rec2 := doChatFromRemote(t, h, "Bearer sk-a", "127.0.0.1:5000", "10.9.9.9, 9.9.9.9", `{"model":"m","messages":[]}`)
	if rec2.Code != 403 {
		t.Fatalf("真实 IP 9.9.9.9 名单外应 403, got %d", rec2.Code)
	}
}
