// config_opsagent.go 运维 agent 配置（REQ-opsagent-2026-10-24 MVP / ADR-017）。
//
// 默认 enabled=false：不装配任何东西（零回归）。结构体定义独立成文件——
// config.go 处于 maxFileLines 棘轮上限（1088/1089），只允许加一行字段。
package main

import (
	"time"

	"yg-gateway/internal/opsagent"
)

// OpsAgentConfig 运维 agent 配置。规则引擎 = 观察-建议；runtime = DSH 式循环，
// 默认关闭，只有显式开启且配置模型才启动。
type OpsAgentConfig struct {
	// Enabled 是否启用。false（默认）= main 完全不装配。
	Enabled bool `json:"enabled"`
	// IntervalSec 轮询间隔（秒）。<=0 回落 600（10 分钟）。
	IntervalSec int `json:"interval_sec"`
	// HealthyFrac 容量告警阈值：healthy/total 严格小于该值时发建议。<=0 回落 0.5。
	HealthyFrac float64 `json:"healthy_frac"`
	// Agent runtime（ADR-029）。默认 enabled=false：规则引擎行为零变化。
	Agent RuntimeAgentConfig `json:"agent"`
}

// RuntimeAgentConfig 运维 agent runtime 的配置。
// 模型默认空 = 不启动：避免部署时无意调用付费模型。
type RuntimeAgentConfig struct {
	Enabled          bool   `json:"enabled"`
	Model            string `json:"model"`
	MaxSteps         int    `json:"max_steps"`
	MaxTokens        int    `json:"max_tokens"`
	ContextMaxTokens int    `json:"context_max_tokens"`
	TurnTimeoutSec   int    `json:"turn_timeout_sec"`
	SessionDir       string `json:"session_dir"`
}

// normalized 转成 opsagent.Config（默认值兜底在 opsagent 侧统一做）。
func (c OpsAgentConfig) normalized() opsagent.Config {
	return opsagent.Config{
		Interval:    time.Duration(c.IntervalSec) * time.Second,
		HealthyFrac: c.HealthyFrac,
	}
}

// runtimeConfig 转成 opsagent.RuntimeConfig。SessionDir 为空时由装配侧用
// state 文件同目录兜底（不把运行数据写进当前工作目录）。
func (c OpsAgentConfig) runtimeConfig() opsagent.RuntimeConfig {
	return opsagent.RuntimeConfig{
		Enabled:          c.Agent.Enabled,
		Model:            c.Agent.Model,
		SessionDir:       c.Agent.SessionDir,
		Interval:         time.Duration(c.IntervalSec) * time.Second,
		MaxSteps:         c.Agent.MaxSteps,
		MaxTokens:        c.Agent.MaxTokens,
		ContextMaxTokens: c.Agent.ContextMaxTokens,
		TurnTimeout:      time.Duration(c.Agent.TurnTimeoutSec) * time.Second,
	}
}
