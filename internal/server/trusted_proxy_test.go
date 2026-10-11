// trusted_proxy_test.go 受信任代理判定与安全 IP 提取（F-002）的牙齿测试。
package server

import (
	"net/http/httptest"
	"testing"
)

// TestTrustedProxyMatcherParse 解析：精确 IP 与 CIDR 混合，非法条目忽略。
func TestTrustedProxyMatcherParse(t *testing.T) {
	m := NewTrustedProxyMatcher("127.0.0.1,10.0.0.0/8,::1,not-an-ip,192.168.1.0/24")
	if m == nil {
		t.Fatal("expected non-nil matcher for valid spec")
	}
	for _, tc := range []struct {
		src  string
		want bool
	}{
		{"127.0.0.1:443", true},
		{"127.0.0.1", true},
		{"10.1.2.3", true},       // 10.0.0.0/8
		{"192.168.1.99", true},   // 192.168.1.0/24
		{"::1", true},            // 精确 IPv6
		{"203.0.113.7", false},   // 公网，未命中
		{"192.168.2.1", false},   // 不在 192.168.1.0/24
		{"", false},
		{"garbage", false},
	} {
		if got := m.Match(tc.src); got != tc.want {
			t.Errorf("Match(%q) = %v want %v", tc.src, got, tc.want)
		}
	}
}

// TestTrustedProxyMatcherEmptyOrInvalid 空/全非法 spec → nil（安全默认：不信任任何代理头）。
func TestTrustedProxyMatcherEmptyOrInvalid(t *testing.T) {
	for _, spec := range []string{"", "   ", "not-an-ip", "nope,nope2"} {
		if m := NewTrustedProxyMatcher(spec); m != nil {
			t.Errorf("spec %q want nil matcher, got %v", spec, m)
		}
	}
}

// TestExtractClientIPNoTrustedProxy 未配置受信任代理：伪造 XFF 必须被忽略，回落 RemoteAddr。
// 这是 F-002 的核心牙齿——还原「无条件信任 XFF」的 bug 时，此用例必须 FAIL。
func TestExtractClientIPNoTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "198.51.100.9:54321"
	req.Header.Set("X-Forwarded-For", "169.254.169.254") // 伪造元数据地址
	req.Header.Set("X-Real-IP", "10.0.0.1")

	if got := extractClientIP(nil, req); got != "198.51.100.9" {
		t.Fatalf("extractClientIP(nil) = %q want 198.51.100.9 (伪造 XFF 不得被采信)", got)
	}
}

// TestExtractClientIPTrustedProxy 命中受信任代理时，从右往左取第一个非受信任 IP。
func TestExtractClientIPTrustedProxy(t *testing.T) {
	m := NewTrustedProxyMatcher("10.0.0.0/8")
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:443" // 来源是受信任反代
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")

	if got := extractClientIP(m, req); got != "203.0.113.7" {
		t.Fatalf("extractClientIP(trusted) = %q want 203.0.113.7（受信任反代后取真实客户端）", got)
	}
}

// TestExtractClientIPAppendSemantics 追加语义防绕过（F-002 核心牙齿）：
// 受信任代理用 $proxy_add_x_forwarded_for 追加时，首段仍是客户端伪造值，
// 但真实客户端是「从右往左第一个非受信任 IP」。还原「取首段」的 bug 必须 FAIL。
func TestExtractClientIPAppendSemantics(t *testing.T) {
	m := NewTrustedProxyMatcher("10.0.0.0/8")
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:443" // 来源是受信任反代
	// 追加语义：客户端伪造 169.254.169.254 在首段，反代追加真实对端 198.51.100.9 在尾段
	req.Header.Set("X-Forwarded-For", "169.254.169.254, 198.51.100.9")

	if got := extractClientIP(m, req); got != "198.51.100.9" {
		t.Fatalf("extractClientIP(append) = %q want 198.51.100.9（从右往左取第一个非受信任 IP，首段伪造不得采信）", got)
	}
}

// TestExtractClientIPXRealIPFallback 无 XFF 时回落 X-Real-IP（单值，无追加语义问题）。
func TestExtractClientIPXRealIPFallback(t *testing.T) {
	m := NewTrustedProxyMatcher("10.0.0.0/8")
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:443"
	req.Header.Set("X-Real-IP", "198.51.100.9")

	if got := extractClientIP(m, req); got != "198.51.100.9" {
		t.Fatalf("extractClientIP(x-real-ip) = %q want 198.51.100.9", got)
	}
}

// TestExtractClientIPUntrustedSource 来源不是受信任代理（伪造来源）：即便有 XFF 也不采信。
func TestExtractClientIPUntrustedSource(t *testing.T) {
	m := NewTrustedProxyMatcher("10.0.0.0/8")
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "203.0.113.99:1234" // 公网直连，不在受信任集合
	req.Header.Set("X-Forwarded-For", "169.254.169.254")

	if got := extractClientIP(m, req); got != "203.0.113.99" {
		t.Fatalf("extractClientIP(untrusted src) = %q want 203.0.113.99（非受信任来源不采信 XFF）", got)
	}
}

// TestExtractClientIPNilRequest 防御：nil 请求返回空。
func TestExtractClientIPNilRequest(t *testing.T) {
	if got := extractClientIP(nil, nil); got != "" {
		t.Fatalf("extractClientIP(nil,nil) = %q want empty", got)
	}
}
