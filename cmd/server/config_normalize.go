// config_normalize.go 配置归一（缺省填充 → 派生时长解析 → 数值/三态归一）。
//
// 拆分来由（2026-11-15）：`Config.normalize()` 原是一条 218 代码行的直线，25 个互不
// 相干的配置段依次执行；而 `config.go` 是近 60 次提交里改动第 2 频繁的文件（10 次），
// 加一个配置项要在 200 行里找落点、还要自己判断该排在哪个解析表之前。现在每个方法
// 只对一个配置段负责，调用顺序集中在 `normalize()` 一处声明。
//
// **顺序是契约**：调用顺序既决定派生字段的消费关系（缺省回落必须早于解析表），也决定
// 「多字段同时非法时先报哪个错」。调整顺序 = 改变错误优先级。
package main

import (
	"fmt"
	"strings"
	"time"
)

// normalizeUsagePricing 美元参考价合并：内置默认表为底，用户 usage.pricing 按 key
// 覆盖同名模型。不区分"用户没配"与"用户配了空表"——空表 = 用户显式不要默认价。
func (c *Config) normalizeUsagePricing() {
	if c.Usage.Pricing == nil {
		c.Usage.Pricing = defaultPricing()
	} else {
		for k, v := range defaultPricing() {
			if _, ok := c.Usage.Pricing[k]; !ok {
				c.Usage.Pricing[k] = v
			}
		}
	}
}

// normalizePanelLoggingDefaults 面板与日志三项计数型默认值（<=0 一律回落默认）。
func (c *Config) normalizePanelLoggingDefaults() {
	if c.Panel.PackageDetailLimit <= 0 {
		c.Panel.PackageDetailLimit = 5
	}
	if c.Logging.RequestRetentionDays <= 0 {
		c.Logging.RequestRetentionDays = 7
	}
	if c.Logging.RequestArchiveMaxMB <= 0 {
		c.Logging.RequestArchiveMaxMB = 100
	}
}

// normalizeDurationDefaults 五个「空串 → 缺省值」的时长字段回落。
//
// 必须在 normalizeDurations 之前跑：解析表直接消费这几个字符串字段。
// 为什么不在这几段里顺手 ParseDuration：这些字段的合法态各不相同——空 = 未设置
// （回落默认）、"0" = 用户显式关闭（或禁用）、负值 = 无意义（部分字段 fail fast），
// 三态混在一处逐字段判断比统一进表更不容易写错。
func (c *Config) normalizeDurationDefaults() {
	// 入站读取上限（issue #100）：空值回落默认 300s；"0" 合法（不限制）；
	// 负值无语义，fail fast（静默钳 0 会把保护悄悄关掉）。
	if c.Server.ReadTimeout == "" {
		c.Server.ReadTimeout = "300s"
	}
	// 空值回落默认 2h（Default() 已置值；此兜底覆盖显式 "" 与 Default() 被绕过的场景）。
	if c.Cooldown.SoftRateMax == "" {
		c.Cooldown.SoftRateMax = "2h"
	}
	// half-open 试探间隔（ADR-003 D-400）："0" 合法（关闭），故不能沿用
	// ParseDuration 直接失败——空串才回落默认。
	if c.Pool.BreakerProbeGap == "" {
		c.Pool.BreakerProbeGap = defaultBreakerProbeGap
	}
	// 连败降权参数缺省归一（非法/未设置回落默认，与 breaker_threshold 同风格）。
	if c.Pool.DegradeCooldown == "" {
		c.Pool.DegradeCooldown = "10m"
	}
	if c.Pool.DegradeCooldownMax == "" {
		c.Pool.DegradeCooldownMax = "2h"
	}
}

// normalizeDurations 时长解析统一表（ROADMAP v3 V3-5）：dst = src 解析结果，失败
// 以字段路径包装错误 fail fast。特殊语义不进表：ReadTimeout/ProbeGap 的负值检查
// （紧随表后）、ExpiringSoon/CostExplore 的「空=禁用、0 合法」三态（各自块内保留）。
func (c *Config) normalizeDurations() error {
	var err error
	for _, d := range []struct {
		dst  *time.Duration
		src  string
		name string
	}{
		{&c.ServerReadTimeoutDur, c.Server.ReadTimeout, "server.read_timeout"},
		{&c.SoftRateDur, c.Cooldown.SoftRate, "cooldown.soft_rate"},
		{&c.SoftRateMaxDur, c.Cooldown.SoftRateMax, "cooldown.soft_rate_max"},
		{&c.BreakerCooldownDur, c.Pool.BreakerCooldown, "pool.breaker_cooldown"},
		{&c.BreakerCooldownMaxD, c.Pool.BreakerCooldownMax, "pool.breaker_cooldown_max"},
		{&c.BreakerProbeGapDur, c.Pool.BreakerProbeGap, "pool.breaker_probe_gap"},
		{&c.DegradeCooldownDur, c.Pool.DegradeCooldown, "pool.degrade_cooldown"},
		{&c.DegradeCooldownMaxD, c.Pool.DegradeCooldownMax, "pool.degrade_cooldown_max"},
		{&c.SessionTTL, c.SessionSticky.TTL, "session_sticky.ttl"},
		{&c.SessionGCInterval, c.SessionSticky.GCInterval, "session_sticky.gc_interval"},
	} {
		if *d.dst, err = time.ParseDuration(d.src); err != nil {
			return fmt.Errorf("%s: %w", d.name, err)
		}
	}
	if c.ServerReadTimeoutDur < 0 {
		return fmt.Errorf("server.read_timeout: 负时长 %q 无意义", c.Server.ReadTimeout)
	}
	if c.BreakerProbeGapDur < 0 {
		return fmt.Errorf("pool.breaker_probe_gap: 负值 %s 无意义（0=关闭）", c.Pool.BreakerProbeGap)
	}
	return nil
}

// normalizeSecurityLimits 入站体量上限 / 只读端点限流 / 派生会话键三态
// （F-001 体量、F-003 限流、ADR-004 会话键）。
func (c *Config) normalizeSecurityLimits() error {
	var err error
	// 入站 body 上限（F-001）：nil（未配置）回落默认 64MB，DoS 防护恒开；
	// 显式 0 = 不限（旧行为，用户明确关闭）；负值无意义 fail fast。
	if c.Server.MaxBodyBytes == nil {
		c.MaxBodyBytesEffective = 64 << 20 // 64MB 安全默认
	} else {
		if *c.Server.MaxBodyBytes < 0 {
			return fmt.Errorf("server.max_body_bytes: 负值 %d 无意义（0=不设限，正数=上限）", *c.Server.MaxBodyBytes)
		}
		c.MaxBodyBytesEffective = *c.Server.MaxBodyBytes
	}
	// 只读端点限流窗口（F-003）：limit<=0 或 window 空/非法 = 不限流（旧行为）。
	// window 合法但 limit<=0 时静默不限流（配置了窗口但没给额度 = 用户想关限流）；
	// window 非法且 limit>0 时报错（给了额度但窗口写错 = 用户想限流但配错了）。
	if c.Security.ReadOnlyRateLimit.Limit > 0 {
		if c.Security.ReadOnlyRateLimit.Window == "" {
			return fmt.Errorf("security.read_only_rate_limit.window: 给定了 limit 但 window 为空")
		}
		if c.ReadOnlyRateLimitWindow, err = time.ParseDuration(c.Security.ReadOnlyRateLimit.Window); err != nil {
			return fmt.Errorf("security.read_only_rate_limit.window: %w", err)
		}
		if c.ReadOnlyRateLimitWindow <= 0 {
			return fmt.Errorf("security.read_only_rate_limit.window: 非正时长 %q 无意义", c.Security.ReadOnlyRateLimit.Window)
		}
	}
	// 派生会话键三态（ADR-004）：键缺失/显式 true → 开（保持既有行为）；
	// 显式 false → 关。nil 与 false 必须区分，故用 *bool。
	c.SessionStickyDerive = c.SessionSticky.Derive == nil || *c.SessionSticky.Derive
	return nil
}

// normalizePoolLimits 选号侧全部数值参数归一：快过期窗口、成本探索窗口、故障窗、
// 最小请求间隔、积分保底、EWMA、熔断/降权阈值、在途分档、闲置补偿权重。
//
// 段内顺序与拆分前逐条一致：这里的错误（窗口/间隔解析）会先于其它段的钳制生效。
func (c *Config) normalizePoolLimits() error {
	var err error
	// 快过期窗口：空 = 禁用（ExpiringSoonDur 0）；非空必须可解析（拼写错误 fail fast）。
	if c.Pool.ExpiringSoon != "" {
		if c.ExpiringSoonDur, err = time.ParseDuration(c.Pool.ExpiringSoon); err != nil {
			return fmt.Errorf("pool.expiring_soon: %w", err)
		}
	}
	if c.ExpiringSoonDur < 0 {
		c.ExpiringSoonDur = 0
		c.Pool.ExpiringSoon = "0"
	}
	// costTier 探索窗口（issue #136）：空值回落默认 30m（Default 已置；此兜底覆盖
	// 显式 ""）；"0" 是合法值（关停，完全回到现状行为），不回落；负值钳 0 同关停
	//（"−5m" 无合理语义）。
	if c.Pool.CostExploreInterval == "" {
		c.Pool.CostExploreInterval = "30m"
	}
	if c.CostExploreIntervalDur, err = time.ParseDuration(c.Pool.CostExploreInterval); err != nil {
		return fmt.Errorf("pool.cost_explore_interval: %w", err)
	}
	if c.CostExploreIntervalDur < 0 {
		c.CostExploreIntervalDur = 0
	}
	// 故障窗（P0-2）：空回落默认 30s；"0" 显式关闭；负值钳 0（= 关闭）。
	if c.Pool.FailureWindow == "" {
		c.Pool.FailureWindow = "30s"
	}
	if c.FailureWindowDur, err = time.ParseDuration(c.Pool.FailureWindow); err != nil {
		return fmt.Errorf("pool.failure_window: %w", err)
	}
	if c.FailureWindowDur < 0 {
		c.FailureWindowDur = 0
	}
	// 最小请求间隔（主动 RPM 限流，风控规避）：空值回落默认 "0"（关闭，零回归）。
	// "0" 显式关闭；负值钳 0（= 关闭）。global 分档为空则回落主值。
	if c.Pool.MinRequestInterval == "" {
		c.Pool.MinRequestInterval = "0"
	}
	if c.MinRequestIntervalDur, err = time.ParseDuration(c.Pool.MinRequestInterval); err != nil {
		return fmt.Errorf("pool.min_request_interval: %w", err)
	}
	if c.MinRequestIntervalDur < 0 {
		c.MinRequestIntervalDur = 0
	}
	if c.Pool.MinRequestIntervalGlobal == "" {
		c.Pool.MinRequestIntervalGlobal = "0"
	}
	if c.MinRequestIntervalGlobalDur, err = time.ParseDuration(c.Pool.MinRequestIntervalGlobal); err != nil {
		return fmt.Errorf("pool.min_request_interval_global: %w", err)
	}
	if c.MinRequestIntervalGlobalDur < 0 {
		c.MinRequestIntervalGlobalDur = 0
	}
	// 积分保底：负值钳 0（= 关闭）。0 是合法默认（关闭），无需空值回落。
	if c.Pool.CreditFloor < 0 {
		c.Pool.CreditFloor = 0
	}
	// pool.ewma 归一（ADR-005 D-502）：alpha 0/负值回落默认、>1 报错；min_samples
	// <=0 回落默认。enabled 无归一（bool 无非法态）。
	if c.Pool.Ewma.Alpha <= 0 {
		c.Pool.Ewma.Alpha = defaultEwmaAlpha
	}
	if c.Pool.Ewma.Alpha > 1 {
		return fmt.Errorf("pool.ewma.alpha: 必须在 (0,1]，got %v", c.Pool.Ewma.Alpha)
	}
	if c.Pool.Ewma.MinSamples <= 0 {
		c.Pool.Ewma.MinSamples = defaultEwmaMinSamples
	}
	if c.Pool.BreakerThreshold <= 0 {
		c.Pool.BreakerThreshold = 3
	}
	// 连败降权阈值缺省归一（非法/未设置回落默认，与 breaker_threshold 同风格）。
	// 注意：degrade_cooldown / degrade_cooldown_max 的空值回落**只在
	// normalizeDurationDefaults 一处**。此处曾有一份逐字重复的副本——归一后这两个
	// 字段不可能是 ""，故该副本恒不执行（2026-11-03 删除，行为等价；重复段也让
	// 覆盖率永远差 2 条）。
	if c.Pool.DegradeThreshold <= 0 {
		c.Pool.DegradeThreshold = 5
	}
	// global 在途分档：0/负数视为未设置回落默认 2（WAF 403 修复 P1-1）。
	if c.Pool.MaxInFlightGlobal <= 0 {
		c.Pool.MaxInFlightGlobal = 2
	}
	if c.Pool.IdleWeightPerHour <= 0 {
		c.Pool.IdleWeightPerHour = 0.5
	}
	if c.Pool.IdleWeightMax <= 0 {
		c.Pool.IdleWeightMax = 5.0
	}
	return nil
}

// normalizeUpstreamLimits 出站侧装配期参数归一：RPM 硬闸、在途并发闸、四层超时、
// 流总时长/心跳开关、监听地址补冒号。全部是钳制或补默认，不产生错误。
func (c *Config) normalizeUpstreamLimits() {
	// 出站 RPM 硬闸（REA P0，装配期字段）：负值钳 0（= 关闭，零回归）。
	// 0 = 关闭（默认）；>0 时每账号 60s 窗口最多该数出站请求。
	if c.Upstream.RpmPerAccount < 0 {
		c.Upstream.RpmPerAccount = 0
	}
	// 出站在途并发闸（REA §2.5，装配期字段）：负值钳 0（= 关闭，零回归）。
	if c.Upstream.ConcurrencyPerAccount < 0 {
		c.Upstream.ConcurrencyPerAccount = 0
	}
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = 120
	}
	// header 缺省回落 timeout（保"首字节前换号"既有语义）；idle 缺省走内置大值。
	// 任务书约定：0 一律视为"未设置"走默认，真正的"禁用"留待后续（避免歧义）。
	if c.Upstream.HeaderTimeoutSeconds <= 0 {
		c.Upstream.HeaderTimeoutSeconds = c.Upstream.TimeoutSeconds
	}
	if c.Upstream.IdleTimeoutSeconds <= 0 {
		c.Upstream.IdleTimeoutSeconds = 300
	}
	// 整请求总时长上限与 SSE 心跳：<=0 = 关闭（零回归，不设默认值）。
	// 有意不给默认：两者都会改变线上流的行为（掐长流 / 多写注释帧），
	// 应由部署方按自己的中间层形态显式开启。
	if c.Upstream.StreamTotalTimeoutSeconds < 0 {
		c.Upstream.StreamTotalTimeoutSeconds = 0
	}
	if c.Upstream.SSEKeepAliveSeconds < 0 {
		c.Upstream.SSEKeepAliveSeconds = 0
	}
	if !strings.HasPrefix(c.Listen, ":") && !strings.Contains(c.Listen, ":") {
		c.Listen = ":" + c.Listen
	}
}

// normalizeScheduleWindows 排程默认表 + 余额后台刷新间隔派生。
func (c *Config) normalizeScheduleWindows() {
	// 排程默认表（ROADMAP v3 V3-5）：空数组与 null 反序列化后覆盖掉 Default() 的
	// 排程值（键缺席才保留），在此补齐。空 = 未配置 → 回落默认；「禁用」一律走
	// *_enabled=false，两者互不混淆。
	for _, s := range []struct {
		dst *[]int
		d   []int
	}{
		{&c.Schedule.CheckinHours, []int{9, 21}},
		{&c.Schedule.TravelHours, []int{9, 21}},
		{&c.Schedule.ActivityHours, []int{10}},
		{&c.Schedule.KeepaliveHours, []int{22}},
		{&c.Schedule.BlackcatHours, []int{23}},
		{&c.Schedule.GrowthHours, []int{1}},
	} {
		if len(*s.dst) == 0 {
			*s.dst = s.d
		}
	}
	// 余额后台刷新：启用时 minutes<=0 回落默认 5；关闭时 interval 保持 0（不启动）。
	if c.Schedule.BalanceRefreshEnabled {
		if c.Schedule.BalanceRefreshMinutes <= 0 {
			c.Schedule.BalanceRefreshMinutes = 5
		}
		c.BalanceRefreshInterval = time.Duration(c.Schedule.BalanceRefreshMinutes) * time.Minute
	}
}

// normalizeAlerting 告警去重窗口与阈值下限归一。
func (c *Config) normalizeAlerting() {
	// 告警去重窗口兜底：<=0 回落 60 分钟（同一账号同类事件 1h 内不重发）。
	if c.Alerting.DedupMinutes <= 0 {
		c.Alerting.DedupMinutes = 60
	}
	if c.Alerting.MinCreditThreshold < 0 {
		c.Alerting.MinCreditThreshold = 0
	}
	if c.Alerting.ConsecutiveFailures < 0 {
		c.Alerting.ConsecutiveFailures = 0
	}
}
