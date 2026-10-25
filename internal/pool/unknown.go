// unknown.go 成本未知的登记与消解接入（AGENTS.md「Unknown 消解纪律」）。
//
// 为什么需要：某 (账号, 模型) 在第一次实测到 usage.credit 之前，它的成本是
// **未知**——而未知恰恰是养号决策最要紧的那部分。旧实现把它压成 tier 1（未知层）
// 就没人跟了：没人知道它何时能被消解、还差什么。tier 未知时 credit_floor 放行
// 收费模型，可能一笔打穿余额（真实事故形态见 kimi-k3-1 实案）。
//
// 本文件把「未知」变成可跟踪实体（internal/unknown.Registry）：
//   - TrackCostUnknown 在请求路径上登记（幂等去重，sync.Map 快路径）；
//   - NoteModelCost 实测到 credit 时自动消解——"消解只能由合格的观察证据完成"；
//   - Status 暴露未消解条目，面板/agent 可见。
//
// 全程可选：未调 SetUnknownRegistry 时所有方法 no-op（零回归）。
package pool

import (
	"fmt"
	"time"

	"yg-gateway/internal/unknown"
)

// SetUnknownRegistry 注入未知条目注册表（可选；nil = 关闭跟踪）。
// main 在装配期调用一次；运行期不再改动（与 SetEwma 同风格）。
func (p *Pool) SetUnknownRegistry(reg *unknown.Registry) {
	p.unknownReg = reg
}

// costUnknownKey 组合键（uid|model）。
func costUnknownKey(uid, model string) string { return uid + "|" + model }

// costUnknownQuestion 生成人可读的问题描述（面板直接展示）。
//
// 用 uid 前缀 8 位：与日志脱敏口径一致（R2），面板按前 8 位就能对上账号卡片。
func costUnknownQuestion(uid, model string) string {
	u := uid
	if len(u) > 8 {
		u = u[:8]
	}
	return fmt.Sprintf("账号 %s 对模型 %s 的实际成本未知（tier 未定）", u, model)
}

// costUnknownRequires 说明「需要什么才能消解」——这是登记时的必填项。
const costUnknownRequires = "一次上游返回 usage.credit 的真实调用（成功观测即自动消解）"

// TrackCostUnknown 在请求路径上登记「该组合成本未知」（幂等）。
//
// 快速路径：sync.Map.LoadOrStore 命中即返回（一次原子读），不碰 p.mu。
// 只有当该组合**首次**出现时，才读一次 tier 并进 registry 的锁。
//
// 调用时机：选号落到某 (账号,模型) 且该组合尚无有效观测时。调用方无需判重。
func (p *Pool) TrackCostUnknown(uid, model string) {
	if p.unknownReg == nil || uid == "" || model == "" {
		return
	}
	if p.costKnown(uid, model) {
		return // 已有有效观测：不是未知
	}

	key := costUnknownKey(uid, model)
	q := costUnknownQuestion(uid, model)
	// ID 可预计算（纯函数），因此可以原子地"占位 + 拿到 ID"一步完成，
	// 不存在"占位了但 ID 还没写"的窗口。
	id := unknown.DeriveID(q, costUnknownRequires)
	if _, loaded := p.unknownSeen.LoadOrStore(key, id); loaded {
		return // 已登记过
	}
	if _, err := p.unknownReg.Record(q, costUnknownRequires); err != nil {
		// requires 是常量非空，Record 只会因 question 为空而失败（此处不可能）。
		// 失败则移除占位，让下次重试（不静默留一个永远消解不掉的占位）。
		p.unknownSeen.Delete(key)
	}
}

// resolveCostUnknown 实测到 credit 后消解对应未知（由 NoteModelCost 调用）。
//
// revision 语义：registry 要求回传拿到的 revision，过期即报错迫使重读。
// 这里用"读一次 + 消解一次，stale 则重读重试一次"的有限重试——成本低，
// 且避免在罕见并发下把消解漏掉（漏掉会让面板永远挂着一条已经消解的未知）。
func (p *Pool) resolveCostUnknown(uid, model string) {
	if p.unknownReg == nil {
		return
	}
	key := costUnknownKey(uid, model)
	v, ok := p.unknownSeen.Load(key)
	if !ok {
		return // 从未登记为未知（比如上线前就有观测）：无需消解
	}
	id, _ := v.(string)
	if id == "" {
		return
	}
	evidence := "note_model_cost:" + model
	for attempt := 0; attempt < 2; attempt++ {
		it, found := p.unknownReg.Get(id)
		if !found || it.Resolved {
			return
		}
		if _, err := p.unknownReg.Resolve(id, it.Revision, evidence); err == nil {
			return
		}
		// err 只可能是 ErrStaleRevision（并发消解者赢了）→ 重读一次再试。
		// 第二轮仍失败则放弃（对方已消解，或条目被外部改动）；下次观测再走一遍。
	}
}

// costKnown 报告该 (账号, 模型) 是否已有**有效**（未过期）成本观测。
//
// 只读 p.mu.RLock：与选号读取侧同口径（同样的 TTL 判定），避免"台账过期了
// 但未知没登记"的漏洞。
func (p *Pool) costKnown(uid, model string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false
	}
	mc, has := e.modelCost[model]
	if !has || mc.LastSeen.IsZero() {
		return false
	}
	return time.Since(mc.LastSeen) <= modelCostTTL
}

// reopenCostUnknown 成本观测过期后，把已消解的条目重新打开。
//
// 为什么需要：消解是单向闩锁，而 modelCost 有 6h TTL——过期后选号侧已经按"未知"
// 处理（credit_floor 不再用本地实测），但 registry 里那条仍 Resolved=true，于是
// /metrics 的 unknowns_open 读 0，指标与真实未知脱钩。
//
// 触发点：只在台账过期清理时调用（落盘路径的 pruneAllLocked），**不在选号热路径**，
// 所以这里碰 registry 的锁是安全的（ADR-007 取舍二约束的是 pick 临界区）。
// 调用方必须已持 p.mu 写锁；锁序 p.mu → registry.mu，registry 不会回拿 p.mu。
func (p *Pool) reopenCostUnknown(uid, model string) {
	if p.unknownReg == nil || uid == "" || model == "" {
		return
	}
	v, ok := p.unknownSeen.Load(costUnknownKey(uid, model))
	if !ok {
		return // 从未登记过（上线前就有观测）：无需重开
	}
	id, _ := v.(string)
	if id == "" {
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		it, found := p.unknownReg.Get(id)
		if !found || !it.Resolved {
			return // 未消解（或已消失）：不需要重开
		}
		if _, err := p.unknownReg.Reopen(id, it.Revision, "cost_observation_expired"); err == nil {
			return
		}
		// err 只可能是 ErrStaleRevision（并发改了 revision）→ 重读一次再试。
	}
}

// OpenUnknownCount 未消解成本未知的总数（/metrics gauge / 面板徽章用）。
//
// 遍历全部条目：条目数有界（账号 × 实际用过的模型），且只在抓取/渲染时调用，
// 不在热路径上。
func (p *Pool) OpenUnknownCount() int {
	if p.unknownReg == nil {
		return 0
	}
	return p.unknownReg.Len()
}

// AllOpenUnknowns 返回全池未消解的 unknown 条目（registry 全量，不分账号、不分来源）。
//
// 为什么需要：面板「运维 Agent」视图（REQ-opsagent 阶段 2）。opsagent 登记的条目
// （如 402-余额口径矛盾）没有 unknownSeen 键——那是 cost 口径的 uid|model 键，
// openCostUnknownsLocked 按它过滤永远看不到 agent 条目（2026-10-24 实测缺口）。
// 全量口径让两类来源在同一视图可见。
func (p *Pool) AllOpenUnknowns() []unknown.Item {
	if p.unknownReg == nil {
		return nil
	}
	var out []unknown.Item
	for _, it := range p.unknownReg.All() {
		if !it.Resolved {
			out = append(out, it)
		}
	}
	return out
}

// openCostUnknownsLocked 收集该账号未消解的成本未知（供 Status 透出）。
//
// 调用方必须已持 p.mu（读锁即可）。遍历 sync.Map 在 RLock 下是安全的
// （sync.Map 自带同步）；条目数有界（账号 × 模型），开销可忽略。
func (p *Pool) openCostUnknownsLocked(uid string) []unknown.Item {
	if p.unknownReg == nil {
		return nil
	}
	prefix := uid + "|"
	var out []unknown.Item
	p.unknownSeen.Range(func(k, v any) bool {
		key, _ := k.(string)
		if len(key) < len(prefix) || key[:len(prefix)] != prefix {
			return true // 不是本账号
		}
		id, _ := v.(string)
		it, ok := p.unknownReg.Get(id)
		if !ok || it.Resolved {
			return true // 已消解/已消失：不占注意力
		}
		out = append(out, it)
		return true
	})
	return out
}
