// pace_test.go F9 pace 选号模式测试：因子数学（单调/封顶/回退）、池级中位路由、
// 单 expiring 回退、长期分布。默认关 = 零回归由既有 expiring 测试（×2 常量断言）
// 在传 medianPace=0 下继续守护。
package pool

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestPaceFactorMath 因子公式边界：中位=×1.5、半中位=×1、2×以上封顶 ×2、<1h 钳位、
// 无基准回退常量。
func TestPaceFactorMath(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	median := 100.0 // 基准：100 额度/小时
	mk := func(remaining int64, hours float64) *entry {
		return &entry{creditsEarliestRemaining: remaining, creditsEarliestExpiry: now.Add(time.Duration(hours * float64(time.Hour)))}
	}
	cases := []struct {
		name  string
		e     *entry
		wantF float64
	}{
		{"median → 1.5", mk(100, 1), 1.5},
		{"half median → 1.0", mk(50, 1), 1.0},
		{"below half → 1.0", mk(10, 1), 1.0},
		{"2x median → cap 2.0", mk(200, 1), 2.0},
		{"5x median → cap 2.0", mk(500, 1), 2.0},
	}
	for _, c := range cases {
		if f := paceFactorOf(c.e, now, median); math.Abs(f-c.wantF) > 1e-9 {
			t.Errorf("%s: factor=%v want %v", c.name, f, c.wantF)
		}
	}
	// 单调：remaining 升序 → 因子不降。
	prev := 0.0
	for _, rem := range []int64{10, 50, 100, 150, 200} {
		f := paceFactorOf(mk(rem, 1), now, median)
		if f < prev {
			t.Fatalf("non-monotonic: rem=%d factor=%v < prev=%v", rem, f, prev)
		}
		prev = f
	}
	// <1h 按 1h 计（防除零）：距到期 10 分钟与 1 小时 pace 相同。
	if a, b := paceOf(mk(100, 1.0/6), now), paceOf(mk(100, 1), now); a != b {
		t.Errorf("sub-hour clamp failed: %v vs %v", a, b)
	}
	// 无池级基准 → 回退常量。
	if f := paceFactorOf(mk(100, 1), now, 0); f != expiringVirtualSlots {
		t.Errorf("no-median fallback=%v want %v", f, expiringVirtualSlots)
	}
}

// TestPaceModeWeightsAndCap pace 开/关的权重行为：关=×2 常量（零回归）；开=高 pace
// 号权重大于低 pace 号、封顶 ×2、保留对普通号的偏好。
func TestPaceModeWeightsAndCap(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "fast"})
	p.Add(&auth.Auth{UID: "slow"})
	p.Add(&auth.Auth{UID: "none"})
	now := time.Now()
	p.SetCreditsDetailed("fast", 100, 100, 100, now.Add(time.Hour), 100)     // pace 100/h
	p.SetCreditsDetailed("slow", 100, 100, 100, now.Add(100*time.Hour), 100) // pace 1/h
	p.SetCreditsDetailed("none", 100, 100, 0, time.Time{}, 0)

	p.mu.Lock()
	wf0 := p.routingWeightOf(p.byUID["fast"], 100, now, 0, 0)
	ws0 := p.routingWeightOf(p.byUID["slow"], 100, now, 0, 0)
	wn0 := p.routingWeightOf(p.byUID["none"], 100, now, 0, 0)
	p.mu.Unlock()
	if wf0 != wn0*expiringVirtualSlots || ws0 != wn0*expiringVirtualSlots {
		t.Fatalf("pace off must keep ×2 constant: fast=%v slow=%v none=%v", wf0, ws0, wn0)
	}

	p.SetPaceMode(true)
	p.mu.Lock()
	medianPace := p.poolMedianPace([]*entry{p.byUID["fast"], p.byUID["slow"]}, now)
	wf := p.routingWeightOf(p.byUID["fast"], 100, now, 0, medianPace)
	ws := p.routingWeightOf(p.byUID["slow"], 100, now, 0, medianPace)
	wn := p.routingWeightOf(p.byUID["none"], 100, now, 0, medianPace)
	p.mu.Unlock()
	if medianPace <= 0 {
		t.Fatalf("medianPace=%v want >0", medianPace)
	}
	if wf <= ws {
		t.Fatalf("high-pace must outweigh low-pace: fast=%v slow=%v", wf, ws)
	}
	if wf > wn*2+1e-9 {
		t.Fatalf("cap violated: fast=%v none=%v", wf, wn)
	}
	if wf <= wn {
		t.Fatalf("high-pace lost preference over none: fast=%v none=%v", wf, wn)
	}
}

// TestPaceModeSingleExpiringFallback 单 expiring 候选（池级基准缺失）→ 回退 ×2 常量。
func TestPaceModeSingleExpiringFallback(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "a"})
	p.Add(&auth.Auth{UID: "b"})
	now := time.Now()
	p.SetCreditsDetailed("a", 100, 100, 100, now.Add(time.Hour), 100)
	p.SetCreditsDetailed("b", 100, 100, 0, time.Time{}, 0)
	p.SetPaceMode(true)
	p.mu.Lock()
	mp := p.poolMedianPace([]*entry{p.byUID["a"], p.byUID["b"]}, now)
	w := p.routingWeightOf(p.byUID["a"], 100, now, 0, mp)
	wn := p.routingWeightOf(p.byUID["b"], 100, now, 0, mp)
	p.mu.Unlock()
	if mp != 0 {
		t.Fatalf("single expiring: medianPace=%v want 0", mp)
	}
	if w != wn*expiringVirtualSlots {
		t.Fatalf("fallback factor: a=%v want %v×常量", w, wn)
	}
}

// TestPaceModePickDistribution 长期分布：高 pace 号占多数，普通号保留少量流量
// （与既有 expiring 虚拟实例分布测试同法）。
func TestPaceModePickDistribution(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "fast"})
	p.Add(&auth.Auth{UID: "slow"})
	p.Add(&auth.Auth{UID: "none"})
	now := time.Now()
	p.SetCreditsDetailed("fast", 100, 100, 100, now.Add(time.Hour), 100)
	p.SetCreditsDetailed("slow", 100, 100, 100, now.Add(100*time.Hour), 100)
	p.SetCreditsDetailed("none", 100, 100, 0, time.Time{}, 0)
	p.SetPaceMode(true)
	rng := rand.New(rand.NewPCG(7, 8))
	p.SetRandomSource(func(n int64) int64 { return rng.Int64N(n) })
	counts := map[string]int{}
	for i := 0; i < 2000; i++ {
		got := p.Pick()
		if got == nil {
			t.Fatal("pick returned nil")
		}
		counts[got.UID]++
	}
	if counts["fast"] <= counts["slow"] {
		t.Fatalf("high-pace must dominate: %v", counts)
	}
	if counts["none"] == 0 {
		t.Fatalf("regular account starved: %v", counts)
	}
}
