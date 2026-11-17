// main_session.go 会话粘性路由装配（从 main() 切出）。
package main

import (
	"yg-gateway/internal/pool"
	"yg-gateway/internal/redisstore"
	"yg-gateway/internal/session"
)

// buildSessionRouter 按 config 装配会话粘性路由（可配关闭），并返回面板/状态页
// 需要的两个只读闭包（关闭时返回零值，语义与拆分前 main() 内联版本一致）。
//
// redisMode 由 store 具体类型推导：Noop = 纯内存模式，其余按 upstash 上报。
func buildSessionRouter(cfg *Config, p *pool.Pool, store redisstore.Store) (*session.Router, func() int, func() []session.SessionBinding, string) {
	redisMode := "noop"
	if _, ok := store.(redisstore.Noop); !ok {
		redisMode = "upstash"
	}
	// 会话粘性路由（可配关闭）。
	var sessRouter *session.Router
	if cfg.SessionSticky.Enabled {
		sessRouter = session.New(session.Config{
			TTL:        cfg.SessionTTL,
			GCInterval: cfg.SessionGCInterval,
			Store:      store,
			Available:  p.AvailableUIDs,
			// realm 感知闭包：带前缀模型名按 realm 过滤可用账号（跨 realm 不泄漏）；
			// 裸名走 cn（现状零回归）。闭包内部 resolveModel 剥前缀，再按 realm 过滤。
			AvailableForModel: realmAwareAvailableForModel(p),
		})
		sessRouter.LoadFromStore() // 启动时从 Redis 恢复粘性（读操作仅此处）
		sessRouter.StartGC()
	}
	sessCount := func() int {
		if sessRouter != nil {
			return sessRouter.Count()
		}
		return 0
	}
	sessSnapshot := func() []session.SessionBinding {
		if sessRouter != nil {
			return sessRouter.Snapshot()
		}
		return nil
	}
	return sessRouter, sessCount, sessSnapshot, redisMode
}
