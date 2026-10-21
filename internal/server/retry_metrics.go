// retry_metrics.go F-305 可观测性补齐：重试放大率直方图 + 慢请求计数的请求收口观测点。
//
// 为什么单独成文件：收口观测只有一个函数，但它把 chatCompletions 的 defer 与
// internal/metrics 的 AttemptsStats/Counter 粘起来；handler.go 已 1451 行，继续往里
// 塞会顶深度棘轮（>1000 行文件数只许收紧）。
package server

import "time"

// observeRequestEnd 每个 chat 请求收口时观测一次（由 chatCompletions 的 defer 挂载，
// 恰好执行一次）。
//
// 覆盖范围：成功透传 / 流式错误帧 / 轮转耗尽 / fallback / 末端错误构造——所有离开
// chatCompletions 的路径都走 defer，成功与各种失败结局一视同仁。中途不会重复
// （defer 只在出口跑一次），也不会泄漏到轮转中途（选号失败、退避重试都不经过这里）。
//
// st.attempts 的并发前提：recordAttempt 与本收口观测都在 chatCompletions 所在的
// 同一个 goroutine（chatrun.go 注释明说本结构不跨 goroutine），defer 触发时
// 全部 recordAttempt 早已完成，无竞态。
func (h *Handler) observeRequestEnd(st *chatStat, model string) {
	// 重试放大率：只覆盖真正打过上游的请求（attempts >= 1）。
	// attempts == 0 是网关本地拒绝（无可用账号 / 选号即中止），在
	// requests_total 里已有计数；混进「1 次 vs >1 次」分布会把 le=1 桶污染成
	// 「0 次与 1 次不可区分」。放大率均值仍可经 upstream_attempts_total /
	// requests_total{outcome=completed} 算出（0 次请求作 0 计入分母）。
	if h.cfg.Attempts != nil && st.attempts >= 1 {
		h.cfg.Attempts.Observe(model, st.attempts)
	}
	// 慢请求：端到端耗时（含轮转退避与等待上游）超过阈值才计数。
	// 阈值 <=0 = 关闭（旧配置零回归）。不依赖后台 goroutine/ticker（R9）：
	// 只在请求收口的 defer 里做一次时间比较。
	if h.cfg.SlowRequests != nil && h.cfg.SlowRequestThreshold > 0 {
		if d := time.Since(st.start); d > h.cfg.SlowRequestThreshold {
			h.cfg.SlowRequests.Inc()
		}
	}
}
