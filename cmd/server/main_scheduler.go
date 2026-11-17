// main_scheduler.go 调度器装配（从 main() 切出：原 22 行大字面量）。
package main

import (
	"yg-gateway/internal/pool"
	"yg-gateway/internal/scheduler"
	"yg-gateway/internal/upstream"
)

// buildScheduler 按 config 的 schedule 块装配调度器并打印任务横幅。
// 各任务的 Enabled 取反映射为 Disabled（零值 = 启用），与拆分前 main() 内联版本一致。
func buildScheduler(cfg *Config, p *pool.Pool, up *upstream.Client) *scheduler.Scheduler {
	sch := scheduler.New(scheduler.Config{
		Pool:           p,
		Upstream:       up,
		CheckinHours:   cfg.Schedule.CheckinHours,
		TravelHours:    cfg.Schedule.TravelHours,
		ActivityHours:  cfg.Schedule.ActivityHours,
		KeepaliveHours: cfg.Schedule.KeepaliveHours,
		BlackcatHours:  cfg.Schedule.BlackcatHours,
		GrowthHours:    cfg.Schedule.GrowthHours,
		// 快过期积分优先消耗：签到/余额刷新按此窗口分桶（issue:积分过期）。
		ExpiringSoonWindow: cfg.ExpiringSoonDur,
		CheckinDisabled:    !cfg.Schedule.CheckinEnabled,
		TravelDisabled:     !cfg.Schedule.TravelEnabled,
		ActivityDisabled:   !cfg.Schedule.ActivityEnabled,
		KeepaliveDisabled:  !cfg.Schedule.KeepaliveEnabled,
		BlackcatDisabled:   !cfg.Schedule.BlackcatEnabled,
		GrowthDisabled:     !cfg.Schedule.GrowthEnabled,
		// 保号类四任务是否覆盖禁用账号（缺省 false = 禁用即跳过，保持既有行为）。
		IncludeDisabledInTasks: cfg.Schedule.IncludeDisabledInTasks,
		// 账号现态每日快照（03:00 落盘，历史存活曲线数据地基；目录=state 同目录 snapshots/）。
		SnapshotDir: stateSibling(cfg.StateFile, "snapshots"),
	})
	logScheduleBanner(cfg)
	return sch
}
