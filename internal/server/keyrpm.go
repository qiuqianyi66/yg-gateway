// keyrpm.go 按 Key 独立计数的 RPM 限流器。
//
// 与 F-003 只读端点限流（按 IP、全局一档）不同：这里按**下游 API Key** 限流，
// 防止某一把被分享出去的 key 把整个账号池薅爆。每把 key 的上限来自配置
// api_keys[].rpm（0 = 不限，旧行为零回归）。
//
// 实现选择：固定窗口 1 分钟 + per-key 计数器。窗口边界允许瞬时 2× 突发，
// 对"防单 key 打穿池子"的语义足够（不是精确计费配额）。复用 ratelimit.go 的
// 自写无依赖风格，不引 x/time/rate。
package server

import (
	"sync"
	"time"

	"yg-gateway/internal/httpauth"
)

// rpmWindow 单把 key 当前 1 分钟窗口的计数状态。
type rpmWindow struct {
	count int
	start time.Time
}

// keyRPMGuard 进程内 per-key RPM 限流状态。重启清零（与只读端点限流一致）。
type keyRPMGuard struct {
	mu      sync.Mutex
	windows map[string]*rpmWindow
}

func newKeyRPMGuard() *keyRPMGuard {
	return &keyRPMGuard{windows: map[string]*rpmWindow{}}
}

// allow 报告该 key 是否还在 RPM 限额内。
// key 为 nil（未启用鉴权）或 key.RPM<=0（未配置限额）时恒放行。
func (g *keyRPMGuard) allow(key *httpauth.Key) bool {
	if g == nil || key == nil || key.RPM <= 0 {
		return true
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	// 惰性 GC：窗口数膨胀到阈值时顺手清掉过期窗口，防 map 无限增长
	//（key 数量小，正常不会触发；只在长期高频增删 key 时兜底）。
	if len(g.windows) > 128 {
		for id, w := range g.windows {
			if now.Sub(w.start) >= time.Minute {
				delete(g.windows, id)
			}
		}
	}
	w := g.windows[key.ID]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &rpmWindow{start: now}
		g.windows[key.ID] = w
	}
	if w.count >= key.RPM {
		return false
	}
	w.count++
	return true
}
