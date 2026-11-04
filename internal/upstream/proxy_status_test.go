package upstream

import (
	"strings"
	"testing"

	"yg-gateway/internal/auth"
)

// TestProxyStatusSnapshot 验证 /status 代理三态观测的状态表（P0-2）：
// 构造 clientFor/chatClientFor 时登记 proxyEffective，快照输出脱敏 URL →
// active / failed；直连账号不出现。
//
// 牙齿：把 clientFor 里的登记行删掉，断言立即红（快照为空）；把
// newTransportWithProxy 失败分支的 proxyFailed 误写成 proxyActive，failed 断言红。
func TestProxyStatusSnapshot(t *testing.T) {
	c := &Client{}

	// 1) 坏代理 URL → failed（fail-closed，出站必失败）
	c.clientFor(&auth.Auth{ProxyURL: "://missing-scheme"})
	// 2) 构造成功的 socks5 代理 → active（只构造 Dialer，不实际连接）
	c.clientFor(&auth.Auth{ProxyURL: "socks5://127.0.0.1:1080"})
	// 3) 直连账号 → 不登记（快照不出现）
	c.clientFor(&auth.Auth{})

	got := c.ProxyStatusSnapshot()

	want := map[string]string{
		"://missing-scheme":       "failed",
		"socks5://127.0.0.1:1080": "active",
	}
	if len(got) != len(want) {
		t.Fatalf("ProxyStatusSnapshot() 长度 = %d，want %d（got=%v）", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("ProxyStatusSnapshot()[%q] = %q，want %q（got=%v）", k, got[k], v, got)
		}
	}
}

// TestProxyStatusSnapshotRedacts 验证快照输出已脱敏（R1/R2 红线）：
// 含 userinfo 的代理 URL 登记后，快照里不得出现凭据片段。
func TestProxyStatusSnapshotRedacts(t *testing.T) {
	c := &Client{}
	c.clientFor(&auth.Auth{ProxyURL: "http://alice:s3cr3t@proxy:8080"})

	got := c.ProxyStatusSnapshot()
	if len(got) != 1 {
		t.Fatalf("ProxyStatusSnapshot() = %v，want 1 条", got)
	}
	for k := range got {
		for _, bad := range []string{"alice", "s3cr3t"} {
			if strings.Contains(k, bad) {
				t.Fatalf("快照 key %q 泄露了代理凭据（R1/R2 红线）", k)
			}
		}
	}
}

// TestProxyStatusChatClientShared 验证 chatClientFor 与 clientFor 共用同一状态表：
// 聊天路径构造的代理同样登记（同一 proxyURL 状态一致）。
func TestProxyStatusChatClientShared(t *testing.T) {
	c := &Client{}
	c.chatClientFor(&auth.Auth{ProxyURL: "socks5://127.0.0.1:1080"})

	got := c.ProxyStatusSnapshot()
	if got["socks5://127.0.0.1:1080"] != "active" {
		t.Fatalf("chat 路径未登记代理状态：%v", got)
	}
}
