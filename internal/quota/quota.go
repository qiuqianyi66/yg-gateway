// Package quota 定义**非 credits 类**的额度语义（ADR-026 的 Quota 层）。
//
// 为什么不含 credits：CodeBuddy 的积分 / 到期批次 / tier 成本留在 internal/pool，
// 本包不搬家——否则会引入 quota -> pool 的反向依赖，破坏依赖方向（check-boundaries
// 默认拒绝）。本包只承载 metered（按量计费）与 window（订阅滚动窗口）两族。
//
// 四态纪律：Remaining 的 unknown=true 表示「不知道」，**不是**「0」。消费方必须按
// unknown 处理；Exhausted 是肯定断言，unknown 时不得返回 true（「未知不是耗尽」，
// 与「缺失不是没有」同源）。
package quota

import "yg-gateway/internal/provider"

// State 一个账号的额度状态。只回答「剩多少、是否耗尽、可不可信」。
type State interface {
	// Kind 额度语义族（metered / window）。credits 不在此实现。
	Kind() provider.QuotaKind
	// Remaining 返回剩余额度。unknown=true 表示无法确定（未配置 / 查询失败）。
	Remaining() (value int64, unknown bool)
	// Exhausted 是否已耗尽。unknown 时返回 false——宁可继续选、不误杀。
	Exhausted() bool
}

// Metered 按量计费额度（Ark / OpenAI / DeepSeek 等 API 账号）。
type Metered struct {
	// Balance 当前余额；<0 = unknown（未配置或查询失败）。
	Balance int64
	// FreeRemaining 免费额度剩余；<0 = unknown。
	FreeRemaining int64
}

func (m Metered) Kind() provider.QuotaKind { return provider.QuotaMetered }

func (m Metered) Remaining() (int64, bool) {
	// 任一维度未知 → 整体 unknown：不拼凑半个数（余额已知、免费额度未知时，
	// 求和会给出一个「看似精确实则少算」的值，比 unknown 更危险）。
	if m.Balance < 0 || m.FreeRemaining < 0 {
		return 0, true
	}
	return m.Balance + m.FreeRemaining, false
}

func (m Metered) Exhausted() bool {
	v, unknown := m.Remaining()
	return !unknown && v <= 0
}

// Window 滚动窗口额度（订阅型账号，如 5 小时 / 每周重置）。P2 才接线，本批只定形状。
type Window struct {
	// RemainingInWindow 当前窗口剩余；<0 = unknown。
	RemainingInWindow int64
	// ResetAt 窗口重置时刻（Unix 秒）；0 = unknown。
	ResetAt int64
}

func (w Window) Kind() provider.QuotaKind { return provider.QuotaWindow }

func (w Window) Remaining() (int64, bool) {
	if w.RemainingInWindow < 0 {
		return 0, true
	}
	return w.RemainingInWindow, false
}

func (w Window) Exhausted() bool {
	v, unknown := w.Remaining()
	return !unknown && v <= 0
}
