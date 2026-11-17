// main_observability.go 进程级观测单例装配（从 main() 切出）。
package main

import (
	"yg-gateway/internal/metrics"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/unknown"
)

// observability 承载进程级观测单例（全进程共享，重启归零：分位数与计数都是
// 近期健康度而非历史账本）。
type observability struct {
	latency  *metrics.Registry
	ttfb     *metrics.Registry
	errKinds *metrics.KindCounters
	attempts *metrics.AttemptsStats
	slowReqs *metrics.Counter
	unknown  *unknown.Registry
}

// buildObservability 构造观测单例并接好未知条目注册表。
//
// 口径（与拆分前 main() 内联版本一致）：
//   - 延迟分位数（ADR-003 D-403）：自实现固定分桶，零新依赖；
//   - TTFB 与端到端延迟分开记——两者回答的问题不同（"总共等多久" vs "上游多快开始吐"）；
//   - 错误分类把既有 Classify 的 13 种 ErrKind 导出成时序指标；
//   - 重试放大率回答「每个用户请求触发了几次上游调用」，慢请求是端到端超阈值的
//
// 显式告警信号——两者都是纯观测，不改写请求路径。
func buildObservability(p *pool.Pool) *observability {
	o := &observability{
		latency:  metrics.NewRegistry(),
		ttfb:     metrics.NewRegistry(),
		errKinds: metrics.NewKindCounters(),
		attempts: metrics.NewAttemptsStats(),
		slowReqs: metrics.NewCounter(),
		unknown:  unknown.NewRegistry(),
	}
	// 未知条目注册表（AGENTS.md「Unknown 消解纪律」）：把「某号某模型成本未知」
	// 这类可消解的事实变成一等公民实体——登记时写明还需要什么，实测 credit 时
	// 自动消解。
	p.SetUnknownRegistry(o.unknown)
	return o
}
