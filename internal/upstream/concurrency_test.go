package upstream

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestConcurrencyLimiterFIFO：max=2 时第 3 个排队；release 后按 FIFO 转交槽位
// （inflight 保持 ≤ max 不泄漏）；全部释放归 0。
// 牙齿：把 acquire 的 "inflight < max 立即通过" 改成无条件直放 → 第 3 个 acquire
// 立即返回（done 提前收到），本测试第一条 select 红。
func TestConcurrencyLimiterFIFO(t *testing.T) {
	l := newConcurrencyLimiter(2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := l.acquire(ctx); err != nil {
			t.Fatalf("第 %d 个应通过: %v", i+1, err)
		}
	}
	// 第 3 个排队（不能立即返回）
	done := make(chan error, 1)
	go func() { done <- l.acquire(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("满时不应立即通过: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// 释放一个 → 排队者拿到（FIFO 转交，inflight 保持 2）
	l.release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("release 后应放行: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("release 后排队者未放行")
	}
	if inf, q := l.snapshot(); inf != 2 || q != 0 {
		t.Fatalf("转交后 in_flight=%d queue=%d, want 2/0", inf, q)
	}
	// 全部释放归 0
	l.release()
	l.release()
	if inf, q := l.snapshot(); inf != 0 || q != 0 {
		t.Fatalf("全部释放后 in_flight=%d queue=%d, want 0/0", inf, q)
	}
}

// TestConcurrencyLimiterContextCancel：排队中 ctx 取消 → 返回 ctx.Err()，不悬挂；
// 取消者不泄漏槽位（取消后被转交/释放的槽位仍可用）。
// 牙齿：acquire 取消分支若不移除等待者 → 取消后 release 给已取消者，下一个 acquire
// 悬挂（最后 select 超时红）。
func TestConcurrencyLimiterContextCancel(t *testing.T) {
	l := newConcurrencyLimiter(1)
	ctx := context.Background()
	if err := l.acquire(ctx); err != nil {
		t.Fatalf("首个应通过: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- l.acquire(cctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应 ctx.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx 取消后仍悬挂")
	}
	// 取消者已从队列移除：释放后下一个 acquire 立即通过（无泄漏）
	l.release()
	if err := l.acquire(ctx); err != nil {
		t.Fatalf("取消者不应占用槽位: %v", err)
	}
	l.release()
}

// TestConcurrencyRoundTripperBuckets：按 X-Machine-ID 分桶——账号 a 在途打满时，
// 账号 b 不被阻塞；无头请求归 global 桶；ConcurrencyPerAccount=0 时直放（零回归）。
// 牙齿：把 concurrencyWait 的分桶 key 改成全 "global" → b1 也会卡在 acquire
// （calls 停在 2，第 3 处断言红）。
func TestConcurrencyRoundTripperBuckets(t *testing.T) {
	gate := make(chan struct{}) // 模拟慢上游：请求在途占槽
	var calls int64
	next := rtFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt64(&calls, 1)
		<-gate
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})
	c := &Client{ConcurrencyPerAccount: 2}
	rt := &concurrencyRoundTripper{c: c, next: next}

	do := func(mid string) error {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://upstream.test/", nil)
		if mid != "" {
			req.Header.Set("X-Machine-ID", mid)
		}
		_, err := rt.RoundTrip(req)
		return err
	}

	waitCalls := func(want int64) bool {
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if atomic.LoadInt64(&calls) >= want {
				return true
			}
			time.Sleep(2 * time.Millisecond)
		}
		return false
	}

	// 账号 a 并发 3：前 2 进入在途（calls=2），第 3 个排队（不能到 next）
	errA1 := make(chan error, 1)
	go func() { errA1 <- do("a") }()
	errA2 := make(chan error, 1)
	go func() { errA2 <- do("a") }()
	if !waitCalls(2) {
		t.Fatal("a1/a2 未进入在途")
	}
	errA3 := make(chan error, 1)
	go func() { errA3 <- do("a") }()
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("a3 应排队（calls 仍 2），got %d", got)
	}
	// 账号 b 不受 a 在途影响：立即过 acquire 进 next（calls=3）
	errB1 := make(chan error, 1)
	go func() { errB1 <- do("b") }()
	if !waitCalls(3) {
		t.Fatal("b1 被 a 的在途阻塞——分桶失效")
	}
	// 在途观测：a 号 in_flight=2、queue=1（b 号 limiter 已建，in_flight=1）
	rh := c.RhythmStatus()
	a, ok := rh["a"].(map[string]int)
	if !ok || a["in_flight"] != 2 || a["queue_depth"] != 1 {
		t.Fatalf("rhythm[a] = %v, want in_flight=2 queue=1", rh["a"])
	}
	b, ok := rh["b"].(map[string]int)
	if !ok || b["in_flight"] != 1 {
		t.Fatalf("rhythm[b] = %v, want in_flight=1", rh["b"])
	}

	// 放行全部在途：a1/a2/b1 完成，a3 拿到槽位后也完成
	close(gate)
	for _, ch := range []chan error{errA1, errA2, errB1, errA3} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("请求应完成: %v", err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("放行后请求未完成")
		}
	}
	if got := atomic.LoadInt64(&calls); got != 4 {
		t.Fatalf("应 4 次出站, got %d", got)
	}

	// 关闭态（0）：任意数量直放（零回归）
	c0 := &Client{ConcurrencyPerAccount: 0}
	rt0 := &concurrencyRoundTripper{c: c0, next: rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})}
	for i := 0; i < 3; i++ {
		if err := do0(rt0); err != nil {
			t.Fatalf("关闭态第 %d 个应直放: %v", i+1, err)
		}
	}
	if len(c0.RhythmStatus()) != 0 {
		t.Fatalf("零配置时 RhythmStatus 应为空表, got %v", c0.RhythmStatus())
	}
}

// TestConcurrencyRoundTripperContextCancel：排队中 ctx 取消 → RoundTrip 返回
// ctx.Canceled（经过 RoundTripper 层的完整路径）。
func TestConcurrencyRoundTripperContextCancel(t *testing.T) {
	gate := make(chan struct{})
	next := rtFunc(func(r *http.Request) (*http.Response, error) {
		<-gate
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})
	c := &Client{ConcurrencyPerAccount: 1}
	rt := &concurrencyRoundTripper{c: c, next: next}

	// 占满 1 个在途槽
	req1, _ := http.NewRequestWithContext(context.Background(), "GET", "https://upstream.test/", nil)
	req1.Header.Set("X-Machine-ID", "a")
	done1 := make(chan struct{})
	go func() {
		_, _ = rt.RoundTrip(req1)
		close(done1)
	}()
	time.Sleep(10 * time.Millisecond)

	// 第 2 个排队后取消
	cctx, cancel := context.WithCancel(context.Background())
	req2, _ := http.NewRequestWithContext(cctx, "GET", "https://upstream.test/", nil)
	req2.Header.Set("X-Machine-ID", "a")
	done2 := make(chan error, 1)
	go func() {
		_, err := rt.RoundTrip(req2)
		done2 <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done2:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应 ctx.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("取消后 RoundTrip 仍悬挂")
	}

	close(gate)
	<-done1
}

// TestMachineIDForUIDMatchesHeader 钉死「/status 可读桶名用的派生」与「出站头」同源：
// rhythm 桶名 = X-Machine-ID，两者必须逐字节相等，否则运维换了可读名也对不上号。
// 牙齿：给 MachineIDForUID 换盐或改截断长度 → 本测试红。
func TestMachineIDForUIDMatchesHeader(t *testing.T) {
	c := New()
	req, err := http.NewRequest("POST", "https://upstream.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &auth.Auth{UID: "0250aab9-3697-47c9-9133-3a2cc7ec3af1"}
	c.injectAccountStableHeaders(req, a)
	hdr := req.Header.Get("X-Machine-ID")
	if hdr == "" {
		t.Fatal("X-Machine-ID 未注入")
	}
	if got := MachineIDForUID(a.UID); got != hdr {
		t.Fatalf("MachineIDForUID=%s 与出站头 %s 不一致（两处派生漂移）", got, hdr)
	}
	if n := len(MachineIDForUID(a.UID)); n != 36 {
		t.Fatalf("machine id 长度=%d want 36", n)
	}
}
