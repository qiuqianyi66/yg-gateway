// pace.go pace 选号模式（F9，REQ-pace-pick-2026-10-28）。
//
// 语义（magpie 源码级研读 §五①）：pace = 该号最早到期批次剩余额度 ÷ 距到期小时数
// ——「每小时流失风险」。开启时用池级 medianPace 作基准，把快过期加成从「常量 ×2」
// 平滑为「按流失风险分档」：低于中位一半以下不加成、高于中位线性加到 ×2 封顶。
// 默认关（SetPaceMode false）= 恒走 expiringVirtualSlots 常量，与改动前逐位一致。
package pool

import (
	"sort"
	"time"
)

// pace 因子常量（F9）：
//
//	paceBoostFloor = 0.5  // pace/medianPace 低于 0.5 → 无加成（风险不足半中位）
//	paceMaxBoost   = 1.0  // 加成封顶：因子 ≤ 1+1 = 2，与原 expiringVirtualSlots=2 上限对齐
const (
	paceBoostFloor = 0.5
	paceMaxBoost   = 1.0
)

// SetPaceMode 开关 pace 选号（默认关 = 零回归；开启时 expiring 账号的 ×2 常量
// 加成换成按池级中位 pace 平滑分档）。
func (p *Pool) SetPaceMode(enabled bool) {
	p.mu.Lock()
	p.paceEnabled = enabled
	p.mu.Unlock()
}

// poolMedianPace expiring 候选的 pace 下中位（同 poolMedianLatencyMs 口径）：
// 低于 2 个样本返回 0（因子回退常量，稀疏场景零变化）。
func (p *Pool) poolMedianPace(cands []*entry, now time.Time) float64 {
	paces := make([]float64, 0, len(cands))
	for _, e := range cands {
		if !expiringNow(e, now) {
			continue
		}
		paces = append(paces, paceOf(e, now))
	}
	if len(paces) < 2 {
		return 0
	}
	sort.Float64s(paces)
	return paces[(len(paces)-1)/2] // 下中位：比上中位更保守地认定「高流失风险」
}

// paceOf 单个账号的 pace：最早到期批次剩余额度 ÷ 距到期小时数（<1h 按 1h 计，
// 防除零；到期前最后一小时风险最大，按 1h 计不无限放大）。
func paceOf(e *entry, now time.Time) float64 {
	hours := e.creditsEarliestExpiry.Sub(now).Hours()
	if hours < 1 {
		hours = 1
	}
	return float64(e.creditsEarliestRemaining) / hours
}

// paceFactorOf 计算 pace 加成因子：1 + clamp(pace/medianPace − 0.5, 0, 1)，
// 范围 [1, 2]，关于 pace 单调不减。medianPace<=0（无池级基准）回退原常量
// expiringVirtualSlots（稀疏场景零变化）。
func paceFactorOf(e *entry, now time.Time, medianPace float64) float64 {
	if medianPace <= 0 {
		return expiringVirtualSlots
	}
	ratio := paceOf(e, now) / medianPace
	boost := ratio - paceBoostFloor
	if boost < 0 {
		boost = 0
	}
	if boost > paceMaxBoost {
		boost = paceMaxBoost
	}
	return 1 + boost
}
