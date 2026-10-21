// metrics_handler.go Prometheus 文本导出端点（F-710）。
//
// 为什么另起一个文件而不是塞进 handler.go：handler.go 已 2000+ 行，而这个
// 端点与 /status 是「同一批数据、两种口径」——/status 给面板（JSON + 分位数），
// /metrics 给抓取端（文本 + 分桶 + 速率）。分开写也方便单测只测暴露格式。
//
// 指标清单（克制：只暴露运维真正会看的，不做「什么都有」的指标海）：
//   - yg_gateway_requests_total{outcome}  请求计数（按结局）
//   - yg_gateway_requests_in_flight       当前在途
//   - yg_gateway_request_duration_ms_*    端到端延迟分桶（全局 + 按模型）
//   - yg_gateway_accounts{state}          账号池状态分布
//   - yg_gateway_accounts_in_flight       账号在途总数
//   - yg_gateway_global_in_flight         全网关在途
//   - yg_gateway_tokens_total{kind}       token 计数（prompt/completion/total）
//   - yg_gateway_tokens_cache_hit_total   前缀缓存命中 token
//
// 与 /status 的关系：/status 的 latency 段（Registry.Snapshot）与本文件的
// duration 直方图**同源**（同一个 *metrics.Registry），不存在两套统计口径。
package server

import (
	"net/http"
	"strconv"
	"strings"

	"yg-gateway/internal/metrics"
	"yg-gateway/internal/usage"
)

// metricNamePrefix 所有指标名前缀（便于抓取侧按前缀筛选整个网关）。
const metricNamePrefix = "yg_gateway_"

// metricsHandler GET /metrics：Prometheus 文本格式。
//
// 鉴权口径与 /status 一致（withAuth + rateLimited）：指标含账号池状态与 token
// 量，属敏感运维数据，不对未鉴权方开放。未启用鉴权时与 /status 同样放行。
func (h *Handler) metricsHandler(w http.ResponseWriter, r *http.Request) {
	p := metrics.NewPrometheus()

	// ---- 请求侧（reqlog 内存指标；RequestLog 未启用时全部缺省 0）----
	var (
		completed, succeeded, failed, inFlight int64
		successRate, avgDurationMs             float64
	)
	if snap := h.reqlogSnapshot(); snap != nil {
		completed = snap.Completed
		succeeded = snap.Succeeded
		failed = snap.Failed
		inFlight = snap.InFlight
		successRate = snap.SuccessRate
		avgDurationMs = snap.AvgDurationMs
	}
	p.Help(metricNamePrefix+"requests_total", "Total chat requests completed since start, by outcome.").
		Type(metricNamePrefix+"requests_total", "counter")
	// 结局三分：succeeded（HTTP 2xx 且流正常）/ failed（其余）/ completed（总计）。
	// 不细分 interrupted/stream_error：那些在 reqlog.Recent 里逐条可查，
	// 指标层只回答「成功率趋势」这一个问题。
	p.Counter(metricNamePrefix+"requests_total", succeeded, `outcome="succeeded"`)
	p.Counter(metricNamePrefix+"requests_total", failed, `outcome="failed"`)
	p.Counter(metricNamePrefix+"requests_total", completed, `outcome="completed"`)
	p.Help(metricNamePrefix+"requests_in_flight", "Requests currently being processed.").
		Type(metricNamePrefix+"requests_in_flight", "gauge")
	p.Counter(metricNamePrefix+"requests_in_flight", inFlight)
	p.Help(metricNamePrefix+"request_success_rate", "Success rate over completed requests.").
		Type(metricNamePrefix+"request_success_rate", "gauge")
	p.GaugeFloat(metricNamePrefix+"request_success_rate", successRate)
	p.Help(metricNamePrefix+"request_duration_ms_avg", "Mean end-to-end duration over completed requests.").
		Type(metricNamePrefix+"request_duration_ms_avg", "gauge")
	p.GaugeFloat(metricNamePrefix+"request_duration_ms_avg", avgDurationMs)

	// ---- 延迟分桶（与 /status.latency 同源）----
	if h.cfg.Latency != nil {
		snap := h.cfg.Latency.Snapshot()
		p.Help(metricNamePrefix+"request_duration_ms", "End-to-end request latency histogram (global).").
			Type(metricNamePrefix+"request_duration_ms", "histogram")
		p.Histogram(metricNamePrefix+"request_duration_ms", h.cfg.Latency.Global())
		// 按模型：模型名来自上游目录（有界），标签基数可控。
		for model, hs := range snap.ByModel {
			label := `model="` + escapeLabel(model) + `"`
			p.Histogram(metricNamePrefix+"request_duration_ms", hs.UnsafeHistogram(), label)
		}
	}

	// ---- TTFB 分桶（首 token 延迟；与端到端延迟是两个不同的问题）----
	if h.cfg.TTFB != nil {
		p.Help(metricNamePrefix+"ttfb_ms", "Time to first upstream byte histogram (global).").
			Type(metricNamePrefix+"ttfb_ms", "histogram")
		p.Histogram(metricNamePrefix+"ttfb_ms", h.cfg.TTFB.Global())
		for model, hs := range h.cfg.TTFB.Snapshot().ByModel {
			label := `model="` + escapeLabel(model) + `"`
			p.Histogram(metricNamePrefix+"ttfb_ms", hs.UnsafeHistogram(), label)
		}
	}

	// ---- 重试放大率（F-305）：单请求上游尝试次数分布 ----
	// 读法：_count = 打到过上游的请求数；_sum/_count = 这些请求的平均放大率。
	// 整请求口径的放大率均值 = upstream_attempts_total / requests_total{outcome="completed"}
	// （0 次尝试的网关本地拒绝请求作 0 计入分母，故分母比本直方图 _count 大）。
	if h.cfg.Attempts != nil {
		p.Help(metricNamePrefix+"upstream_attempts_per_request", "Upstream attempts per completed user request that reached upstream (retry amplification). le=1 bucket = no account-rotation retry; le>1 = retried/failover happened. Mean amplification = upstream_attempts_total / requests_total{outcome=\"completed\"}.").
			Type(metricNamePrefix+"upstream_attempts_per_request", "histogram")
		p.HistogramInt(metricNamePrefix+"upstream_attempts_per_request", h.cfg.Attempts.Global())
		for model, hs := range h.cfg.Attempts.ModelSnapshot() {
			label := `model="` + escapeLabel(model) + `"`
			p.HistogramInt(metricNamePrefix+"upstream_attempts_per_request", hs, label)
		}
		p.Help(metricNamePrefix+"upstream_attempts_total", "Total upstream attempts across all user requests (numerator of the amplification ratio).").
			Type(metricNamePrefix+"upstream_attempts_total", "counter")
		p.Counter(metricNamePrefix+"upstream_attempts_total", h.cfg.Attempts.Total())
	}

	// ---- 慢请求（F-305）：端到端耗时超阈值的显式计数信号 ----
	if h.cfg.SlowRequests != nil {
		p.Help(metricNamePrefix+"slow_requests_total", "Completed requests whose end-to-end duration exceeded the configured slow threshold (observational counter; no request-path impact).").
			Type(metricNamePrefix+"slow_requests_total", "counter")
		p.Counter(metricNamePrefix+"slow_requests_total", h.cfg.SlowRequests.Value())
	}

	// ---- 上游错误分类计数 ----
	if h.cfg.ErrKinds != nil {
		p.Help(metricNamePrefix+"upstream_errors_total", "Upstream errors by classification kind.").
			Type(metricNamePrefix+"upstream_errors_total", "counter")
		for kind, n := range h.cfg.ErrKinds.Snapshot() {
			p.Counter(metricNamePrefix+"upstream_errors_total", n, `kind="`+escapeLabel(kind)+`"`)
		}
	}

	// ---- 账号池状态 ----
	total, healthy, cooling, disabled, inFlightFull := h.cfg.Pool.CountsDetailed()
	p.Help(metricNamePrefix+"accounts", "Account pool size by state.").
		Type(metricNamePrefix+"accounts", "gauge")
	p.Counter(metricNamePrefix+"accounts", int64(total), `state="total"`)
	p.Counter(metricNamePrefix+"accounts", int64(healthy), `state="healthy"`)
	p.Counter(metricNamePrefix+"accounts", int64(cooling), `state="cooling"`)
	p.Counter(metricNamePrefix+"accounts", int64(disabled), `state="disabled"`)
	p.Counter(metricNamePrefix+"accounts", int64(inFlightFull), `state="in_flight_full"`)
	p.Help(metricNamePrefix+"accounts_in_flight", "Total in-flight requests across accounts.").
		Type(metricNamePrefix+"accounts_in_flight", "gauge")
	p.Counter(metricNamePrefix+"accounts_in_flight", h.cfg.Pool.InFlightTotal())
	p.Help(metricNamePrefix+"global_in_flight", "Gateway-wide in-flight requests (admission control gauge).").
		Type(metricNamePrefix+"global_in_flight", "gauge")
	p.Counter(metricNamePrefix+"global_in_flight", h.cfg.Pool.GlobalInFlight())

	// ---- 未消解未知（AGENTS.md「Unknown 消解纪律」）----
	// 这个 gauge 直接回答"还有多少事没摸清"——它**持续不为零**本身就是一个信号：
	// 要么池子在扩（新号/新模型），要么某些未知永远消解不掉（requires 设计有问题）。
	p.Help(metricNamePrefix+"unknowns_open", "Unresolved unknowns being tracked (e.g. cost tier of an account+model pair).").
		Type(metricNamePrefix+"unknowns_open", "gauge")
	p.Counter(metricNamePrefix+"unknowns_open", int64(h.cfg.Pool.OpenUnknownCount()))

	// ---- token 用量（usage 聚合桶；未启用时缺省 0）----
	if agg := h.usageTotals(); agg != nil {
		p.Help(metricNamePrefix+"tokens_total", "Accumulated tokens by kind.").
			Type(metricNamePrefix+"tokens_total", "counter")
		p.Counter(metricNamePrefix+"tokens_total", agg.PromptTokens, `kind="prompt"`)
		p.Counter(metricNamePrefix+"tokens_total", agg.CompletionTok, `kind="completion"`)
		p.Counter(metricNamePrefix+"tokens_total", agg.TotalTokens, `kind="total"`)
		p.Help(metricNamePrefix+"tokens_cache_hit_total", "Accumulated prompt-cache hit tokens.").
			Type(metricNamePrefix+"tokens_cache_hit_total", "counter")
		p.Counter(metricNamePrefix+"tokens_cache_hit_total", agg.CacheHitTokens)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = p.WriteTo(w)
}

// reqlogSnapshot 取 reqlog 快照；RequestLog 未启用时返回 nil（指标缺省 0）。
func (h *Handler) reqlogSnapshot() *reqlogSnapshot {
	if h.cfg.RequestLog == nil {
		return nil
	}
	s := h.cfg.RequestLog.Snapshot()
	return &reqlogSnapshot{
		Completed:     s.Completed,
		Succeeded:     s.Succeeded,
		Failed:        s.Failed,
		InFlight:      s.InFlight,
		SuccessRate:   s.SuccessRate,
		AvgDurationMs: s.AvgDurationMs,
	}
}

// reqlogSnapshot 是 /metrics 需要的 reqlog 字段子集（解耦：metrics 端点不依赖
// 面板侧 Snapshot 的完整结构，只取这几个数）。
type reqlogSnapshot struct {
	Completed, Succeeded, Failed, InFlight int64
	SuccessRate, AvgDurationMs             float64
}

// usageTotals 取 usage 全量累计（未启用时 nil）。
// Window{} 零值 = 不设时间界（全部历史），与面板「全部」口径一致。
func (h *Handler) usageTotals() *usageAgg {
	if h.cfg.Usage == nil {
		return nil
	}
	s := h.cfg.Usage.SnapshotWindow(usage.Window{}, nil, nil, nil, nil)
	return &usageAgg{
		PromptTokens:   s.Totals.PromptTokens,
		CompletionTok:  s.Totals.CompletionTok,
		TotalTokens:    s.Totals.TotalTokens,
		CacheHitTokens: s.Totals.CacheHitTokens,
	}
}

type usageAgg struct {
	PromptTokens, CompletionTok, TotalTokens, CacheHitTokens int64
}

// labelEscaper 标签值转义表（Prometheus 标签值规范：反斜杠、双引号、换行必须转义）。
// 单例构造：Replacer 内部有状态机，重复构造是纯浪费。
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// escapeLabel 转义标签值里的引号、反斜杠与换行。
// 模型名理论上不会含这些字符，但**伪造 model 名打请求是真实攻击面**——
// 不做转义会让暴露文本畸形（甚至注入假指标行，污染抓取端的时序数据）。
func escapeLabel(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	return labelEscaper.Replace(s)
}

// formatInt 本地小工具（避免在 server 包里再引 strconv 只为格式化整数）。
func formatInt(v int64) string { return strconv.FormatInt(v, 10) }
