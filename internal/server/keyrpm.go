// keyrpm.go 按 Key 独立计数的 RPM 限流器 + 实际速率观测（F6）。
//
// 与 F-003 只读端点限流（按 IP、全局一档）不同：这里按**下游 API Key** 限流，
// 防止某一把被分享出去的 key 把整个账号池薅爆。每把 key 的上限来自配置
// api_keys[].rpm（0 = 不限，旧行为零回归）。
//
// 实现选择：固定窗口 1 分钟 + per-key 计数器。窗口边界允许瞬时 2× 突发，
// 对"防单 key 打穿池子"的语义足够（不是精确计费配额）。复用 ratelimit.go 的
// 自写无依赖风格，不引 x/time/rate。
//
// 计数口径（F6）：record 在 withAuth 处对**每次实际请求**调用（含将被 429 的、
// 无 rpm 配置的 key）——统计=真实请求速率，不是「被放行数」；allow 只判不限，
// 限流语义与旧实现逐位一致（每窗口恰好放行 RPM 次：先 record 后 allow，第
// RPM+1 次 count>limit 被拒）。
package server

import (
	"sort"
	"sync"
	"time"

	"yg-gateway/internal/httpauth"
)

// rpmHistoryLen 每把 key 保留的闭合分钟历史桶数（F6 曲线长度；12 分钟窗口）。
const rpmHistoryLen = 12

// rpmWindow 单把 key 当前 1 分钟窗口的计数状态。
type rpmWindow struct {
	count int
	start time.Time
}

// rpmStats 单把 key 的限流+观测状态：当前窗口计数、闭合历史桶、进程内累计。
type rpmStats struct {
	limit   int       // api_keys[].rpm 当前限额（0 = 不限；record 时同步，支持热生效）
	cur     rpmWindow // 当前 1 分钟窗口
	buckets []int     // 已闭合分钟桶（最旧在前，容量 rpmHistoryLen，环形截断）
	total   int       // 本进程启动以来累计请求（重启清零）
	last    time.Time // 最近一次 record 时刻（惰性 GC 用）
}

// keyRPMGuard 进程内 per-key RPM 限流状态。重启清零（与只读端点限流一致）。
type keyRPMGuard struct {
	mu    sync.Mutex
	stats map[string]*rpmStats
}

func newKeyRPMGuard() *keyRPMGuard {
	return &keyRPMGuard{stats: map[string]*rpmStats{}}
}

// record 记录一次实际请求（含将被限流的请求）。key 为 nil 时不记录。
// 分钟翻转时把当前窗口计数压入历史桶（环形截断到 rpmHistoryLen）。
func (g *keyRPMGuard) record(key *httpauth.Key) {
	if g == nil || key == nil {
		return
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.ensureLocked(key.ID, now)
	st.limit = key.RPM
	st.cur.count++
	st.total++
	st.last = now
}

// allow 报告该 key 是否还在 RPM 限额内（只判不计数——计数由 record 统一负责，
// withAuth 先 record 再 allow，保证被 429 的请求也进统计）。
// key 为 nil（未启用鉴权）或 key.RPM<=0（未配置限额）时恒放行。
func (g *keyRPMGuard) allow(key *httpauth.Key) bool {
	if g == nil || key == nil || key.RPM <= 0 {
		return true
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.ensureLocked(key.ID, now)
	return st.cur.count <= key.RPM
}

// ensureLocked 取（或建）key 的状态，并做分钟翻转与惰性 GC。调用方须持 g.mu。
func (g *keyRPMGuard) ensureLocked(id string, now time.Time) *rpmStats {
	// 惰性 GC：key 长期无活动（超过保留窗口）时清掉，防 map 无限增长
	//（key 数量小，正常不会触发；只在长期高频增删 key 时兜底）。
	if len(g.stats) > 128 {
		for sid, st := range g.stats {
			if now.Sub(st.last) > (rpmHistoryLen+1)*time.Minute {
				delete(g.stats, sid)
			}
		}
	}
	st := g.stats[id]
	if st == nil {
		st = &rpmStats{cur: rpmWindow{start: now}, last: now}
		g.stats[id] = st
		return st
	}
	// 分钟翻转：把当前窗口计数值推进历史桶（环形截断）。
	if now.Sub(st.cur.start) >= time.Minute {
		st.buckets = append(st.buckets, st.cur.count)
		if len(st.buckets) > rpmHistoryLen {
			st.buckets = st.buckets[len(st.buckets)-rpmHistoryLen:]
		}
		st.cur = rpmWindow{start: now}
	}
	return st
}

// Snapshot 返回 key RPM 观测快照（F6，面板渲染用）：每把观测过的 key 输出
// {key_id, limit, rpm(当前窗口), total(进程累计), history(最近分钟桶+当前)}。
// history 尾元素 = 当前窗口计数（曲线连续）；无观测时返回空切片（观测到 0，
// 非 unknown）。按键 id 排序，输出确定性。
func (g *keyRPMGuard) Snapshot() []map[string]any {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]map[string]any, 0, len(g.stats))
	for id, st := range g.stats {
		hist := make([]int, 0, len(st.buckets)+1)
		hist = append(hist, st.buckets...)
		hist = append(hist, st.cur.count)
		out = append(out, map[string]any{
			"key_id":  id,
			"limit":   st.limit,
			"rpm":     st.cur.count,
			"total":   st.total,
			"history": hist,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["key_id"].(string) < out[j]["key_id"].(string)
	})
	return out
}
