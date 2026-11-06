// concurrency.go 出站在途并发闸（REA §2.5 节奏面另一半：RPM 限速率、并发闸限在途）。
//
// 语义（吸收 magpie concurrency.go "a Codex account is risk-controlled past five or
// six requests at once" 的教训，形态做成 yg 可观测版本）：
//   - 每账号（X-Machine-ID）同时在途出站请求数 ≤ ConcurrencyPerAccount；
//   - 超过 → FIFO 排队等待（不失败、不换号），ctx 取消即返回；
//   - 长请求（SSE 流式）占在途槽位直到 RoundTrip 返回（区别于 rpm 的 1s 槽）；
//   - 与 rpm 硬闸正交：速率闸管"多快"，并发闸管"同时几个"——短时间并发打满一个号
//     时 RPM 拦不住（速率正常、在途高），这正是"把号打热"的另一条路。
//
// 可观测（magpie 明确没有 per-账号在途观测，REA §2.7 自承 unknown）：
//
//	Client.RhythmStatus() 暴露每账号 {in_flight, queue_depth}，/status 只增键输出。
//	可见优先于精确——与 proxy_status（P0-2）同哲学。
//
// 挂载点：newUpstreamHTTP 与 rpmRoundTripper 同位置（并发闸外层、rpm 内层：
// 先卡在途、再卡速率，两者独立 limiter、独立锁，互不耦合）。
// ConcurrencyPerAccount=0 直接放行（零回归）。
package upstream

import (
	"context"
	"net/http"
	"sync"
)

// concurrencyLimiter 单账号在途限流器：inflight 计数 + FIFO 等待队列。
// max 可注入（测试）；count/队列状态可快照（/status 观测）。
type concurrencyLimiter struct {
	mu       sync.Mutex
	max      int
	inflight int
	waiters  []chan struct{}
}

func newConcurrencyLimiter(max int) *concurrencyLimiter {
	return &concurrencyLimiter{max: max}
}

// acquire 阻塞直到获得在途槽位。ctx 取消返回 ctx.Err()，不悬挂。
// 竞态语义：release 转交槽位与 ctx 取消同时发生时，若取消方发现槽位已被转交
// （ch 已不在 waiters），立即调用 release 归还，保证 inflight 不泄漏。
func (l *concurrencyLimiter) acquire(ctx context.Context) error {
	l.mu.Lock()
	if l.inflight < l.max {
		l.inflight++
		l.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	l.waiters = append(l.waiters, ch)
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.mu.Lock()
		found := false
		for i, w := range l.waiters {
			if w == ch {
				l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
				found = true
				break
			}
		}
		l.mu.Unlock()
		if !found {
			// 槽位已被 release 转交（ch 已关闭）：放弃使用，归还给后续等待者。
			l.release()
		}
		return ctx.Err()
	case <-ch:
		return nil
	}
}

// release 归还一个在途槽位；若有等待者，直接转交（FIFO），inflight 保持 ≤ max。
func (l *concurrencyLimiter) release() {
	l.mu.Lock()
	if len(l.waiters) > 0 {
		w := l.waiters[0]
		l.waiters = l.waiters[1:]
		l.mu.Unlock()
		close(w) // inflight 不变：槽位直接转交
		return
	}
	if l.inflight > 0 {
		l.inflight--
	}
	l.mu.Unlock()
}

// snapshot 返回在途/排队深度（/status 观测）。
func (l *concurrencyLimiter) snapshot() (inflight, queue int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight, len(l.waiters)
}

// concurrencyRoundTripper 出站并发闸 RoundTripper：按账号分桶限在途后转发。
type concurrencyRoundTripper struct {
	c    *Client
	next http.RoundTripper
}

func (r *concurrencyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := r.c.concurrencyWait(req); err != nil {
		return nil, err
	}
	resp, err := r.next.RoundTrip(req)
	r.c.concurrencyRelease(req)
	return resp, err
}

// CloseIdleConnections 透传底层 transport 的空闲连接池清理能力
// （与 rpmRoundTripper 同款；wrapper 不得吞掉该能力，否则传输层失败后的清池行为回归）。
func (r *concurrencyRoundTripper) CloseIdleConnections() {
	if ci, ok := r.next.(closeIdler); ok {
		ci.CloseIdleConnections()
	}
}

// concurrencyWait 出站前按账号取限流器并等待在途槽位。
// key = X-Machine-ID（账号稳定标识，headers.go 全出站注入，与 rpmWait 同口径）；
// 空（防御兜底）→ global 桶，受同一配置约束，不绕过硬闸。
// ConcurrencyPerAccount <= 0 → 直放（零回归）。
func (c *Client) concurrencyWait(req *http.Request) error {
	if c.ConcurrencyPerAccount <= 0 {
		return nil
	}
	key := req.Header.Get("X-Machine-ID")
	if key == "" {
		key = "global"
	}
	c.concMu.Lock()
	if c.concLimiters == nil {
		c.concLimiters = make(map[string]*concurrencyLimiter)
	}
	l, ok := c.concLimiters[key]
	if !ok {
		l = newConcurrencyLimiter(c.ConcurrencyPerAccount)
		c.concLimiters[key] = l
	}
	c.concMu.Unlock()
	return l.acquire(req.Context())
}

// concurrencyRelease 归还账号的在途槽位（RoundTrip 返回后调用）。
func (c *Client) concurrencyRelease(req *http.Request) {
	if c.ConcurrencyPerAccount <= 0 {
		return
	}
	key := req.Header.Get("X-Machine-ID")
	if key == "" {
		key = "global"
	}
	c.concMu.Lock()
	l, ok := c.concLimiters[key]
	c.concMu.Unlock()
	if ok {
		l.release()
	}
}

// RhythmStatus 返回并发闸观测快照（/status rhythm 键）：每账号 {in_flight, queue_depth}。
// 未装配/零配置返回空表（零回归只增键）。RPM 排队深度不在此暴露（rpm.go 无快照接口，
// 不为观测改动已部署机制；在途并发是运维最关心的瞬时量）。
func (c *Client) RhythmStatus() map[string]any {
	out := map[string]any{}
	if c.ConcurrencyPerAccount <= 0 {
		return out
	}
	c.concMu.Lock()
	defer c.concMu.Unlock()
	for k, l := range c.concLimiters {
		inflight, queue := l.snapshot()
		out[k] = map[string]int{"in_flight": inflight, "queue_depth": queue}
	}
	return out
}
