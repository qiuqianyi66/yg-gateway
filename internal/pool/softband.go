// softband.go credit_floor 软带（REQ-routing-improvements P1，magpie 实跑研究落地）。
//
// 悬崖问题（REQ 必要性）：floor 硬拦让账号从「可选」瞬间变「不可选」，多号触底时
// 流量集中到少数号。magpie smart 的三档分层证明「软降权」是更平滑的答案；YG 的
// 加权抽签体系里「软」的正确翻译 = 带内权重 ×0.5（所有候选仍会被抽到，只是概率降档）。
//
// 带宽定义（不拍倍率）：带内含义 =「还敢接 N 次收费请求」，N=2。带宽 =
// max(floor×0.2, 2×该号实测每 1k token 扣费×20k token 典型请求规模)；无实测台账
// 时退化到 floor×0.2。20k/请求是显式假设（注释+REQ 声明），真机分布后校准。
package pool

import (
	"sort"
	"time"
)

const (
	// softBandFactor 带内权重系数（×0.5：加权体系对「排到队尾」的翻译）。
	softBandFactor = 0.5
	// softBandMinMult 无台账时的最小带宽倍率（floor×0.2）。
	softBandMinMult = 1.2
	// softBandMaxWidth 带宽硬上限（floor 的 50%）：软带是缓冲坡道不是跑道——
	// 超宽带会让「软降权」变成事实上的第二档正常池，失去保底语义。同时它让
	// softBandFactorOf 的热路径预筛成立：credits > floor×1.5 恒带外，昂贵的
	// 台账中位数计算只对 (floor, floor×1.5] 的真正近底线号执行（独立自查 🟡：
	// 原实现对该声称不成立——中位数算在所有健康候选的热路径上）。
	softBandMaxWidthMult = 0.5
	// softBandTypicalTokens 一次典型重度请求的 token 规模（带宽估算用，显式假设）。
	softBandTypicalTokens = 20_000.0
)

// softBandUpper 返回软带上界。creditFloor<=0（保底关闭）时返回 0（带不存在）。
// 调用方须已持 p.mu（读 e.modelCost）。仅在预筛通过（credits ≤ floor×1.5）后调用。
func (p *Pool) softBandUpper(e *entry, now time.Time) int64 {
	if p.creditFloor <= 0 {
		return 0
	}
	base := int64(float64(p.creditFloor) * softBandMinMult)
	// 实测台账有新鲜扣费时按「2 次典型请求」定带宽；取该号全部模型 CostPer1k 的
	// 中位数（少量样本比均值稳健）。
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
	if cap := p.creditFloor + int64(float64(p.creditFloor)*softBandMaxWidthMult); base > cap {
		return cap
	}
	return base
}

// softBandFactorOf 报告账号是否处于软带 (floor, upper] 内及权重系数。
// credits ≤ floor 由既有 floorBlocked 硬拦（本函数不重复判定），带外返回 (1,false)。
// 热路径口径：先做一行预筛（credits > floor×1.5 恒带外——带宽被 cap 在 floor×0.5），
// 中位数计算只对预筛通过的近底线号执行。
func (p *Pool) softBandFactorOf(e *entry, now time.Time) (float64, bool) {
	if p.creditFloor <= 0 || e.credits <= p.creditFloor {
		return 1, false
	}
	if e.credits > p.creditFloor+int64(float64(p.creditFloor)*(softBandMaxWidthMult+0.01)) {
		return 1, false // 预筛：物理上不可能带内（带宽 cap = floor×0.5）
	}
	if upper := p.softBandUpper(e, now); e.credits <= upper {
		return softBandFactor, true
	}
	return 1, false
}
