// pickreason_test.go 决策溯源测试：四条选号路径各自落正确的理由。
// 亲历依据：词表口径 bug（hard vs hard_credit 规则永假）由投影测试抓到——
// 决策理由同样必须有牙齿，不能「写了但没人验」。
package pool

import (
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestPickReasonWeighted 正常加权路径：weighted + tier（无模型时 tier=1 未知层）。
func TestPickReasonWeighted(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 100, 0)
	if got := p.Pick(); got == nil || got.UID != "u1" {
		t.Fatalf("pick = %+v", got)
	}
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("Status 缺失")
	}
	if !strings.HasPrefix(st.PickReason, "weighted tier=") {
		t.Fatalf("PickReason = %q, want weighted tier=… 前缀", st.PickReason)
	}
}

// TestPickReasonSticky 粘性命中路径。
func TestPickReasonSticky(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	if got := p.PickByUID("u1"); got == nil {
		t.Fatal("粘性未命中")
	}
	st, _ := p.Status("u1")
	if st.PickReason != "sticky" {
		t.Fatalf("PickReason = %q, want sticky", st.PickReason)
	}
}

// TestPickReasonEarliestExpiry 全冷却兜底路径：mode + 粗口径 kind。
func TestPickReasonEarliestExpiry(t *testing.T) {
	p := New("")
	base := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	p.SetClock(func() time.Time { return base })
	p.Add(&auth.Auth{UID: "uc"})
	p.Cooldown("uc", CoolSoft, time.Hour, "test")
	if got := p.Pick(); got == nil || got.UID != "uc" {
		t.Fatalf("全冷却兜底 = %+v, want uc", got)
	}
	st, _ := p.Status("uc")
	if st.PickReason != "earliest-expiry kind=soft" {
		t.Fatalf("PickReason = %q, want earliest-expiry kind=soft", st.PickReason)
	}
}

// TestPickReasonNilEmpty 从未选中的账号：Status.PickReason 为空（不 panic、不编造）。
func TestPickReasonNilEmpty(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "never"})
	st, _ := p.Status("never")
	if st.PickReason != "" {
		t.Fatalf("从未选中应空串，got %q", st.PickReason)
	}
}
