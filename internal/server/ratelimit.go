// ratelimit.go 只读端点轻量限流（F-003）。
//
// 背景：/v1/models、/status 这类只读端点无任何频控，可被刷。每次 /v1/models 的
// 缓存失效都可能放大为 2 次上游探测，客户端周期性刷新即持续打上游（上游故障时
// 无冷却窗口，重试会放大请求量）。给只读端点加进程内滑窗限流，超阈返回 429。
//
// 实现选择：自写固定窗口计数器（sync.Mutex + 时间戳），不引 golang.org/x/time/rate。
// 理由：个人网关的限流语义极简（全局一档 + 可选按来源 IP 一档），固定窗口足够、
// 20 行可测；引 x/time/rate 会破坏「单文件自包含 + 最少依赖」的哲学（现仅 redis
// 一个直接依赖）。语义核对：固定窗口在窗口边界处允许瞬时 2× 突发，对本场景
// （防无脑刷取，非精确配额）可接受。
package server

import (
	"sync"
	"time"
)

// ReadOnlyRateLimit 只读端点限流配置（装配期固定，不热改——热改成 0 等于禁用防护）。
type ReadOnlyRateLimit struct {
	// Limit 窗口内最大请求数（全局按来源 IP 计数）。
	Limit int
	// Window 滑窗长度。
	Window time.Duration
}

// build 实例化限流器；Limit<=0 或 Window<=0 时返回 nil（禁用）。
func (c *ReadOnlyRateLimit) build() *rateLimiter {
	if c == nil {
		return nil
	}
	return newRateLimiter(c.Limit, c.Window)
}

// rateLimiter 固定窗口限流器（进程内，重启清零）。
type rateLimiter struct {
	mu      sync.Mutex
	limit   int           // 窗口内最大请求数
	window  time.Duration // 窗口长度
	counts  map[string]int
	windowStart time.Time
}

// newRateLimiter 构造限流器。limit <= 0 返回 nil（禁用限流，调用方据此跳过）。
func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	if limit <= 0 || window <= 0 {
		return nil
	}
	return &rateLimiter{
		limit:       limit,
		window:      window,
		counts:      map[string]int{},
		windowStart: time.Now(),
	}
}

// allow 报告 key 是否允许通过（并计数）。key 为空时退化为全局单一计数。
// 返回 false 表示应拒绝（429）。
func (rl *rateLimiter) allow(key string) bool {
	if rl == nil {
		return true
	}
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if now.Sub(rl.windowStart) >= rl.window {
		// 窗口滑过：整体重置（固定窗口，非滑窗——简单且对个人网关够用）。
		rl.windowStart = now
		rl.counts = map[string]int{}
	}
	if key == "" {
		key = "\x00global" // 空串与全局键隔离，避免与「无 key」歧义
	}
	n := rl.counts[key]
	if n >= rl.limit {
		return false
	}
	rl.counts[key] = n + 1
	return true
}
