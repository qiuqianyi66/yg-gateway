package main

// Default 默认配置。
func Default() *Config {
	c := &Config{
		Listen:    ":7863",
		APIKey:    "",
		AuthDir:   "./auths",
		StateFile: "./data/state.json",
	}
	c.Cooldown.SoftRate = "600s"
	c.Cooldown.SoftRateMax = "2h"
	c.Server.ReadTimeout = "300s"
	c.Panel.PackageDetailLimit = 5
	c.Logging.RequestArchiveEnabled = true
	c.Logging.RequestRetentionDays = 7
	c.Logging.RequestArchiveMaxMB = 100
	// 慢请求阈值默认 120s（取值理由见 observability 字段注释）。
	c.Observability.SlowRequestThresholdSeconds = 120
	// 积分兑人民币默认汇率 1 积分 ≈ ¥0.01（纯展示层，不改选号）。
	c.Usage.CreditCNYRate = 0.01
	// 缺省 true 靠显式赋值实现（同 Schedule 开关）：JSON 里键缺席时字段保留此值，
	// 只有显式 false 才关闭来源记录。
	c.Logging.RequestClientInfo = true
	c.Schedule.CheckinHours = []int{9, 21}
	c.Schedule.TravelHours = []int{9, 21}
	c.Schedule.ActivityHours = []int{10}
	c.Schedule.KeepaliveHours = []int{22}
	c.Schedule.BlackcatHours = []int{23}
	c.Schedule.GrowthHours = []int{1}
	// 开关「缺省 true」靠这几行实现：Load 先取 Default() 再 json.Unmarshal 覆盖，
	// 键缺席（或为 null）时字段原样保留 true，只有显式 false 才关。
	c.Schedule.CheckinEnabled = true
	c.Schedule.GrowthEnabled = true
	c.Schedule.TravelEnabled = true
	c.Schedule.ActivityEnabled = true
	c.Schedule.KeepaliveEnabled = true
	c.Schedule.BlackcatEnabled = true
	c.Schedule.BalanceRefreshEnabled = true
	c.Schedule.BalanceRefreshMinutes = 5
	c.Upstream.TimeoutSeconds = 120
	// HeaderTimeoutSeconds/IdleTimeoutSeconds 默认 0（未设置态），回落见 normalize()。
	c.Upstream.HeaderTimeoutSeconds = 0
	c.Upstream.IdleTimeoutSeconds = 0
	// Global.Enabled 缺省 true（纯 CN 行为不变：CN 账号恒判 cn，global base 不被使用）；
	// ChatBase/BillingBase 缺省空（回落内置默认）。
	c.Global.Enabled = true
	c.Features.SanitizeBlacklistFingerprints = true
	c.Prompt.Mode = "passthrough" // 缺省 passthrough：透传客户端原始 system（对齐上游；custom 由用户显式选择）
	c.Pool.MaxInFlight = 3
	// MaxInFlightGlobal 缺省 2：global 域 WAF 风控更紧，压低单号并发（WAF 403 修复
	// P1-1）；0/负数 normalize 回落默认（与 max_in_flight 的 0=不限语义不同，分档键
	// 的 0 没有合理语义，回退分档默认最稳）。
	c.Pool.MaxInFlightGlobal = 2
	c.Pool.BreakerThreshold = 3
	c.Pool.BreakerCooldown = "30m"
	c.Pool.BreakerCooldownMax = "6h"
	// BreakerProbeGap 缺省 "30s"（ADR-003 D-400）：熔断到期后限速试探，避免
	// 到期瞬间全量洪峰把坏号二次打挂（二次熔断会让退避翻倍、最长 6h）。
	c.Pool.BreakerProbeGap = defaultBreakerProbeGap
	// GlobalMaxInFlight 缺省 0 = 不限（ADR-003 D-401）：默认不改变既有行为。
	c.Pool.GlobalMaxInFlight = 0
	c.Pool.DegradeThreshold = 5
	c.Pool.DegradeCooldown = "10m"
	c.Pool.DegradeCooldownMax = "2h"
	c.Pool.IdleWeightPerHour = 0.5
	c.Pool.IdleWeightMax = 5.0
	c.Pool.PreferExpiring = true
	c.Pool.ExpiringSoon = "168h" // 快过期窗口默认 7 天：官方活动奖励积分多在两周内过期
	// costTier 探索默认 30m（issue #136：垄断破除 + 搭车改道零新增请求）；"0" 关停。
	c.Pool.CostExploreInterval = "30m"
	// EWMA 表现回灌默认关闭（ADR-005 D-502）：不改变既有行为；Alpha/MinSamples
	// 默认值必须与 internal/pool entry.go 的 defaultEwma* 一致。
	c.Pool.Ewma = EwmaConfig{Enabled: false, Alpha: defaultEwmaAlpha, MinSamples: defaultEwmaMinSamples}
	c.SessionSticky.Enabled = true
	c.SessionSticky.TTL = "30m"
	c.SessionSticky.GCInterval = "5m"
	return c
}

// defaultPricing 内置 OpenRouter 美元参考价（$/1M tokens）。
// 数值为 2025-2026 年公开价的近似值，仅供"白嫖感知"展示——不是实时报价，
// 用户应在 config.json usage.pricing 里按实际模型覆盖。
// key 为裸模型名（不含 cn:/global: 前缀），与 usage 桶的 Model 字段同口径。
func defaultPricing() map[string]ModelPrice {
	return map[string]ModelPrice{
		// GLM 系列（智谱）
		"glm-5.3-flash": {Input: 0.15, Output: 0.6, CacheRead: 0.015},
		"glm-5.3":       {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"glm-5.2":       {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"glm-5.1":       {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"glm-5v-turbo":  {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		// MiniMax
		"minimax-m3": {Input: 0.3, Output: 1.2, CacheRead: 0.03},
		// DeepSeek
		"deepseek-v4-flash":   {Input: 0.15, Output: 0.6, CacheRead: 0.015},
		"deepseek-v4.1-flash": {Input: 0.15, Output: 0.6, CacheRead: 0.015},
		"deepseek-v4-pro":     {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		// Kimi（月之暗面）
		"kimi-k2.6":         {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"kimi-k2.7":         {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"kimi-k2.8-preview": {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"kimi-k3-1":         {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		// 混元（腾讯）
		"hy3":         {Input: 0.1, Output: 0.1, CacheRead: 0.01},
		"hy3-x":       {Input: 0.1, Output: 0.1, CacheRead: 0.01},
		"hy4-preview": {Input: 1.1, Output: 1.1, CacheRead: 0.11},
		// 阶跃
		"step-5-preview": {Input: 0.6, Output: 2.4, CacheRead: 0.06},
	}
}
