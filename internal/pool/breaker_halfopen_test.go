package pool

import (
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// half-open 断路器测试（ADR-003 D-400）。
//
// 场景设定：让一个号熔断，再把 breakerUntil 拨到过去（模拟"熔断刚到期"），
// 然后连续 pick，观察是否被限速。

// newBreakerProbePool 构造一个「熔断刚到期」的池：u1 已被熔断且 retryCount=1，
// breakerUntil 已过期。返回池与 entry 供断言。
func newBreakerProbePool(t *testing.T, probeGap time.Duration) (*Pool, *entry) {
	t.Helper()
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetBreaker(1, time.Hour, 6*time.Hour) // 阈值 1：一次错误即熔断
	p.SetBreakerProbeGap(probeGap)
	p.NoteError("u1") // 熔断：retryCount=1, breakerUntil=now+1h

	p.mu.Lock()
	e := p.byUID["u1"]
	e.breakerUntil = time.Now().Add(-time.Minute) // 拨到过去 = 熔断刚到期
	p.mu.Unlock()
	return p, e
}

// TestBreakerHalfOpenRateLimits：熔断到期后，half-open 窗口内限速放行。
//
// 牙齿：把 breakerHalfOpen 的门槛去掉（恒 false）→ 第二次 pick 也能选中 →
// 本测试 FAIL（这正是"到期瞬间洪峰"的复现）。
func TestBreakerHalfOpenRateLimits(t *testing.T) {
	p, _ := newBreakerProbePool(t, time.Minute)

	// 第一次 pick：首次到期（breakerProbeAt 为零）→ 放行试探。
	got := p.PickExcluding(nil)
	if got == nil || got.UID != "u1" {
		t.Fatalf("首次到期应放行一次试探，got %+v", got)
	}

	// 第二次 pick：距上次试探 < gap → 应被限速跳过。
	got2 := p.PickExcluding(nil)
	if got2 != nil && got2.UID == "u1" {
		t.Fatal("half-open 窗口内第二次 pick 不应再选中该号（限速失效）")
	}
	if got2 != nil {
		t.Fatalf("池中只有 u1 且它被限速，应返回 nil（无可用号），got %+v", got2)
	}
}

// TestBreakerHalfOpenAllowsAfterGap：跨过 gap 后可再次试探。
func TestBreakerHalfOpenAllowsAfterGap(t *testing.T) {
	p, e := newBreakerProbePool(t, time.Minute)

	if got := p.PickExcluding(nil); got == nil {
		t.Fatal("首次到期应放行试探")
	}
	// 把上次试探时刻拨到 gap 之前 → 本轮的限速判定应放行。
	p.mu.Lock()
	e.setProbeAt(time.Now().Add(-2 * time.Minute))
	p.mu.Unlock()

	if got := p.PickExcluding(nil); got == nil || got.UID != "u1" {
		t.Fatalf("跨过 gap 后应可再次试探，got %+v", got)
	}
}

// TestBreakerSuccessRestoresFully：试探成功（NoteSuccess）后全量恢复，不再限速。
func TestBreakerSuccessRestoresFully(t *testing.T) {
	p, _ := newBreakerProbePool(t, time.Hour)

	if got := p.PickExcluding(nil); got == nil {
		t.Fatal("首次到期应放行试探")
	}
	// 试探成功：NoteSuccess 清 retryCount/breakerUntil/breakerProbeAt。
	p.NoteSuccess("u1")

	// 之后连续 pick 都应正常选中（不再受 half-open 限速）。
	for i := 0; i < 3; i++ {
		p.mu.Lock()
		if e := p.byUID["u1"]; e != nil {
			e.clearLastUsed() // 清掉防撞号窗口，专测 half-open 是否还在限速
		}
		p.mu.Unlock()
		if got := p.PickExcluding(nil); got == nil || got.UID != "u1" {
			t.Fatalf("恢复后第 %d 次 pick 应正常选中，got %+v", i+1, got)
		}
	}
}

// TestBreakerHalfOpenDisabledRegression：probeGap=0（关闭）时行为与旧版一致——
// 熔断到期即全量放行，不设试探限速。
//
// 这是 D-400「默认零回归」的守门测试：配置 0 必须逐位等同改动前。
func TestBreakerHalfOpenDisabledRegression(t *testing.T) {
	p, _ := newBreakerProbePool(t, 0) // 关闭 half-open

	// 连续 pick 都应能选中（旧语义：到期即恢复）。
	for i := 0; i < 3; i++ {
		p.mu.Lock()
		if e := p.byUID["u1"]; e != nil {
			e.clearLastUsed() // 只测 half-open 维度，排除防撞号干扰
		}
		p.mu.Unlock()
		if got := p.PickExcluding(nil); got == nil || got.UID != "u1" {
			t.Fatalf("probeGap=0 时第 %d 次 pick 应放行（零回归），got %+v", i+1, got)
		}
	}
}

// TestBreakerHalfOpenSkipsNeverBroken：从未熔断过的号不受 half-open 影响。
func TestBreakerHalfOpenSkipsNeverBroken(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetBreakerProbeGap(time.Hour)

	for i := 0; i < 3; i++ {
		p.mu.Lock()
		if e := p.byUID["u1"]; e != nil {
			e.clearLastUsed()
		}
		p.mu.Unlock()
		if got := p.PickExcluding(nil); got == nil || got.UID != "u1" {
			t.Fatalf("未熔断的号不应被 half-open 限速（第 %d 次），got %+v", i+1, got)
		}
	}
}

// TestBreakerProbeAtSetOnSelection：被放行的试探必须记下时刻，否则限速形同虚设。
func TestBreakerProbeAtSetOnSelection(t *testing.T) {
	p, e := newBreakerProbePool(t, time.Minute)
	if got := p.PickExcluding(nil); got == nil {
		t.Fatal("首次到期应放行试探")
	}
	p.mu.RLock()
	probeAt := e.probeAtTime()
	p.mu.RUnlock()
	if probeAt.IsZero() {
		t.Fatal("试探被放行后必须记录 breakerProbeAt（否则限速失效）")
	}
}

// TestBreakerProbeAtClearedOnSuccess：恢复后试探记录清零（不留残余状态）。
func TestBreakerProbeAtClearedOnSuccess(t *testing.T) {
	p, e := newBreakerProbePool(t, time.Minute)
	if got := p.PickExcluding(nil); got == nil {
		t.Fatal("首次到期应放行试探")
	}
	p.NoteSuccess("u1")
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !e.probeAtTime().IsZero() {
		t.Error("NoteSuccess 应清 breakerProbeAt（恢复后不再限速）")
	}
}
