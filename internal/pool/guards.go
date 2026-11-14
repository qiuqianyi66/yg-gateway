// guards.go 选号只读护栏的**单一实现**。
//
// 为什么单独成文件：pick（credits 加权随机）与 pickNonCredits（非 credits 顺序）需要
// 同一组护栏——realm/group 过滤、故障窗、主动 RPM 限流、熔断 half-open。精简前这两条
// 路径各写一份同名闭包，靠注释「与 pick 内同名闭包逐字对齐（勿单改，改一处必须同步
// 另一处）」约束——那是必然漂移的约束：注释本身已经不同步（pick 侧带 ADR 编号与边界
// 推导，pickNonCredits 侧只剩一行「逐字对齐」）。2026-11-14 合并为唯一实现点。
//
// 约束：guards 只读。调用方必须已持 p.mu 读锁；本文件不写 entry 字段（写回仍由
// 选中方在 pick 内完成）。
package pool

import "time"

// pickGuards 一次选号用到的只读判定集合。now 在构造时固定，保证同一次选号内
// 所有判定看到同一时刻（否则跨判定边界会出现「刚到期」与「未到期」的撕裂）。
type pickGuards struct {
	p     *Pool
	now   time.Time
	realm string
	group string
}

// guardsAt 构造护栏集合（realm/group 为空表示不限，零回归）。
func (p *Pool) guardsAt(now time.Time, realm, group string) pickGuards {
	return pickGuards{p: p, now: now, realm: realm, group: group}
}

// realmOK 分池过滤（P1-B：账号池分组）。group 空 = 不限池（零回归）。
func (g pickGuards) realmOK(e *entry) bool {
	if g.realm != "" && e.a.Realm() != g.realm {
		return false
	}
	if g.group != "" && e.a.Group != g.group {
		return false
	}
	return true
}

// failureWindowBlocked 故障窗（P0-2）：最近失败在窗内的账号临时退出 normal 候选，
// 防止抖动号被连续打。只挡 normal：全池皆窗内时走 pickEarliestExpiry 兜底（失败
// 成本仅一轮换）。healthy() 不加本判定——兜底/粘性路径复用 healthy()，加进去会让
// 全池窗内时兜底也失效（行为退化）。
func (g pickGuards) failureWindowBlocked(e *entry) bool {
	return g.p.failureWindow > 0 && !e.lastErr.IsZero() && g.now.Sub(e.lastErr) < g.p.failureWindow
}

// minIntervalBlocked 主动 RPM 限流（风控规避）：距离上次请求太近的账号跳过，避免
// 单账号短时间打太密触发上游风控（真实用户不会每秒发多次请求）。
// global 账号阈值更严（国际版风控更敏感）。0 = 关闭（零回归）。
func (g pickGuards) minIntervalBlocked(e *entry) bool {
	interval := g.p.minRequestInterval
	if e.a.IsGlobal() && g.p.minRequestIntervalGlobal > 0 {
		interval = g.p.minRequestIntervalGlobal
	}
	if interval <= 0 {
		return false
	}
	last := e.lastRequestAt.Load()
	if last == 0 {
		return false // 从未请求过，不拦
	}
	return g.now.Sub(time.Unix(0, last)) < interval
}

// breakerHalfOpen（ADR-003 D-400）：熔断号**刚到期**时进入 half-open 试探窗——每
// breakerProbeGap 至多放行 1 次，避免到期瞬间全量洪峰把坏号二次打挂（二次熔断会让
// 退避翻倍，最长 6h，代价远高于限速一轮）。返回 true = 本账号本轮应被跳过。
//
// 判定边界（为什么这样切）：
//   - breakerProbeGap <= 0 → 关闭，恒 false（零回归，与旧行为逐位一致）
//   - breakerUntil 为空 或 尚未到期 → 不受此限（未到期本来就不可选；空则从未熔断）
//   - retryCount == 0 → 从未熔断过或已被成功清理，无限速必要
//   - 距上次试探 < gap → 跳过（把机会让给别的号）
func (g pickGuards) breakerHalfOpen(e *entry) bool {
	if g.p.breakerProbeGap <= 0 {
		return false
	}
	if e.breakerUntil.IsZero() || g.now.Before(e.breakerUntil) {
		return false // 从未熔断 / 仍在熔断期（后者由 healthy 拦下）
	}
	if e.retryCount == 0 {
		return false // 已成功恢复（NoteSuccess 清 retryCount）
	}
	if t := e.probeAtTime(); t.IsZero() {
		return false // 首次到期：放行一次试探
	} else {
		return g.now.Sub(t) < g.p.breakerProbeGap
	}
}
