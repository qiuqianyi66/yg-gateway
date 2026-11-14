// status.go /status 只读状态端点的输出视图（P0-2 起自 handler.go 拆出，
// 与 chatCompletions 等请求处理路径分离：本文件只做「读池 + 读观测器 → 组装 JSON」）。
package server

import (
	"net/http"
	"sort"

	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/upstream"
)

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	total, healthy, cooling, disabled, inFlightFull := h.cfg.Pool.CountsDetailed()
	sticky := 0
	if h.cfg.StickyCount != nil {
		sticky = h.cfg.StickyCount()
	}
	redisMode := h.cfg.RedisMode
	if redisMode == "" {
		redisMode = "noop"
	}
	// cost_explore 探索台账（issue #136 §5 可观测性）：累计探索事件数 + 各
	// (域, 模型) 的最近探索时刻（键 "realm|model"）。与 accounts[].model_costs
	// 行对照即可读出「探索→毕业」全链路（单一事实来源，不做双表示）。零回归只增键。
	exploreEvents, exploreLast := h.cfg.Pool.CostExploreStatus()
	body := map[string]any{
		"accounts": h.cfg.Pool.List(),
		// proxy_status 代理生效观测（P0-2，magpie REA 第④节落地）：per-代理（脱敏 URL）
		// → "active" / "failed"。与 accounts[].proxy_url 对照：账号配了代理但对应键是
		// failed = 该号出站必失败（fail-closed，请求持续换号）——运维一眼看出「代理没
		// 生效」，而不是误判成账号本身的问题。未配置代理（直连）不出现；键恒输出
		// （零回归只增键；未装配/零登记时 {}）。
		"proxy_status":   h.proxyStatusSnapshot(),
		"total":          total,
		"healthy":        healthy,
		"cooling":        cooling,
		"disabled":       disabled,
		"in_flight_full": inFlightFull,
		// realm_totals 按域分组的计数汇总（双 realm 并存时运维一眼看到各域可用性）：
		// 只新增字段，既有 total/healthy/cooling/disabled/in_flight_full 汇总键不变（零回归）。
		"realm_totals": map[string]map[string]int{
			"cn":     countsMapFrom(h.cfg.Pool.CountsDetailedForRealm("cn")),
			"global": countsMapFrom(h.cfg.Pool.CountsDetailedForRealm("global")),
		},
		"sticky_sessions": sticky,
		"redis_mode":      redisMode,
		// model_locks 当前有未过期模型级限流的 (域, 模型) 全清单：账号池视图回答
		// 「哪些号不能用」，本键回答「哪些模型不能用、锁了几个号、还要锁多久」。
		// 与 ModelBlocked（请求失败时的单模型判定）互补；无锁时为 null。零回归只增键。
		"model_locks": h.cfg.Pool.ModelLockView(),
		// credit_floor 生效的积分保底值（0 = 关闭）。与 accounts[].credits +
		// model_costs 对照即可判定「某号为何对某模型不出票」。零值也显式写出
		// （运维口径：缺失会让人误以为没记录）。
		"credit_floor": h.cfg.Pool.CreditFloor(),
		// cost_explore 事件与 per-model 时间戳（时间值由 encoding/json 写 RFC3339）。
		"cost_explore": map[string]any{
			"events_total": exploreEvents,
			"per_model":    exploreLast,
		},
		// latency 延迟分位数（ADR-003 D-403）：全局 + 按模型的 p50/p90/p95/p99。
		// 补齐"只有均值、看不见长尾"的观测缺口——均值会被大量快请求稀释，
		// 而网关的体验痛点恰在长尾。未启用观测时为 null（零回归只增键）。
		"latency": h.latencySnapshot(),
		// global_in_flight 全局在途计数（ADR-003 D-401）：与 gateway.max_in_flight
		// 对照可判断是否触顶（触顶时新请求收 429）。
		"global_in_flight": h.cfg.Pool.GlobalInFlight(),
		// rhythm 并发闸观测（F1，magpie REA §2.5 节奏面收口）：每账号 {in_flight,
		// queue_depth}——与 concurrency_per_account 对照即可看出「某号现在几个在途、
		// 几个排队、离上限多远」。magpie 明确没有 per-账号在途观测（REA §2.7 自承
		// unknown），本键是 yg 可审计定位的增量。零回归只增键；未装配/零配置时 {}。
		"rhythm": h.rhythmSnapshot(),
		// sticky_paid_hits 粘性命中收费号且池里有免费/未知号的累计次数
		// （ADR-004 遗留轻量版）。纯观测，不自动换号。
		"sticky_paid_hits": h.cfg.Pool.StickyPaidHits(),
	}

	// providers 多 provider 汇总（F7）：注册表元数据 + 各 provider 账号数。
	// 空/未装配 = 仅 codebuddy（键省略，零回归）。
	if ps := h.providerSummary(); ps != nil {
		body["providers"] = ps
	}

	// task_metrics F5 度量基线（REQ-f5-metric-counter）：把首调用上下文与总 token 从
	// unknown 变成 observation。仅当观测到至少一个会话的 usage 时才加入响应——否则
	// 整体省略该键（unknown，不输出 0、不输出 false，与本项目「字段缺失=未知」纪律一致）。
	// first_call_prompt_tokens 仅在该会话确实观测到首轮 prompt_tokens 时输出，否则省略。
	if tmView, ok := upstream.TaskMetrics.Snapshot(); ok {
		tm := map[string]any{
			"tasks":             tmView.Tasks,
			"task_total_tokens": int64(tmView.TaskTotalTokens),
			"task_turns":        tmView.TaskTurns,
		}
		if tmView.FirstCallKnown {
			tm["first_call_prompt_tokens"] = int64(tmView.FirstCallPromptTokens)
		}
		body["task_metrics"] = tm
	}

	// protocol_drift 上游协议漂移观测（R-102）：模块未启用（Drift=nil）时整体省略
	// 该键——键缺失 = 未观测（四态 unknown 的外层表达），不是「无漂移」。模块启用后，
	// 观测器内部再区分 unknown（未播种）/ ok / drift：禁止用键省略当「无漂移」读。
	if h.cfg.Drift != nil {
		body["protocol_drift"] = h.cfg.Drift.Status()
	}

	// agent_profile（V3-10）：从 reqlog archive 读最近 1000 条 chat 事件，按
	// session_fp / agent_kind 聚合。零条事件（或事件里没有任何 session_fp /
	// agent_kind 信号）时整块省略——与 task_metrics / protocol_drift 同源，
	// 键缺失 = 未观测，不是「没有 agent 流量」。
	if prof := h.agentProfileSnapshot(); prof != nil {
		body["agent_profile"] = prof
	}

	// guardrail_stats 上下文护栏命中（REQ-context-guardrail-2026-10-31 / ADR-019）：
	// 无任何命中时整块省略（与 agent_profile / task_metrics 同源，键缺失 = 未观测，
	// 不是「零命中」）。估算 vs 上游对照样本（Calibration）随块透出供权重校准。
	if h.guardrail != nil {
		if gs := h.guardrail.Snapshot(); gs.WarnHits+gs.BlockedHits+gs.SessionRPMHits > 0 {
			body["guardrail_stats"] = gs
		}
	}

	writeJSON(w, http.StatusOK, body)
}

// providerSummary 多 provider 汇总（F7 /status providers[]）：注册表元数据 + 各
// provider 账号数（从 pool.List() 的 provider 字段统计，含 codebuddy）。未装配任何
// provider（Providers 空）返回 nil → 键省略 = 仅 codebuddy（零回归）。输出按
// providerID 稳定排序（map 无序，不排序会抖动）。
func (h *Handler) providerSummary() []map[string]any {
	if len(h.cfg.Providers) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, a := range h.cfg.Pool.List() {
		if a.Provider != "" {
			counts[a.Provider]++
		} else {
			counts["codebuddy"]++
		}
	}
	ids := make([]string, 0, len(h.cfg.Providers))
	for id := range h.cfg.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		ep := h.cfg.Providers[id]
		out = append(out, map[string]any{
			"id":            id,
			"kind":          ep.Kind,
			"quota":         ep.Quota,
			"enabled":       ep.Enabled,
			"models":        ep.Models,
			"account_count": counts[id],
		})
	}
	return out
}

// proxyStatusSnapshot 返回 upstream 代理生效状态视图（/status 的 proxy_status 键）。
// Upstream 未装配（测试桩 / 降级形态）时返回空 map——键仍存在（{}），
// 与「零登记 = 全部直连」的语义一致，避免消费方把键缺失读成「无此观测」。
func (h *Handler) proxyStatusSnapshot() map[string]string {
	if h.cfg.Upstream == nil {
		return map[string]string{}
	}
	return h.cfg.Upstream.ProxyStatusSnapshot()
}

// rhythmSnapshot 返回并发闸观测视图（/status 的 rhythm 键）。Upstream 未装配时
// 返回空表（键仍存在，{}）——与 proxy_status 同语义，避免键缺失被读成「无此观测」。
//
// 桶名（上游 X-Machine-ID，36 hex）在这里换成「昵称(uid8)」：运维要判断「哪个号被打热 /
// 被排队」，看哈希只能数个数。分桶语义不动（上游仍按 machine-id 计），只改展示名；
// 未匹配到账号的桶（global / 无账号归属的请求）原样保留——未知就是未知，不隐藏不合并。
func (h *Handler) rhythmSnapshot() map[string]any {
	if h.cfg.Upstream == nil {
		return map[string]any{}
	}
	raw := h.cfg.Upstream.RhythmStatus()
	if len(raw) == 0 || h.cfg.Pool == nil {
		return raw
	}
	return rhythmRelabel(raw, h.cfg.Pool.List())
}

// rhythmRelabel 按账号表把 rhythm 桶名换成可读标签；纯函数便于单测。
func rhythmRelabel(raw map[string]any, accounts []pool.Status) map[string]any {
	labels := make(map[string]string, len(accounts))
	for _, a := range accounts {
		mid := upstream.MachineIDForUID(a.UID)
		if mid == "" {
			continue
		}
		label := shortUID(a.UID)
		if a.Nickname != "" {
			label = a.Nickname + "(" + shortUID(a.UID) + ")"
		}
		if _, dup := labels[mid]; !dup { // 同一 machine-id 只认首个（uid 互异时应无此情）
			labels[mid] = label
		}
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if lb, ok := labels[k]; ok {
			out[lb] = v
			continue
		}
		out[k] = v
	}
	return out
}

// shortUID 取 uid 前 8 位（与请求日志/stdout 的显示口径一致）。
func shortUID(uid string) string {
	if len(uid) <= 8 {
		return uid
	}
	return uid[:8]
}

// agentProfileSnapshot 读 archive 并聚合 agent 流量画像。无数据（无 RequestLog、
// archive 关闭、零条 chat 事件、事件里既无 session_fp 也无 agent_kind）时返回 nil。
func (h *Handler) agentProfileSnapshot() any {
	if h.cfg.RequestLog == nil {
		return nil
	}
	events, err := h.cfg.RequestLog.ReadArchive(1000, reqlog.Filter{Kind: reqlog.KindChat})
	if err != nil || len(events) == 0 {
		return nil
	}
	// 整块省略条件：所有事件都没打过 session_fp 也没打过 agent_kind（V3-10 之前
	// 的归档行、或开关全关的历史窗口）——聚合出来全是 unknown 桶空榜，不如不输出。
	hasSignal := false
	for i := range events {
		if events[i].SessionFP != "" || events[i].AgentKind != "" {
			hasSignal = true
			break
		}
	}
	if !hasSignal {
		return nil
	}
	return reqlog.AggregateProfile(events)
}

// latencySnapshot 返回延迟分位数快照；未启用观测（Latency=nil）时返回 nil。
func (h *Handler) latencySnapshot() any {
	if h.cfg.Latency == nil {
		return nil
	}
	return h.cfg.Latency.Snapshot()
}

// countsMapFrom 把 CountsDetailed 五元组打包成 /status 的域分组建模。
func countsMapFrom(total, healthy, cooling, disabled, inFlightFull int) map[string]int {
	return map[string]int{
		"total":          total,
		"healthy":        healthy,
		"cooling":        cooling,
		"disabled":       disabled,
		"in_flight_full": inFlightFull,
	}
}
