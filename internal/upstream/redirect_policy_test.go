package upstream

// CheckRedirect 纵深防御回归（四维度审查 [P3] SSRF）。
// 生产契约：上游 base 是配置固定的受信端点 → 重定向只允许同 host 内发生；
// 跨 host 一律拒绝并回抛 3xx 响应（http.ErrUseLastResponse），不静默跟随。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestUpstreamClientsHaveExplicitRedirectPolicy：New() 的两个 client 都必须带
// 显式重定向策略——P3 的要点是「不依赖 net/http 默认行为」，nil 即回退默认。
func TestUpstreamClientsHaveExplicitRedirectPolicy(t *testing.T) {
	c := New()
	if c.HTTP.CheckRedirect == nil {
		t.Error("HTTP 必须显式设置 CheckRedirect（纵深防御，不依赖默认值）")
	}
	if c.ChatHTTP.CheckRedirect == nil {
		t.Error("ChatHTTP 必须显式设置 CheckRedirect（SSE 长连接同样不能静默跨 host）")
	}
}

// TestSameHostRedirectPolicy：策略判定表。同 host（含 http→https 升级、大小写、
// 显式缺省端口）放行；跨 host、换端口、相似域名后缀一律拒绝。
func TestSameHostRedirectPolicy(t *testing.T) {
	orig, err := url.Parse("https://copilot.tencent.com/v1/chat")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		to   string
		want error // nil = 放行
	}{
		{"同 host 路径跳转", "https://copilot.tencent.com/v2/chat", nil},
		{"同 host http→https 升级", "https://copilot.tencent.com/x", nil},
		{"hostname 大小写不同", "https://COPILOT.TENCENT.COM/x", nil},
		{"显式缺省端口 443", "https://copilot.tencent.com:443/x", nil},
		{"跨 host", "https://evil.example.com/x", http.ErrUseLastResponse},
		{"端口变化（换服务）", "https://copilot.tencent.com:8443/x", http.ErrUseLastResponse},
		{"相似域名后缀", "https://copilot.tencent.com.evil.com/x", http.ErrUseLastResponse},
	}
	policy := New().HTTP.CheckRedirect
	if policy == nil {
		t.Fatal("CheckRedirect 为 nil，策略未接线")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			to, err := url.Parse(tc.to)
			if err != nil {
				t.Fatal(err)
			}
			got := policy(&http.Request{URL: to}, []*http.Request{{URL: orig}})
			if got != tc.want {
				t.Fatalf("policy(%s) = %v, want %v", tc.to, got, tc.want)
			}
		})
	}
}

// TestRedirectPolicyEndToEndSameHostFollowed：同 host 302 仍被正常跟随
// （策略收紧不得误伤合法的站内跳转）。用真实 client + 真实 HTTP 服务器。
func TestRedirectPolicyEndToEndSameHostFollowed(t *testing.T) {
	targetHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/target", http.StatusFound)
		case "/target":
			targetHit = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New()
	resp, err := c.HTTP.Get(srv.URL + "/start")
	if err != nil {
		t.Fatalf("同 host 重定向请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200（重定向应被跟随到 /target）", resp.StatusCode)
	}
	if !targetHit {
		t.Fatal("同 host 重定向未被跟随（策略过紧，误伤站内跳转）")
	}
}

// TestRedirectPolicyEndToEndCrossHostRejected：跨 host 302 不得被跟随——
// 第二个 host 一次都不该被请求；调用方拿到 302 响应本体（ErrUseLastResponse 语义）。
// 两个 httptest 服务器端口不同 = 跨 host（这正是策略的端口比较要覆盖的情形）。
func TestRedirectPolicyEndToEndCrossHostRejected(t *testing.T) {
	var secondHostHits int
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHostHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, second.URL+"/target", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer first.Close()

	c := New()
	resp, err := c.HTTP.Get(first.URL + "/start")
	if err != nil {
		t.Fatalf("跨 host 重定向应回抛响应而非报错: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d want 302（应拿到 3xx 本体）", resp.StatusCode)
	}
	if secondHostHits != 0 {
		t.Fatalf("跨 host 重定向被跟随了：第二个 host 被请求 %d 次", secondHostHits)
	}
}
