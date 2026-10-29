// softband.go credit_floor 软带（REQ-routing-improvements P1，magpie 实跑研究落地）。
//
// 悬崖问题（REQ 必要性）：floor 硬拦让账号从「可选」瞬间变「不可选」，多号触底时
// 流量集中到少数号。magpie smart 的三档分层证明「软降权」是更平滑的答案；YG 的
// 加权抽签体系里「软」的正确翻译 = 带内权重 ×0.5（所有候选仍会被抽到，只是概率降档）。
//
// 带宽定义（不拍倍率，PLAN-softband-recalibration 方案 A）：带内含义 =「还敢接 N 次
// 收费请求」，N=2。带宽 =
//   - 有扣费环（entry.recentCharges，实测单次扣费）时：floor + 2×P90(近 ≤32 条)。
//     为什么 P90 而非 P50：真机 reqlog 双峰分布（聊天 p50=762 token vs 评测
//     200k-500k token、~4.4 credit/次）下 P50 恒落聊天侧，对评测大额风险不可见；
//     P90 落在重流量段，带宽才覆盖「典型中的最重请求」（derivation，V3-3 回填）。
//   - 无环（重启冷启动窗口）时：回退 floor×0.2 / 2×该号实测每 1k token 扣费×20k
//     token 典型请求规模（迁移期公式，行为与现状一致）。20k 是显式假设（注释+REQ
//     声明），仅回退路径使用。
package pool

import (
	"math"
	"sort"
	"time"
)

const (
	// softBandFactor 带内权重系数（×0.5：加权体系对「排到队尾」的翻译）。
	softBandFactor = 0.5
	// softBandMinMult 无台账时的最小带宽倍率（floor×0.2）。
	softBandMinMult = 1.2
	// softBandMaxWidthMult 带宽硬上限（floor 的 50%）：软带是缓冲坡道不是跑道——
	// 超宽带会让「软降权」变成事实上的第二档正常池，失去保底语义。同时它让
	// softBandFactorOf 的热路径预筛成立：credits > floor×1.5 恒带外，昂贵的
	// 中位数计算只对 (floor, floor×1.5] 的真正近底线号执行（独立自查 🟡：
	// 原实现对该声称不成立——中位数算在所有健康候选的热路径上）。
	softBandMaxWidthMult = 0.5
	// softBandTypicalTokens 一次典型重度请求的 token 规模（**迁移期回退公式**的显式
	// 假设）。PLAN-softband-recalibration：真机双峰分布已推翻该假设（聊天 762 vs
	// 评测 30 万），回退公式仅服务重启后无扣费环的冷窗口；环一旦有样本即走 P90 口径。
	softBandTypicalTokens = 20_000.0
	// softBandChargeHistory 扣费环容量（近 N 次实测单次扣费，N=32）。
	// 窗口语义：覆盖约 1-2 天混合流量（评测 215 次/2 天 + 聊天 383 次/2 天），
	// P90 稳定后不随单次请求抖动。
	softBandChargeHistory = 32
)

// RequestClass 请求流量类（bandMult 分口径，PLAN §四 选项 A）。
// 双峰真机分布（derivation，48h reqlog）：聊天 ≤5k token、评测 200k-500k token，
// 5k-200k 之间无样本。阈值取深谷；样本外语义待真机复核（unknown）。
type RequestClass int

const (
	// ClassChat 聊天类：软带上界走聊天扣费环，无环回退 20k 公式（现状语义）。
	ClassChat RequestClass = iota
	// ClassBench 评测/大请求类：软带上界走评测扣费环（大额不再污染聊天带宽）。
	ClassBench
)

// BenchTokenThreshold 分口径阈值：单次请求 tokens > 此值归 ClassBench。
// 导出供 server 层在选号入口判类（estimateTokensJSON 估算）。
const BenchTokenThreshold = 50_000

// chargesOf 按流量类取扣费环（调用方须已持 p.mu）。
func (p *Pool) chargesOf(e *entry, class RequestClass) []float64 {
	if class == ClassBench {
		return e.recentBenchCharges
	}
	return e.recentCharges
}

// softBandP90 扣费环 P90（环非空时调用）。P90 取排序后 ceil(0.9n)-1 索引
// （保守取上分位；与全局延迟分位的分桶口径不同，这里样本少用精确排序）。
func softBandP90(charges []float64) float64 {
	n := len(charges)
	if n == 0 {
		return 0
	}
	sorted := make([]float64, n)
	copy(sorted, charges)
	sort.Float64s(sorted)
	idx := int(math.Ceil(0.9*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

// softBandUpper 返回软带上界。creditFloor<=0（保底关闭）时返回 0（带不存在）。
// 调用方须已持 p.mu（读 e.modelCost / 对应类扣费环）。仅在预筛通过（credits ≤ floor×1.5）后调用。
// class 决定取哪条扣费环（分口径，PLAN §四 选项 A）：评测大额扣费只抬评测带宽，
// 聊天带宽不被污染。
func (p *Pool) softBandUpper(e *entry, now time.Time, class RequestClass) int64 {
	if p.creditFloor <= 0 {
		return 0
	}
	base := int64(float64(p.creditFloor) * softBandMinMult)
	// 主路径（方案 A）：实测单次扣费环 P90。N=2 语义 = floor + 2×典型最重单次扣费。
	if charges := p.chargesOf(e, class); len(charges) > 0 {
		if v := p.creditFloor + int64(2*softBandP90(charges)); v > base {
			base = v
		}
	} else {
		// 迁移期回退：单价×20k 假设（与现状逐位一致，冷启动窗口）。
		var costs []float64
		for _, mc := range e.modelCost {
			if !mc.LastSeen.IsZero() && now.Sub(mc.LastSeen) <= modelCostTTL && mc.CostPer1k > 0 {
				costs = append(costs, mc.CostPer1k)
			}
		}
		if len(costs) > 0 {
			sort.Float64s(costs)
			median := costs[len(costs)/2]
			if v := p.creditFloor + int64(2*median/1000*softBandTypicalTokens); v > base {
				base = v
			}
		}
	}
	if cap := p.creditFloor + int64(float64(p.creditFloor)*softBandMaxWidthMult); base > cap {
		return cap
	}
	return base
}

// softBandFactorOf 报告账号是否处于软带 (floor, upper] 内及权重系数。
// credits ≤ floor 由既有 floorBlocked 硬拦（本函数不重复判定），带外返回 (1,false)。
// class 沿 softBandUpper 贯通（分口径）。
// 热路径口径：先做一行预筛（credits > floor×1.5 恒带外——带宽被 cap 在 floor×0.5），
// 中位数计算只对预筛通过的近底线号执行。
func (p *Pool) softBandFactorOf(e *entry, now time.Time, class RequestClass) (float64, bool) {
	if p.creditFloor <= 0 || e.credits <= p.creditFloor {
		return 1, false
	}
	if e.credits > p.creditFloor+int64(float64(p.creditFloor)*(softBandMaxWidthMult+0.01)) {
		return 1, false // 预筛：物理上不可能带内（带宽 cap = floor×0.5）
	}
	if upper := p.softBandUpper(e, now, class); e.credits <= upper {
		return softBandFactor, true
	}
	return 1, false
}
