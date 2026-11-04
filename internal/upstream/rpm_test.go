package upstream

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock 可控时钟，驱动 rpmLimiter 的滑动窗/槽位语义。
type fakeClock struct {
	t atomic.Int64 // 纳秒
}

func (f *fakeClock) now() time.Time          { return time.Unix(0, f.t.Load()) }
func (f *fakeClock) advance(d time.Duration) { f.t.Add(int64(d)) }

func newTestRpmLimiter(max int, window, slot time.Duration, now func() time.Time) *rpmLimiter {
	l := newRpmLimiter(max, window, slot)
	l.now = now
	return l
}

// TestRpmLimiterWindowAndSlot：窗口满等待最早滑出；槽位 1s 最小间隔。
// 场景：max=2, window=60s, slot=1s（after 由测试控制：推进时钟后释放）。
//
//	t=0     r1 通过（ts=[0]）
//	t=1s    r2 通过（槽位释放，ts=[0,1]）
//	t=2s    r3 等待：窗口满（2/2）→ 等 ts[0]+60s = t=60s
//	t=60s   r3 放行
func TestRpmLimiterWindowAndSlot(t *testing.T) {
	clk := &fakeClock{}
	l := newTestRpmLimiter(2, 60*time.Second, 1*time.Second, clk.now)
	release := make(chan struct{})
	l.after = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time)
		go func() {
			<-release
			ch <- time.Now()
		}()
		return ch
	}

	ctx := context.Background()

	// t=0：r1 立即通过
	if err := l.wait(ctx); err != nil {
		t.Fatalf("r1 应通过: %v", err)
	}
	// t=1s：r2 通过（距上次 1s）
	clk.advance(1 * time.Second)
	if err := l.wait(ctx); err != nil {
		t.Fatalf("r2 应通过（槽位已释放）: %v", err)
	}
	// t=2s：r3 窗口满，应阻塞（不能立即返回）
	clk.advance(1 * time.Second)
	done := make(chan error, 1)
	go func() { done <- l.wait(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("r3 在窗口满时不应立即通过: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// t=60s：最早槽位滑出窗口 + 释放睡眠，r3 放行
	clk.advance(58 * time.Second)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("r3 滑出后应通过: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("r3 在最早槽位滑出后仍未放行")
	}
}

// TestRpmLimiterContextCancel：等待中 ctx 取消 → 返回 ctx.Err()，不悬挂。
// 用真实时钟（默认 after）：slot=1s 的等待窗口内取消，确定性。
func TestRpmLimiterContextCancel(t *testing.T) {
	l := newRpmLimiter(1, 60*time.Second, 1*time.Second)

	ctx := context.Background()
	if err := l.wait(ctx); err != nil {
		t.Fatalf("首个应通过: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- l.wait(cctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应返回 ctx.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx 取消后仍悬挂")
	}
}

// TestRpmRoundTripperBuckets：按 X-Machine-ID 分桶——两账号互不占用槽位；
// 无头请求归 global 桶；RpmPerAccount=0 时直放（零回归）。
func TestRpmRoundTripperBuckets(t *testing.T) {
	var calls int64
	next := rtFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt64(&calls, 1)
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})

	c := &Client{RpmPerAccount: 2, rpmWindow: 5 * time.Second, rpmSlot: 1 * time.Millisecond}
	rt := &rpmRoundTripper{c: c, next: next}

	do := func(mid string) error {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://upstream.test/", nil)
		if mid != "" {
			req.Header.Set("X-Machine-ID", mid)
		}
		_, err := rt.RoundTrip(req)
		return err
	}

	// 账号 a 用满 2 个槽位（slot 极小，两请求快速通过）
	if err := do("a"); err != nil {
		t.Fatalf("a1: %v", err)
	}
	if err := do("a"); err != nil {
		t.Fatalf("a2: %v", err)
	}
	// 账号 b 不受 a 影响：b1/b2 立即通过（若共享桶，第 3 个会卡）
	if err := do("b"); err != nil {
		t.Fatalf("b1: %v", err)
	}
	if err := do("b"); err != nil {
		t.Fatalf("b2: %v", err)
	}
	// 无头请求归 global 桶，独立计数：立即通过
	if err := do(""); err != nil {
		t.Fatalf("global: %v", err)
	}
	if got := atomic.LoadInt64(&calls); got != 5 {
		t.Fatalf("应 5 次出站, got %d", got)
	}

	// 关闭态（RpmPerAccount=0）：任意数量直放
	c0 := &Client{RpmPerAccount: 0}
	rt0 := &rpmRoundTripper{c: c0, next: rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})}
	for i := 0; i < 3; i++ {
		if err := do0(rt0); err != nil {
			t.Fatalf("关闭态第 %d 个应直放: %v", i+1, err)
		}
	}
}

func do0(rt http.RoundTripper) error {
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://upstream.test/", nil)
	req.Header.Set("X-Machine-ID", "x")
	_, err := rt.RoundTrip(req)
	return err
}
