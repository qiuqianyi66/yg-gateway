package server

import (
	"sync"
	"sync/atomic"
	"time"
)

// degradeGate 降级状态机：passthrough 模式下请求被上游内容策略拦截时，
// 切换到 Degraded 中性提示词直到次日 00:00 CST 重置。
//
// 定位：误报处理（与 internal/upstream/sanitize.go 同哲学）——内容拦截多为
// system 来源的指纹误报，换最小中性提示词即可绕开；不是对抗框架。
//
// 状态机：进程内存、重启清零（可接受：重启极少触发，且 custom 模式根本
// 不进降级路径）。降级期内的请求直达 Degraded，不再先撞 400。
// 并发模型（热路径降锁）：until 存 Unix 纳秒的 atomic.Int64，Active() 全程
// 无锁；Trigger() 仍在 mutex 下做「读-改-写」，保证「不续期」语义原子。
// untilUnix 用 int64 而不是 time.Time：atomic 不能承载结构体，且降级判据只有
// 「现在是否早于 until」这一个比较，纳秒时间戳足够。0 = 从未触发。
type degradeGate struct {
	mu        sync.Mutex
	untilUnix atomic.Int64
}

// until 读取当前降级截止时刻（零值 = 未触发）。
func (g *degradeGate) until() time.Time {
	n := g.untilUnix.Load()
	if n <= 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Active 当前是否处于降级期（now < until）。热路径无锁。
func (g *degradeGate) Active() bool {
	n := g.untilUnix.Load()
	if n <= 0 {
		return false
	}
	return time.Now().UnixNano() < n
}

// Trigger 触发降级，直到次日 00:00 CST（Asia/Shanghai）。
// 已在降级期内则不续期（保持最早触发点的 00:00 重置语义）。
//
// 「判定 + 置位」必须原子：并发 Trigger 若各自读到未过期，会同时算出新 until
// 并互相覆盖（虽然两者都是同一个午夜、结果等价，但语义上仍应是单次置位）。
func (g *degradeGate) Trigger() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Now().UnixNano() >= g.untilUnix.Load() {
		g.untilUnix.Store(nextMidnightCST(time.Now()).UnixNano())
	}
}

// nextMidnightCST 返回 now 之后最近的 Asia/Shanghai 00:00 时刻（纯函数，方便单测）。
//
// 边界语义：
//   - 23:59 → 次日 00:00（几秒后）
//   - 00:00 → 次日 00:00（刚过 00:00，下个零点是次日）
//
// 用固定 +08:00 偏移计算，避免依赖系统时区配置（容器/宿主机时区不确定）。
func nextMidnightCST(now time.Time) time.Time {
	cst := time.FixedZone("CST", 8*60*60)
	// 把 now 转到 CST 视角取当天 00:00，再加一天；若该 00:00 <= now 则再加一天。
	y, m, d := now.In(cst).Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, cst)
	for !midnight.After(now) {
		midnight = midnight.Add(24 * time.Hour)
	}
	return midnight
}
