// rpm.go 出站 RPM 硬闸（REA P0 最高优先级，可配置默认关）。
//
// 语义（对照 magpie rpm.go "60s+1s 滑动窗 + RoundTripper 卡槽位"）：
//   - 60s 滑动窗口内每账号最多 rpmPerAccount 个出站槽位；
//   - 每个槽位占用 1s（请求间最小间隔，平滑突发；长 SSE 请求只占 1s 槽，
//     不饿死后续请求）；
//   - 窗口满 → 阻塞等待最早槽位滑出（不失败、不换号），ctx 取消即返回。
//
// 挂载点：newUpstreamHTTP 统一包在全部出站 client（直连 + per-proxy）的
// Transport 外层，RoundTrip 时按 X-Machine-ID（账号稳定标识，全出站注入）
// 分桶限速；RpmPerAccount=0 直接放行（零回归）。
package upstream

import (
	"context"
	"net/http"
	"sync"
	"time"
)

const (
	rpmWindow = 60 * time.Second // 滑动窗口
	rpmSlot   = 1 * time.Second  // 槽位占用（请求间最小间隔）
)

// rpmLimiter 单账号出站限速器：滑动窗 + 槽位。
// window/slot/max 可注入（测试）；now/after 可注入（可控时钟测试）。
type rpmLimiter struct {
	mu     sync.Mutex
	window time.Duration
	slot   time.Duration
	max    int
	now    func() time.Time
	after  func(time.Duration) <-chan time.Time
	ts     []time.Time // 窗口内槽位开始时刻（有序）
}

func newRpmLimiter(max int, window, slot time.Duration) *rpmLimiter {
	return &rpmLimiter{window: window, slot: slot, max: max, now: time.Now, after: time.After}
}

// wait 阻塞直到获得槽位。ctx 取消时返回 ctx.Err()，不悬挂。
func (l *rpmLimiter) wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := l.now()
		// 滑出窗口外的旧槽位
		cut := 0
		for cut < len(l.ts) && now.Sub(l.ts[cut]) > l.window {
			cut++
		}
		l.ts = l.ts[cut:]

		var wait time.Duration
		if len(l.ts) >= l.max {
			// 窗口满：等最早槽位滑出窗口
			wait = l.ts[0].Add(l.window).Sub(now)
		} else if len(l.ts) > 0 {
			// 槽位约束：距上次出站至少 slot
			wait = l.ts[len(l.ts)-1].Add(l.slot).Sub(now)
		}
		if wait <= 0 {
			l.ts = append(l.ts, now)
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.after(wait):
		}
	}
}

// rpmRoundTripper 出站硬闸 RoundTripper：按账号分桶限速后转发。
type rpmRoundTripper struct {
	c    *Client
	next http.RoundTripper
}

func (r *rpmRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := r.c.rpmWait(req); err != nil {
		return nil, err
	}
	return r.next.RoundTrip(req)
}

// CloseIdleConnections 透传底层 transport 的空闲连接池清理能力
// （client.go:710 roundTripCloseIdle 依赖；wrapper 不得吞掉该能力，否则
// 传输层失败后的清池行为回归）。
func (r *rpmRoundTripper) CloseIdleConnections() {
	if ci, ok := r.next.(closeIdler); ok {
		ci.CloseIdleConnections()
	}
}

// rpmWait 出站前按账号取限速器并等待槽位。
// key = X-Machine-ID（账号稳定标识，headers.go 全出站注入）；
// 空（防御兜底，理论不发生）→ global 桶，受同一配置约束，不绕过硬闸。
// RpmPerAccount <= 0 → 直放（零回归）。
func (c *Client) rpmWait(req *http.Request) error {
	if c.RpmPerAccount <= 0 {
		return nil
	}
	key := req.Header.Get("X-Machine-ID")
	if key == "" {
		key = "global"
	}
	window, slot := c.rpmWindow, c.rpmSlot
	if window <= 0 {
		window = rpmWindow
	}
	if slot <= 0 {
		slot = rpmSlot
	}
	c.rpmMu.Lock()
	if c.rpmLimiters == nil {
		c.rpmLimiters = make(map[string]*rpmLimiter)
	}
	l, ok := c.rpmLimiters[key]
	if !ok {
		l = newRpmLimiter(c.RpmPerAccount, window, slot)
		c.rpmLimiters[key] = l
	}
	c.rpmMu.Unlock()
	return l.wait(req.Context())
}
