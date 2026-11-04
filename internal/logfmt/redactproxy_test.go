package logfmt

import (
	"strings"
	"testing"
)

// TestRedactProxyURL 钉住 R1/R2 红线：代理 URL 的 userinfo 绝不出现在任何可见面
// （日志 / /status / 面板）。本函数是**单一实现**（auth.RedactedProxyURL /
// pool.Status.ProxyURL / upstream 日志共用），upstream 侧另有薄包装回归。
//
// 牙齿：把实现换成"原样返回"，前两条立刻红（会看到 alice/s3cr3t）。
func TestRedactProxyURL(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		mustContain string
		mustNotHave []string
	}{
		{
			name:        "http 带账号密码",
			raw:         "http://alice:s3cr3t@proxy:8080",
			mustContain: "proxy:8080",
			mustNotHave: []string{"alice", "s3cr3t"},
		},
		{
			name:        "socks5 带账号密码",
			raw:         "socks5://bob:hunter2@127.0.0.1:1080",
			mustContain: "127.0.0.1:1080",
			mustNotHave: []string{"bob", "hunter2"},
		},
		{
			name:        "无 userinfo 原样保留",
			raw:         "http://proxy:8080",
			mustContain: "http://proxy:8080",
		},
		{
			name:        "解析失败且含 @ → 整体隐去",
			raw:         "http://carol:pa55w0rd@[::1",
			mustContain: "已隐去",
			mustNotHave: []string{"carol", "pa55w0rd"},
		},
		{
			name:        "解析失败且无 @ → 原样返回（无凭据可泄露）",
			raw:         "::::",
			mustContain: "::::",
		},
		{
			name:        "空串原样返回（直连形态，无凭据）",
			raw:         "",
			mustContain: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactProxyURL(c.raw)
			if c.mustContain != "" && !strings.Contains(got, c.mustContain) {
				t.Fatalf("RedactProxyURL(%q) = %q，应含 %q", c.raw, got, c.mustContain)
			}
			for _, bad := range c.mustNotHave {
				if strings.Contains(got, bad) {
					t.Fatalf("RedactProxyURL(%q) = %q，泄露了凭据片段 %q", c.raw, got, bad)
				}
			}
		})
	}
}
