package httpauth

import (
	"net"
	"testing"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return n
}

func TestKeyIPAllowed_EmptyMeansUnrestricted(t *testing.T) {
	// 四态：省略/nil = 不限，不是「未配置即拒绝」。任意来源都放行。
	k := &Key{ID: "k1"}
	for _, src := range []string{"1.2.3.4:5678", "8.8.8.8", "2001:db8::1", "", "not-an-ip"} {
		if !k.IPAllowed(src) {
			t.Fatalf("空白名单应恒放行，src=%q 却被拒", src)
		}
	}
}

func TestKeyIPAllowed_CIDRWhitelist(t *testing.T) {
	k := &Key{
		ID: "k2",
		AllowedCIDRs: []*net.IPNet{
			mustCIDR(t, "10.0.0.0/8"),
			mustCIDR(t, "192.168.1.0/24"),
		},
	}
	cases := []struct {
		src  string
		want bool
	}{
		{"10.1.2.3:8080", true}, // 命中 /8（带端口）
		{"192.168.1.50", true},  // 命中 /24
		{"192.168.2.50", false}, // /24 之外
		{"8.8.8.8", false},      // 完全不在名单
		{"not-an-ip", false},    // 解析失败 fail-closed
	}
	for _, c := range cases {
		if got := k.IPAllowed(c.src); got != c.want {
			t.Fatalf("IPAllowed(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestKeyIPAllowed_BareHostIP(t *testing.T) {
	// 裸 IP（无 CIDR 后缀）收进名单 = 单主机白名单。
	_, n, _ := net.ParseCIDR("1.2.3.4/32")
	k := &Key{ID: "k3", AllowedCIDRs: []*net.IPNet{n}}
	if !k.IPAllowed("1.2.3.4:443") {
		t.Fatal("命中单主机 IP 应放行")
	}
	if k.IPAllowed("1.2.3.5:443") {
		t.Fatal("相邻主机应被拒")
	}
}

func TestKeyIPAllowed_NilEntrySkipped(t *testing.T) {
	// 名单里混入 nil 条目应跳过而非 panic。
	k := &Key{ID: "k4", AllowedCIDRs: []*net.IPNet{nil, mustCIDR(t, "10.0.0.0/8")}}
	if !k.IPAllowed("10.9.9.9") {
		t.Fatal("有效 CIDR 应命中放行")
	}
	if k.IPAllowed("11.0.0.1") {
		t.Fatal("名单外应拒")
	}
}
