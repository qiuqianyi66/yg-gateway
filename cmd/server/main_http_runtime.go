// main_http_runtime.go 管理面板与 HTTP handler 的装配（从 main() 切出：
// 原 84 行内联装配，含 F6 晚绑定闭包与 F7 provider 汇总）。
package main

import (
	"io"
	"log"
	"os"
	"time"

	"yg-gateway/internal/alerting"
	"yg-gateway/internal/livecfg"
	"yg-gateway/internal/logfmt"
	"yg-gateway/internal/opsagent"
	"yg-gateway/internal/panel"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/scheduler"
	"yg-gateway/internal/server"
	"yg-gateway/internal/session"
	"yg-gateway/internal/upstream"
	"yg-gateway/internal/usage"
)

// httpRuntimeDeps 是 HTTP 面装配所需的已构造部件（main 持有）。
// 用结构体而非长位置参数：字段名即语义，新增部件不需要改调用点顺序。
type httpRuntimeDeps struct {
	cfg        *Config
	cfgPath    string
	p          *pool.Pool
	up         *upstream.Client
	sch        *scheduler.Scheduler
	rec        *usage.Recorder
	live       *livecfg.Holder
	requestLog *reqlog.Recorder
	opsAg      *opsagent.Agent
	alert      *alerting.Notifier
	obs        *observability
	redisMode  string
	sessRouter *session.Router
	sessCount  func() int
	sessSnap   func() []session.SessionBinding
	providers  map[string]server.ProviderEndpoint
}

// buildHTTPRuntime 装配管理面板 + HTTP handler + 协议漂移观测器。
//
// 装配顺序是语义的一部分：panel 必须先于 handler（handler 持有 Panel 引用）；
// 漂移观测器在 panel 之后构造；guardrail 校验在 handler 之前 fail fast。
// F6 的 per-key RPM 快照是晚绑定闭包：panel.New 时 handler 尚未存在，先用占位
// 闭包，handler 创建后再改写。
func buildHTTPRuntime(d httpRuntimeDeps) (*server.Handler, *upstream.DriftMonitor) {
	// keyRPMStatsFn 是 panel 消费 per-key RPM 观测的晚绑定闭包：server.NewHandler
	// 在 panel.New 之后才创建，用闭包变量先占位后接线（F6）。
	keyRPMStatsFn := func() any { return nil }
	pn := panel.New(panel.Config{
		Pool:            d.p,
		Usage:           d.rec,
		CreditCNYRate:   d.cfg.Usage.CreditCNYRate,
		Pricing:         buildUSDPriceMap(d.cfg.Usage.Pricing),
		RequestLog:      d.requestLog,
		Upstream:        d.up,
		Scheduler:       d.sch,
		OpsAgent:        d.opsAg,
		AuthDir:         d.cfg.AuthDir,
		APIKey:          d.cfg.APIKey,
		RedisMode:       d.redisMode,
		StickyCount:     d.sessCount,
		SessionSnapshot: d.sessSnap,
		Version:         appVersion,
		Live:            d.live,
		KeyRPMStats:     func() any { return keyRPMStatsFn() },                                  // F6：handler 创建后接线
		ProviderSummary: func() any { return providerSummaryView(d.cfg.Providers, d.p.List()) }, // F7：provider 汇总条
		// 模型上限探测数据（scripts/probe_max_tokens.py --panel-out 写入）：
		// 与 state 文件同目录，缺省 data/output_probes.json。
		ProbeFile:  stateSibling(d.cfg.StateFile, "output_probes.json"),
		ConfigPath: d.cfgPath,
		LoadConfig: func() (any, error) {
			return loadConfigMasked(d.cfgPath)
		},
		SaveConfig: func(raw []byte) ([]string, error) {
			return saveConfig(raw, d.cfgPath, d.live, d.p, d.up, d.sch)
		},
		// Key 重置（面板 Key 卡片「接入配置 → 重置密钥」）：前端不持有明文，
		// 走服务端直接改目标条目（见 resetAPIKey），其他 Key 原样保留。
		ResetKey: func(id, newKey string) error {
			return resetAPIKey(d.cfgPath, id, newKey, d.live, d.p, d.up, d.sch)
		},
	})
	// 成长任务队列每日自动执行（与「执行全部待办」同管线）：Sequential 族零点解锁后
	// 无需手动扫描；hook 返回即启动（异步执行），已在跑时内部跳过。
	d.sch.SetGrowthHook(pn.RunGrowthQueueOnce)
	// 单行化写出口（2026-11-14，日志完整性）：log.Printf 的参数可能来自客户端
	// （model / 请求 ID / 上游 body），含换行就能伪造出第二条日志行。包在唯一写出口
	// 上，所有现在与将来的日志都自动压平，不依赖每个调用点记得清洗。
	log.SetOutput(logfmt.OneLineWriter{W: io.MultiWriter(os.Stderr, pn.Logs())})
	server.SetChatLogOutput(logfmt.OneLineWriter{W: io.MultiWriter(os.Stdout, pn.Logs())})

	// 协议漂移观测（R-102，P1）：启用时从快照文件恢复基线（缺失 = unknown），
	// /status 暴露状态；探测 goroutine 在校验循环外启动（main 内）。
	driftMon := buildDriftMonitor(d.cfg)

	// 上下文护栏（REQ-context-guardrail-2026-10-31 / ADR-019）：阈值来自 config
	// guardrails 块，0 = 关（零回归）。负值 fail fast（校验在 config_guardrail.go，
	// 同 opsagent 先例由装配侧调用）；Set* 热生效（当前启动生效；main 持有引用，
	// 后续接配置 reload 即可在线调档，无需改装配）。
	if err := d.cfg.Guardrails.validate(); err != nil {
		log.Fatalf("guardrails config: %v", err)
	}
	guardrail := server.NewGuardrail()
	guardrail.SetContextWarnTokens(d.cfg.Guardrails.ContextWarnTokens)
	guardrail.SetContextMaxTokens(d.cfg.Guardrails.ContextMaxTokens)
	guardrail.SetSessionRPM(d.cfg.Guardrails.SessionRPM)

	h := server.NewHandler(server.Config{
		Pool:     d.p,
		Upstream: d.up,
		APIKey:   d.cfg.APIKey,
		Session:  d.sessRouter,
		// 内容派生会话键开关（ADR-004）：关闭后只有显式会话标识才粘性。
		DisableSessionDerive: !d.cfg.SessionStickyDerive,
		StickyCount:          d.sessCount,
		RedisMode:            d.redisMode,
		SoftCooldown:         d.cfg.SoftRateDur,
		Panel:                pn,
		Live:                 d.live,
		Usage:                d.rec,
		Latency:              d.obs.latency,
		TTFB:                 d.obs.ttfb,
		ErrKinds:             d.obs.errKinds,
		Attempts:             d.obs.attempts,
		SlowRequests:         d.obs.slowReqs,
		SlowRequestThreshold: time.Duration(d.cfg.Observability.SlowRequestThresholdSeconds) * time.Second,
		RequestLog:           d.requestLog,
		PromptMode:           d.cfg.Prompt.Mode,
		PromptText:           d.cfg.PromptText,
		// 来源记录开关经 livecfg 热生效；此处同时填静态字段，供 Live 为 nil 的
		// 裸用/测试路径拿到同一缺省值。
		RecordClientInfo: d.cfg.Logging.RequestClientInfo,
		// handler 侧第三道闸（global realm）：false（显式逃生门）时不列 global: 模型名。
		GlobalEnabled: d.cfg.Global.Enabled,
		// 入站边界加固（F-001/F-002/F-003）：装配期固定，改动需重启。
		MaxBodyBytes: d.cfg.MaxBodyBytesEffective,
		TrustedProxy: server.NewTrustedProxyMatcher(d.cfg.Security.TrustedProxy),
		ReadOnlyRateLimit: func() *server.ReadOnlyRateLimit {
			if d.cfg.Security.ReadOnlyRateLimit.Limit <= 0 || d.cfg.ReadOnlyRateLimitWindow <= 0 {
				return nil
			}
			return &server.ReadOnlyRateLimit{
				Limit:  d.cfg.Security.ReadOnlyRateLimit.Limit,
				Window: d.cfg.ReadOnlyRateLimitWindow,
			}
		}(),
		// 异常外发告警（P0-4）：webhook URL 空 = 不告警（零回归）。
		Alerting: d.alert,
		// 备用上游级联（P2）：enabled=false 或 base_url 空 = 不启用（零回归）。
		Fallback: buildFallbackConfig(d.cfg),
		// 多 provider 出站端点表（ADR-026）：空 map = 仅 codebuddy（零回归）。
		Providers: d.providers,
		// 协议漂移观测（R-102）：未启用 = nil（/status 省略该键 = 外层 unknown）。
		Drift: driftMon,
		// 上下文护栏（REQ-context-guardrail）：阈值 0 = 关（零回归）；实例在此持有
		// 引用（guardrail 局部变量），Set* 已暴露供热调档。
		Guardrail: guardrail,
	})
	// F6：handler 创建后把 per-key RPM 快照接线给 panel（晚绑定闭包）。
	keyRPMStatsFn = func() any { return h.KeyRPMStats() }
	return h, driftMon
}
