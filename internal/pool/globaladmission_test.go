package pool

import (
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// 全局准入控制测试（ADR-003 D-401）。

// TestGlobalAdmissionDisabledByDefault：未配置上限（0）时恒放行，零回归。
func TestGlobalAdmissionDisabledByDefault(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	// 默认 globalMaxInFlight = 0
	for i := 0; i < 100; i++ {
		if !p.AcquireGlobal() {
			t.Fatalf("未配置上限时第 %d 次应恒放行（零回归）", i+1)
		}
	}
	if got := p.GlobalInFlight(); got != 0 {
		t.Fatalf("未配置上限时不应计数，got %d", got)
	}
}

// TestGlobalAdmissionLimit：配置上限后，超限拒绝。
//
// 牙齿：把 AcquireGlobal 的 CAS 环改成「先读后写」→ 并发下会超发 →
// TestGlobalAdmissionConcurrent 会 FAIL。
func TestGlobalAdmissionLimit(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetGlobalMaxInFlight(3)

	for i := 0; i < 3; i++ {
		if !p.AcquireGlobal() {
			t.Fatalf("第 %d 个名额应被占用（上限 3）", i+1)
		}
	}
	if p.AcquireGlobal() {
		t.Fatal("第 4 个请求应被拒绝（超上限）")
	}
	if got := p.GlobalInFlight(); got != 3 {
		t.Fatalf("在途数应为 3，got %d", got)
	}

	// 释放一个 → 应能再进一个
	p.ReleaseGlobal()
	if !p.AcquireGlobal() {
		t.Fatal("释放一个名额后应能再占用")
	}
	if got := p.GlobalInFlight(); got != 3 {
		t.Fatalf("释放+占用后应为 3，got %d", got)
	}
}

// TestGlobalAdmissionConcurrent：并发下不得超发。
//
// 这是 CAS 的牙齿：并发 100 个 goroutine 抢 10 个名额，成功数必须恰好 10。
func TestGlobalAdmissionConcurrent(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetGlobalMaxInFlight(10)

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.AcquireGlobal() {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 10 {
		t.Fatalf("并发抢 10 个名额应恰好放行 10 个，got %d（CAS 超发？）", granted)
	}
	if got := p.GlobalInFlight(); got != 10 {
		t.Fatalf("在途数应为 10，got %d", got)
	}
}

// TestGlobalAdmissionReleasePairing：严格配对（无泄漏、无下溢）。
//
// 这是 D-401 的关键正确性：计数器只增不减即为泄漏（最终永远 429）。
func TestGlobalAdmissionReleasePairing(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetGlobalMaxInFlight(5)

	// 反复占用/释放，计数必须回到 0。
	for i := 0; i < 50; i++ {
		if !p.AcquireGlobal() {
			t.Fatalf("第 %d 轮应能占用", i+1)
		}
		p.ReleaseGlobal()
	}
	if got := p.GlobalInFlight(); got != 0 {
		t.Fatalf("反复占用/释放后计数应归零（泄漏？），got %d", got)
	}
}

// TestGlobalAdmissionReleaseUnderflowGuard：异常重复 Release 不得把计数打成负数。
func TestGlobalAdmissionReleaseUnderflowGuard(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetGlobalMaxInFlight(5)

	p.ReleaseGlobal() // 未占用就释放
	if got := p.GlobalInFlight(); got < 0 {
		t.Fatalf("重复 Release 不应下溢成负，got %d", got)
	}
	if got := p.GlobalInFlight(); got != 0 {
		t.Fatalf("下溢保护应钳到 0，got %d", got)
	}
}

// TestGlobalAdmissionConcurrentPairing：并发占用+释放配对，-race 下验证无泄漏。
func TestGlobalAdmissionConcurrentPairing(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetGlobalMaxInFlight(100)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.AcquireGlobal() {
				time.Sleep(time.Millisecond) // 模拟请求处理
				p.ReleaseGlobal()
			}
		}()
	}
	wg.Wait()
	if got := p.GlobalInFlight(); got != 0 {
		t.Fatalf("并发配对后应归零（泄漏？），got %d", got)
	}
}

// TestSetGlobalMaxInFlightNegative：负值归一为 0（不限），不 panic。
func TestSetGlobalMaxInFlightNegative(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetGlobalMaxInFlight(-5)
	if !p.AcquireGlobal() {
		t.Fatal("负值应归一为 0（不限），恒放行")
	}
}
