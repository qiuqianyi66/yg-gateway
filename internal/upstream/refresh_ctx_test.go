package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// newRefreshTestClient 构造一个把 refresh 端点指向测试服务器的 Client。
func newRefreshTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := New()
	c.ChatBaseCN = srv.URL
	c.BillingBaseCN = srv.URL
	return c
}

func newTestAuth() *auth.Auth {
	return &auth.Auth{
		UID:          "uid-refresh-1",
		AccessToken:  "at-old",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(2 * time.Hour).Unix(),
		Domain:       "cn",
	}
}

// refreshSrv 返回一个计数型 refresh 服务器（成功时轮换 token）。
func refreshSrv(counter *atomic.Int64, delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"accessToken":"at-new","refreshToken":"rt-new","expiresIn":5184000}}`))
	}))
}

// TestRefreshTokenContextCancelled 调用方 ctx 取消 → 刷新请求随之取消（F-708）。
//
// 牙齿：把 refreshTokenLocked 里的 ctx 改回 context.Background() → 本测试 FAIL
// （取消后 refreshSrv 仍在跑，cancel 等不到它返回）。
func TestRefreshTokenContextCancelled(t *testing.T) {
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		time.Sleep(2 * time.Second) // 比测试的取消时机长
		w.Write([]byte(`{"code":0,"data":{"accessToken":"at-new"}}`))
	}))
	defer srv.Close()
	c := newRefreshTestClient(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.RefreshTokenContext(ctx, newTestAuth()) }()
	<-entered // 确认请求已进服务器
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after ctx cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RefreshTokenContext did not observe ctx cancel (still using Background?)")
	}
}

// TestRefreshTokenSingleFlight 同 uid 的并发刷新只打一次上游（F-709）。
//
// 牙齿：把 RefreshTokenContext 里的 refreshGroup 分支去掉 → 本测试 FAIL
// （N 个并发各打一次，计数 > 1）。
func TestRefreshTokenSingleFlight(t *testing.T) {
	var hits atomic.Int64
	srv := refreshSrv(&hits, 150*time.Millisecond)
	defer srv.Close()
	c := newRefreshTestClient(t, srv)

	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	a := newTestAuth()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = c.RefreshTokenContext(context.Background(), a)
		}(i)
	}
	wg.Wait()
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream refresh hits=%d want 1 (singleflight)", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d err=%v", i, err)
		}
	}
	// leader 已写回新 token（等待方复用同一结果，不需要各自写回）。
	a.Lock()
	at, rt := a.AccessToken, a.RefreshToken
	a.Unlock()
	if at != "at-new" || rt != "rt-new" {
		t.Errorf("token not rotated: at=%q rt=%q want at-new/rt-new", at, rt)
	}
}

// TestRefreshTokenSingleFlightSequentialReuses 串行调用不会互相阻塞（leader
// 结束后 group 里不留条目，下一个请求重新成为 leader）。
func TestRefreshTokenSingleFlightSequentialReuses(t *testing.T) {
	var hits atomic.Int64
	srv := refreshSrv(&hits, 0)
	defer srv.Close()
	c := newRefreshTestClient(t, srv)

	for i := 0; i < 3; i++ {
		if err := c.RefreshToken(newTestAuth()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("hits=%d want 3 (sequential calls must each refresh)", got)
	}
	c.refreshGroup.mu.Lock()
	left := len(c.refreshGroup.m)
	c.refreshGroup.mu.Unlock()
	if left != 0 {
		t.Errorf("refreshGroup leaked %d entries", left)
	}
}

// TestRefreshTokenGroupNil Waiter 在 leader 完成前取消 → 返回 ctx 错误，且不影响
// leader 完成（group 不留条目）。
func TestRefreshTokenGroupNil(t *testing.T) {
	var g refreshGroup
	if err := g.do(context.Background(), "k", func() error { return nil }); err != nil {
		t.Fatalf("do err=%v", err)
	}
	// 无并发时与普通调用等价。
	if err := g.do(context.Background(), "k", func() error { return context.DeadlineExceeded }); err == nil {
		t.Fatal("fn error must propagate")
	}
}
