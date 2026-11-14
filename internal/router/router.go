// Package router 定义**非 credits** 账号的选号策略。credits 账号仍走 internal/pool
// 的积分权重组合（pick.go），本包不实现 credits——避免两处权重语义漂移。
//
// 依赖纪律：只依赖 provider（QuotaKind 枚举），不读 pool；pool 负责把账号状态
// 装配成 View 再交本包排序。选号策略是「纯函数 + 游标」，不持额度/健康状态。
package router

import (
	"sync"
	"time"
)

// Mode 选号策略标识。
type Mode string

const (
	// ModeOrder 顺序：取第一个可被选账号；失败后剔除重选（重试由调用方驱动）。
	ModeOrder Mode = "order"
	// ModeRotate 轮转：跨请求环形轮换，均衡打散（游标自持有，并发安全）。
	ModeRotate Mode = "rotate"
	// ModeUsage 最少使用：InFlight 最小者优先，并列取 LastUsed 最早者。
	ModeUsage Mode = "usage"
)

// View 选号所需的账号状态快照。由 pool 装配后传入，router 不读 pool 内部结构
// （依赖方向：pool -> router，禁止反向）。
type View struct {
	// Healthy 是否可被选（冷却 / 熔断 / 连败降权之后的最终结论，由 pool 判定）。
	Healthy bool
	// QuotaValue 剩余额度（credits=积分、metered=余额、window=窗口剩余）。
	QuotaValue int64
	// QuotaUnknown 额度是否未知（未知 ≠ 0，AGENTS.md 四态纪律）。
	QuotaUnknown bool
	// InFlight 在途请求数。
	InFlight int
	// LastUsed 最近一次被选时刻（零值 = 从未被选）。
	LastUsed time.Time
}

// Strategy 一条选号策略。Pick 返回选中下标；无可用返回 ok=false。
type Strategy interface {
	Mode() Mode
	Pick(views []View) (index int, ok bool)
}

// Order 顺序策略：取第一个 Healthy 账号。
type Order struct{}

func (Order) Mode() Mode { return ModeOrder }

func (Order) Pick(views []View) (int, bool) {
	for i, v := range views {
		if v.Healthy {
			return i, true
		}
	}
	return 0, false
}

// Rotate 轮转策略：跨请求环形轮换。游标自持有、并发安全（R9：有回收路径，
// 无 goroutine 泄漏；这里只用互斥锁，不 spawn goroutine）。
type Rotate struct {
	mu   sync.Mutex
	next int
}

func (r *Rotate) Mode() Mode { return ModeRotate }

func (r *Rotate) Pick(views []View) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(views)
	if n == 0 {
		return 0, false
	}
	for step := 0; step < n; step++ {
		i := (r.next + step) % n
		if views[i].Healthy {
			r.next = (i + 1) % n
			return i, true
		}
	}
	return 0, false
}

// Usage 最少使用策略：InFlight 最小者优先，并列取 LastUsed 最早者。
type Usage struct{}

func (Usage) Mode() Mode { return ModeUsage }

func (Usage) Pick(views []View) (int, bool) {
	best := -1
	for i, v := range views {
		if !v.Healthy {
			continue
		}
		if best == -1 ||
			v.InFlight < views[best].InFlight ||
			(v.InFlight == views[best].InFlight && v.LastUsed.Before(views[best].LastUsed)) {
			best = i
		}
	}
	if best == -1 {
		return 0, false
	}
	return best, true
}

// compile-time 断言：三个策略都实现 Strategy 接口。
var (
	_ Strategy = Order{}
	_ Strategy = (*Rotate)(nil)
	_ Strategy = Usage{}
)
