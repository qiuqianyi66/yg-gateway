// guardrail.go 上下文护栏（REQ-context-guardrail-2026-10-31 / ADR-019）。
//
// 为什么需要：实弹观测（2026-10-29~31 request-logs）单会话 prompt 无界增长是常态
// （30 万+ 占 53–97.7%，最大 82.8 万），多会话并发放大「每请求重算全量历史」成本——
// 费用爆炸、TTFB 恶化、上游 context canceled、客户端 agent-busy、图片识别失败。
// 模型与 max_tokens 只是触发器，根因是使用模式；服务端护栏是根本治理的一部分
// （客户端侧会话治理属另一切片）。
//
// 口径（诚实声明，四态纪律）：估算值 = derivation（estimateTokensJSON 启发式：CJK
// 计 1、其余 1/4 向上取整，非上游 usage 精确值）；「命中阈值」是 observation。
// 拒绝/告警必须发生在派发上游之前，而上游 usage 请求后才可得——这是用估算的唯一
// 原因。估算 vs 上游对照样本经 /status 暴露供校准（RecordCalibration）。
//
// 与 keyRPM 同族：自写无依赖、固定窗口、先 record 后 allow、0 = 关零回归。
// 热生效：Set* 在锁内同步，main 装配后保留引用即可接 reload。
package server

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"yg-gateway/internal/session"
)

// GuardrailResult 一次上下文护栏判定的结果。
type GuardrailResult int

const (
	// GuardrailPass 未超任何阈值，放行。
	GuardrailPass GuardrailResult = iota
	// GuardrailWarn 超 context_warn_tokens：打标 + 响应头，不阻断。
	GuardrailWarn
	// GuardrailBlocked 超 context_max_tokens：413 拒绝。
	GuardrailBlocked
)

// GuardrailDecision 一次护栏判定的完整结果（handler 据此写响应）。
type GuardrailDecision struct {
	Result      GuardrailResult
	RateLimited bool  // 会话限流拒绝（429）；与 Result 独立（限流与规模是两个维度）
	EstTokens   int64 // 估算 prompt token（derivation）
	WarnTokens  int64 // 生效的 warn 阈值（0 = 关）
	MaxTokens   int64 // 生效的 max 阈值（0 = 关）
}

// guardrailSessionWindow 单会话固定窗口（sessKey → 1 分钟）。
type guardrailSessionWindow struct {
	count int
	start time.Time
}

// GuardrailSample 估算 vs 上游 usage 对照样本（derivation 校准，/status 暴露）。
type GuardrailSample struct {
	Est      int64 `json:"est"`
	Upstream int64 `json:"upstream"`
}

// GuardrailStats /status 聚合视图。无命中时整块省略（unknown 表达，与 agent_profile
// 同源——缺失不是「零命中」，是「无观测」，实现侧由 handler 判断省略）。
type GuardrailStats struct {
	WarnHits       int64             `json:"warn_hits,omitempty"`
	BlockedHits    int64             `json:"blocked_hits,omitempty"`
	SessionRPMHits int64             `json:"session_rpm_hits,omitempty"`
	LastHitAt      time.Time         `json:"last_hit_at,omitempty"`
	Calibration    []GuardrailSample `json:"calibration_samples,omitempty"`
}

// guardrailCalibrationMax 对照样本上限（FIFO，防无限增长）。
const guardrailCalibrationMax = 20

// guardrailSessionMax 会话窗口表惰性 GC 阈值（同 keyRPM：超阈值清理长期无活动窗口）。
const guardrailSessionMax = 128

// Guardrail 上下文护栏状态。零值不可用，用 NewGuardrail 构造。
type Guardrail struct {
	mu sync.Mutex

	warnTokens int64 // 0 = 关
	maxTokens  int64 // 0 = 关
	sessionRPM int   // 0 = 关

	sessions map[string]*guardrailSessionWindow // sessKey → 窗口；散客（空键）不计
	stats    GuardrailStats
	now      func() time.Time
}

// NewGuardrail 构造护栏（全 0 = 全关 = 零回归）。
func NewGuardrail() *Guardrail {
	return &Guardrail{
		sessions: make(map[string]*guardrailSessionWindow),
		now:      time.Now,
	}
}

// SetContextWarnTokens 热生效设置 warn 阈值（0 = 关）。
func (g *Guardrail) SetContextWarnTokens(n int64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.warnTokens = n
}

// SetContextMaxTokens 热生效设置 max 阈值（0 = 关）。
func (g *Guardrail) SetContextMaxTokens(n int64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.maxTokens = n
}

// SetSessionRPM 热生效设置会话级 RPM（0 = 关）。关闭/降档时清空窗口表（内存回收）。
func (g *Guardrail) SetSessionRPM(n int) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessionRPM = n
	if n <= 0 {
		g.sessions = make(map[string]*guardrailSessionWindow)
	}
}

// Check 对单个请求执行护栏判定。sessKey 为空 = 散客：不参与会话限流。
// 幂等语义：每个实际请求恰好调用一次（先 record 后 allow，与 keyRPM 一致——
// 被拒的那次也计入窗口，统计的是真实速率）。
func (g *Guardrail) Check(sessKey string, est int64) GuardrailDecision {
	if g == nil {
		return GuardrailDecision{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	d := GuardrailDecision{EstTokens: est, WarnTokens: g.warnTokens, MaxTokens: g.maxTokens}

	if g.warnTokens > 0 && est >= g.warnTokens {
		d.Result = GuardrailWarn
	}
	if g.maxTokens > 0 && est >= g.maxTokens {
		d.Result = GuardrailBlocked
	}
	switch d.Result {
	case GuardrailWarn:
		g.stats.WarnHits++
		g.stats.LastHitAt = now
	case GuardrailBlocked:
		g.stats.BlockedHits++
		g.stats.LastHitAt = now
	}

	if g.sessionRPM > 0 && sessKey != "" {
		w, ok := g.sessions[sessKey]
		if !ok || now.Sub(w.start) >= time.Minute {
			// 窗口翻转（惰性：旧桶原地换新，不做全表遍历清理）
			w = &guardrailSessionWindow{count: 0, start: now}
			g.sessions[sessKey] = w
			g.sweepLocked(now)
		}
		w.count++
		if w.count > g.sessionRPM {
			d.RateLimited = true
			g.stats.SessionRPMHits++
			g.stats.LastHitAt = now
		}
	}
	return d
}

// sweepLocked 惰性 GC：窗口表超过阈值时清理超 1 分钟的陈旧键（同 keyRPM 策略）。
// 调用方须持 g.mu。
func (g *Guardrail) sweepLocked(now time.Time) {
	if len(g.sessions) <= guardrailSessionMax {
		return
	}
	for k, w := range g.sessions {
		if now.Sub(w.start) >= time.Minute {
			delete(g.sessions, k)
		}
	}
}

// RecordCalibration 追加估算 vs 上游实际 usage 对照样本（≤20 条 FIFO）。
// 仅当上游确实返回了 prompt_tokens 时调用（unknown 不冒充 0）。
func (g *Guardrail) RecordCalibration(est, upstream int64) {
	if g == nil || upstream <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.Calibration = append(g.stats.Calibration, GuardrailSample{Est: est, Upstream: upstream})
	if len(g.stats.Calibration) > guardrailCalibrationMax {
		g.stats.Calibration = g.stats.Calibration[len(g.stats.Calibration)-guardrailCalibrationMax:]
	}
}

// Snapshot 返回 /status 用的聚合视图（锁内拷贝，调用方不可改内部状态）。
func (g *Guardrail) Snapshot() GuardrailStats {
	if g == nil {
		return GuardrailStats{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := g.stats
	cp.Calibration = append([]GuardrailSample(nil), g.stats.Calibration...)
	return cp
}

// applyGuardrail 在 chatCompletions 阶段②（deriveSessionKeys）之后、阶段③（提示词
// 改写）之前执行上下文护栏判定；返回 true = 已写响应（413/429），调用方须 return。
//
// 估算口径：三段（system/messages/tools 的字符串叶子，estimateTokensJSON 启发式），
// 与 /v1/messages/count_tokens（V3-1）完全同源；OpenAI（system 在 messages[0]）与
// Anthropic（顶层 system 键）两种形状均覆盖。估算值 = derivation，非上游 usage 精确
// 值——拒绝/告警必须发生在派发上游之前，这是用估算的唯一原因（ADR-019 D1）。
func (h *Handler) applyGuardrail(w http.ResponseWriter, r *http.Request, sessKey string, reqView *session.RequestView, run *chatRun) (handled bool) {
	if h.guardrail == nil {
		return false
	}
	est := int64(estimateTokensJSON(reqView.Raw("system")) +
		estimateTokensJSON(reqView.Raw("messages")) +
		estimateTokensJSON(reqView.Raw("tools")))
	d := h.guardrail.Check(sessKey, est)
	switch {
	case d.RateLimited:
		// 会话级限流（ADR-019 D4）：429 与 server_busy 语义分离——这是会话自身速率，
		// 非服务繁忙；客户端按 Retry-After 退避。
		w.Header().Set("Retry-After", "60")
		writeOpenAIError(w, http.StatusTooManyRequests, "session_rate_limit",
			"session request rate exceeded; retry shortly")
		if tr := requestTraceFrom(r); tr != nil {
			tr.setGuardrailHit("session_rpm")
		}
		return true
	case d.Result == GuardrailBlocked:
		// 硬拒（ADR-019 D3）：413 与 MaxBodyBytes 同族（实体过长），code 语义明确，
		// 不排队、不降级、不选号——拒绝要便宜。
		w.Header().Set("Retry-After", "60")
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "context_length_exceeded",
			fmt.Sprintf("estimated prompt tokens %d exceed guardrail max %d; reduce conversation context or start a new session",
				est, d.MaxTokens))
		if tr := requestTraceFrom(r); tr != nil {
			tr.setGuardrailHit("blocked")
		}
		return true
	case d.Result == GuardrailWarn:
		// 软告警（ADR-019 D2）：不阻断；响应头让客户端知情，reqlog 打标供面板聚合，
		// webhook 提醒由告警层按 key+1 分钟去重（D5），不在此处发。
		w.Header().Set("X-Gateway-Warning",
			fmt.Sprintf("estimated prompt tokens %d exceed warn threshold %d", est, d.WarnTokens))
		if tr := requestTraceFrom(r); tr != nil {
			tr.setGuardrailHit("warn")
		}
		if run != nil {
			run.guardrailEst = est // 上游返回 usage 后补对照样本（RecordCalibration）
		}
		return false
	}
	return false
}
