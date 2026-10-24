// config_opsagent.go 运维 agent 配置（REQ-opsagent-2026-10-24 MVP / ADR-017）。
//
// 默认 enabled=false：不装配任何东西（零回归）。结构体定义独立成文件——
// config.go 处于 maxFileLines 棘轮上限（1088/1089），只允许加一行字段。
package main

import (
	"time"

	"yg-gateway/internal/opsagent"
)

// OpsAgentConfig 运维 agent 配置。MVP = 观察-建议模式：不自动执行任何运维动作。
type OpsAgentConfig struct {
	// Enabled 是否启用。false（默认）= main 完全不装配。
	Enabled bool `json:"enabled"`
	// IntervalSec 轮询间隔（秒）。<=0 回落 600（10 分钟）。
	IntervalSec int `json:"interval_sec"`
	// HealthyFrac 容量告警阈值：healthy/total 严格小于该值时发建议。<=0 回落 0.5。
	HealthyFrac float64 `json:"healthy_frac"`
}

// normalized 转成 opsagent.Config（默认值兜底在 opsagent 侧统一做）。
func (c OpsAgentConfig) normalized() opsagent.Config {
	return opsagent.Config{
		Interval:    time.Duration(c.IntervalSec) * time.Second,
		HealthyFrac: c.HealthyFrac,
	}
}
