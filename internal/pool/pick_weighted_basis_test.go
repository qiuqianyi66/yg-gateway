// pickWeighted 基准一致性 + 熔断时钟口径的回归测试（2026-10-24 算法深读产出）。
package pool

import (
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestPickWeightedSharesFullSetMaxCredits：抽签的 credits 比例必须用调用方传入的
// 全集 maxCredits（与 pick 截断排序同基准）。回归场景：top5 = {a:500, b:100}，
// 全集最高分 big:1000 未进 top5——全集基准下 a:b 权重 = 6:2（3:1）；
// 若子集重算（max=500）则 11:3（3.67:1），两阶段权重不可比。
// 断言注入随机源看到的 total == (6+2)×1e6，旧实现会看到 14e6。
func TestPickWeightedSharesFullSetMaxCredits(t *testing.T) {
	p := New("")
	// 关闲置补偿：credits 项主导，权重可手算。SetWeights(0,0) 被 >0 守卫吞掉，
	// 直接置字段（同包测试）。
	p.mu.Lock()
	p.idleWeightPerHour = 0
	p.idleWeightMax = 0
	p.mu.Unlock()
	p.Add(&auth.Auth{UID: "a"})
	p.Add(&auth.Auth{UID: "b"})
	p.SetCredits("a", 500, 0)
	p.SetCredits("b", 100, 0)

	var gotTotal int64
	p.SetRandomSource(func(n int64) int64 { gotTotal = n; return 0 })

	a, b := p.byUID["a"], p.byUID["b"]
	if a == nil || b == nil {
		t.Fatal("账号未入池")
	}
	pickWeighted := p.pickWeighted([]*entry{a, b}, 1000 /* 全集 maxCredits */, 0, 0, ClassChat)
	if pickWeighted != a {
		t.Fatalf("r=0 应命中权重最高的 a")
	}
	// a = 1 + 500/1000×10 = 6；b = 1 + 100/1000×10 = 2 → total = 8×1e6。
	// 子集基准（旧 bug）：a = 11、b = 3 → 14×1e6。
	if gotTotal != 8_000_000 {
		t.Fatalf("抽签 total = %d, want 8000000（全集基准）；子集基准会是 14000000", gotTotal)
	}
}

// TestBreakerUntilUsesInjectableClock：熔断截止必须由 p.now()（注入时钟）推导，
// 与 pick 的 breakerHalfOpen 门槛（同用 p.now() 比较）同口径。
// 旧实现直接 time.Now()：冻结时钟下 breakerUntil = 墙钟+d，注入时钟永远到不了期。
func TestBreakerUntilUsesInjectableClock(t *testing.T) {
	p := New("")
	base := time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)
	p.SetClock(func() time.Time { return base })
	p.breakerThreshold = 1
	p.breakerCooldown = time.Hour
	p.breakerCooldownMax = 6 * time.Hour

	p.Add(&auth.Auth{UID: "x"})
	e := p.byUID["x"]
	if e == nil {
		t.Fatal("账号未入池")
	}

	p.mu.Lock()
	p.recordBreakerFailureLocked(e)
	p.mu.Unlock()

	want := base.Add(time.Hour)
	if !e.breakerUntil.Equal(want) {
		t.Fatalf("breakerUntil = %v, want %v（注入时钟基准）", e.breakerUntil, want)
	}
}
