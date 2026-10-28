// main.go yg-gateway 入口：加载配置、构建 pool、起调度器与 HTTP 服务。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/livecfg"
	"yg-gateway/internal/metrics"
	"yg-gateway/internal/panel"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/redisstore"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/scheduler"
	"yg-gateway/internal/server"
	"yg-gateway/internal/session"
	"yg-gateway/internal/unknown"
	"yg-gateway/internal/upstream"
	"yg-gateway/internal/usage"
)

// appVersion 网关版本，透出到 /panel/api/overview（面板左下角显示）。
//
// 独立化后从 2.0.0 起号，明确区别于上游 1.x 线：
//   - 2.0.0 = 脱离上游 fork 的首个版本（module 改名、身份标识改名、构建工具链升级）
//   - 此后本仓库独立演进，不再与上游版本号对齐
//
// 注意：CI 发布时会断言 tag == 本值（剥掉后缀后比对），改这里要同步打 tag。
const appVersion = "2.0.0"

// usagePathFor 由 state 文件路径推出用量文件路径：同目录、文件名 usage.json。
// 这样 config 里改 state_file 时用量数据跟着走，不需要额外配置项。
func usagePathFor(stateFile string) string { return stateSibling(stateFile, "usage.json") }

// stateSibling 返回与 state 文件同目录的指定文件名路径（相对路径场景回落当前目录）。
// usage.json（用量记录）与 output_probes.json（模型上限探测）共用本规则。
func stateSibling(stateFile, name string) string {
	dir := filepath.Dir(stateFile)
	if dir == "" || dir == "." {
		return name
	}
	return filepath.Join(dir, name)
}

// buildDriftMonitor 装配协议漂移观测器（R-102）。未启用返回 nil（/status 省略键）。
// 快照损坏仅记 WARN 并继续（观测层是旁路，不拖死主链路）。
func buildDriftMonitor(cfg *Config) *upstream.DriftMonitor {
	if !cfg.Drift.Enabled {
		return nil
	}
	snapPath := cfg.Drift.SnapshotPath
	if snapPath == "" {
		snapPath = stateSibling(cfg.StateFile, "protocol-snapshot.json")
	}
	mon := upstream.NewDriftMonitor(snapPath)
	if err := mon.Load(); err != nil {
		log.Printf("WARN: [drift] snapshot %s unreadable, reseeding: %v", snapPath, err)
	}
	return mon
}

// buildFallbackConfig 组装备用上游级联配置（P2/R-107）。未启用时返回 nil。
// 配置优先级：endpoints 列表（多端点级联）→ 单端点旧字段（向后兼容）。
func buildFallbackConfig(cfg *Config) *server.FallbackConfig {
	if !cfg.Fallback.Enabled {
		return nil
	}
	buildEndpoint := func(baseURL, apiKey string, models []string, timeout int) (server.FallbackEndpoint, bool) {
		if baseURL == "" || apiKey == "" {
			return server.FallbackEndpoint{}, false
		}
		mm := make(map[string]bool, len(models))
		for _, m := range models {
			mm[m] = true
		}
		return server.FallbackEndpoint{BaseURL: baseURL, APIKey: apiKey, Models: mm, TimeoutSeconds: timeout}, true
	}
	var eps []server.FallbackEndpoint
	for i := range cfg.Fallback.Endpoints {
		if ep, ok := buildEndpoint(cfg.Fallback.Endpoints[i].BaseURL, cfg.Fallback.Endpoints[i].APIKey,
			cfg.Fallback.Endpoints[i].Models, cfg.Fallback.Endpoints[i].TimeoutSeconds); ok {
			eps = append(eps, ep)
		}
	}
	// 未配列表：回退单端点旧字段（旧配置文件零回归）。
	if len(eps) == 0 {
		if ep, ok := buildEndpoint(cfg.Fallback.BaseURL, cfg.Fallback.APIKey, cfg.Fallback.Models, cfg.Fallback.TimeoutSeconds); ok {
			eps = []server.FallbackEndpoint{ep}
		}
	}
	if len(eps) == 0 {
		return nil
	}
	return &server.FallbackConfig{Endpoints: eps}
}

// configFileGenuinelyMissing 判定「config 文件本身不存在」。
//
// 不能只凭 Load 的错误：Load 里 normalizePrompt 等校验会读取 prompt.file 等附属
// 文件，这些文件缺失同样返回 fs.ErrNotExist 包装错误；此时 config 文件其实是
// 存在的，若走 auto-generate 分支会把正常配置整体丢弃成默认值（账号池清空、
// api_key 丢失、面板全 0）。只有 os.Stat(configPath) 本身报告不存在，才属于
// 首次运行、应自动生成推荐配置。
func configFileGenuinelyMissing(configPath string, loadErr error) bool {
	if !errors.Is(loadErr, fs.ErrNotExist) {
		return false
	}
	_, statErr := os.Stat(configPath)
	return statErr != nil && errors.Is(statErr, fs.ErrNotExist)
}

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径（默认当前目录 config.json；不存在时自动生成推荐配置）")
	flag.Parse()

	cfg, err := Load(*cfgPath)
	if err != nil {
		// errors.Is 才能看穿 Load 里 fmt.Errorf("%w") 的包装；os.IsNotExist 不行。
		// 注意：必须验证 config 文件本身缺失，不能只看错误类型——prompt.file 等
		// 附属文件缺失同样报 ErrNotExist，误判会丢弃正常配置（2026-10-23 实弹）。
		if configFileGenuinelyMissing(*cfgPath, err) {
			// 首次运行：目录下没有配置 → 自动落一份推荐配置（含随机 api_key）再加载。
			// 双击 exe / 裸跑 docker 即开，无需先手工复制样例。
			if key, werr := WriteDefault(*cfgPath); werr == nil {
				log.Printf("config %s 不存在，已生成推荐配置（api_key=%s，记录在该文件里，可自行修改）", *cfgPath, key)
				cfg, err = Load(*cfgPath)
			}
			if err != nil {
				// 生成失败（目录只读等）：退回纯默认 + env（旧行为兜底），不阻塞启动。
				log.Printf("config %s not found (auto-generate failed), using defaults+env: %v", *cfgPath, err)
				cfg, err = Load("")
			}
		}
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
	}

	// 启动期装配（账号扫描 + 池构造 + 加载错误判定）抽成函数：main() 里的 log.Fatalf 不可测，
	// 而「扫描不完整只增不删」「加载失败拒绝启动」这两条语义必须被测试覆盖。
	auths, skippedAuths, p, err := loadAccountsAndPool(cfg)
	if err != nil {
		log.Fatalf("%v", err)
	}

	// redisstore：未配置/连接失败 → Noop（纯内存模式，一切功能照常）。
	store := redisstore.New(cfg.Upstash.URL, cfg.Upstash.Token)
	// 停机序：先 pool.Close()（最后一次 Flush → SaveState 已提交到 store），
	// 再 store.Close() 排空在途异步写（最后一笔 Redis 镜像必须写完才关连接）。
	defer func() {
		p.Close()
		// 关闭失败只记 WARN：进程已在退出，无法补救；但不该完全不可见——
		// 超时放弃排空意味着最后一笔 Redis 状态镜像可能没写完（判据②+ 可观测性）。
		if err := store.Close(); err != nil {
			log.Printf("WARN: [main] store.Close 失败（Redis 镜像可能未排空）: %v", err)
		}
	}()
	p.SetStore(store)
	p.RestoreFromSnapshot() // 择新恢复：Redis 快照比本地新才采用，否则本地优先
	// 与 auths 目录对齐：新账号加入、已删除文件账号剔除（状态保留）。
	// 扫描不完整（有坏文件）时退化为「只增不删」——读不到的账号不是「被删的账号」。
	if len(skippedAuths) > 0 {
		p.SyncToDirIncomplete(auths, len(skippedAuths))
	} else {
		p.SyncToDir(auths)
	}

	// 熔断器 + 在途上限（含 global 分档）+ 连败降权 + 闲置补偿调优（从 config 注入，
	// 非正值回退默认）。
	p.SetBreaker(cfg.Pool.BreakerThreshold, cfg.BreakerCooldownDur, cfg.BreakerCooldownMaxD)
	p.SetMaxInFlight(cfg.Pool.MaxInFlight)
	p.SetMaxInFlightGlobal(cfg.Pool.MaxInFlightGlobal) // global 域 WAF 风控分档（P1-1）
	p.SetDegrade(cfg.Pool.DegradeThreshold, cfg.DegradeCooldownDur, cfg.DegradeCooldownMaxD)
	p.SetSoftRateMax(cfg.SoftRateMaxDur)                 // 软冷却指数退避封顶（soft_rate_max，默认 2h）
	p.SetCostExploreInterval(cfg.CostExploreIntervalDur) // costTier 探索窗口（issue #136，默认 30m；0 关停）
	p.SetBreakerProbeGap(cfg.BreakerProbeGapDur)         // 熔断 half-open 试探间隔（ADR-003 D-400，默认 30s；0 关闭）
	p.SetGlobalMaxInFlight(cfg.Pool.GlobalMaxInFlight)   // 全局在途上限（ADR-003 D-401，默认 0 = 不限）
	p.SetCreditFloor(cfg.Pool.CreditFloor)               // 积分保底（默认 0 = 关闭）
	p.SetWeights(cfg.Pool.IdleWeightPerHour, cfg.Pool.IdleWeightMax)
	p.SetPreferExpiring(cfg.Pool.PreferExpiring)
	p.SetEwma(cfg.Pool.Ewma.Enabled, cfg.Pool.Ewma.Alpha, cfg.Pool.Ewma.MinSamples) // EWMA 表现回灌（ADR-005 D-502，默认关 = 零回归）
	p.SetRecentUsageFactor(cfg.Pool.RecentUsageFactor)                              // 近期用量衰减（REQ-routing P2，默认关）
	p.SetPaceMode(cfg.Pool.PaceMode)                                                // pace 选号（F9，默认关 = 零回归）

	// 会话粘性路由（可配关闭）。
	var sessRouter *session.Router
	redisMode := "noop"
	if _, ok := store.(redisstore.Noop); !ok {
		redisMode = "upstash"
	}
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
		defer sessRouter.StopGC()
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

	up := upstream.New()

	// 积分保底的「收费」兜底判据：接上游模型目录的积分倍率表。本地实测台账无观测
	// 时用它判收费——否则「没学过」恒等于「放行」，高价新模型会把触底号一笔打穿
	// （kimi-k3-1 实案：全池无观测 → 保底全放行 → 两笔打穿并硬冷却到次日 04:00）。
	// 位于 up 装配之后：倍率表由探测下发，闭包每次调用读实时快照。
	p.SetModelRateOf(func(realm, model string) string { return up.ModelRate(realm, model) })

	// 短 RPC 总时长上限（refresh/checkin/balance/FetchModels），语义不变。
	up.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	// 聊天 SSE 首字节前（响应头）上限：cfg 已 normalize（缺省回落 timeout_seconds）。
	up.HeaderTimeout = time.Duration(cfg.Upstream.HeaderTimeoutSeconds) * time.Second
	if tr, ok := up.ChatHTTP.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = up.HeaderTimeout
	}
	// 聊天 SSE 流中空闲上限（S3 空闲监控读取）。
	up.IdleTimeout = time.Duration(cfg.Upstream.IdleTimeoutSeconds) * time.Second
	// 整请求总时长上限（F-707，三件套第三层；<=0 = 不限 = 零回归）。
	up.StreamTotalTimeout = time.Duration(cfg.Upstream.StreamTotalTimeoutSeconds) * time.Second
	// SSE 心跳间隔（F-706；<=0 = 关闭 = 零回归）。
	// 透给 handler 的流式路径：StreamHint 静默超该值就写 `: ping` 注释帧。
	upstream.SetSSEKeepAlive(time.Duration(cfg.Upstream.SSEKeepAliveSeconds) * time.Second)
	up.SanitizeFingerprints.Store(cfg.Features.SanitizeBlacklistFingerprints)
	// 出站 UA 与归属头（issue #42 + 上游同步）：
	// UserAgent 非空则完全覆盖；ClientVersion/CliVersion 缺省对齐官方形态；
	// ClientName 非空时 chat 路径注入 X-IDE-* 四头（用量归因对齐官方桌面端）。
	up.UserAgent = cfg.Upstream.UserAgent
	up.ClientVersion = cfg.Upstream.ClientVersion
	up.CliVersion = cfg.Upstream.CliVersion
	up.ClientName = cfg.Upstream.ClientName
	up.DeviceToken = cfg.Upstream.DeviceToken
	up.DeviceTokenFile = cfg.Upstream.DeviceTokenFile
	up.PassthroughIP = cfg.Upstream.PassthroughIP
	// global realm 路由（config global 段）：上游侧开关（第一道闸）+ base 覆盖；
	// auth 侧开关（auth.SetGlobalEnabled）是第二道闸，两者同 config global.enabled。
	up.GlobalEnabled = cfg.Global.Enabled
	up.ChatBaseGlobal = cfg.Global.ChatBase
	up.BillingBaseGlobal = cfg.Global.BillingBase
	auth.SetGlobalEnabled(cfg.Global.Enabled)
	// model.json 本地缓存接线（context_length/max_output_tokens 四级查找链第 3 级）：
	// 数据目录与 state.json 同风格（Docker volume 持久化路径）。首次缺失/损坏自动
	// 回落仓库内嵌种子；models.dev 按需拉取成功后原子写回。
	upstream.SetModelCatalogPath(stateSibling(cfg.StateFile, "model.json"))

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
	})
	switch {
	case !cfg.Schedule.CheckinEnabled:
		log.Printf("签到已禁用（schedule.checkin_enabled=false）")
	default:
		log.Printf("签到已启用：%v 点（签到 + 余额查询解冻）", cfg.Schedule.CheckinHours)
	}
	switch {
	case !cfg.Schedule.TravelEnabled:
		log.Printf("猫猫旅行已禁用（schedule.travel_enabled=false）")
	default:
		log.Printf("猫猫旅行已启用：%v 点（独立排程：领养 / 派出 / 领奖）", cfg.Schedule.TravelHours)
	}
	switch {
	case !cfg.Schedule.ActivityEnabled:
		log.Printf("活跃上报已禁用（schedule.activity_enabled=false）")
	default:
		log.Printf("活跃上报已启用：%v 点（每日 1 次，点亮连登 + 解锁 first_buddy）", cfg.Schedule.ActivityHours)
	}
	if !cfg.Schedule.KeepaliveEnabled {
		log.Printf("token 保活已禁用（schedule.keepalive_enabled=false）")
	} else {
		log.Printf("token 保活已启用：%v 点", cfg.Schedule.KeepaliveHours)
	}
	switch {
	case !cfg.Schedule.BlackcatEnabled:
		log.Printf("夜猫子已禁用（schedule.blackcat_enabled=false）")
	default:
		log.Printf("夜猫子已启用：%v 点（23:00–08:00 窗口 glm-5.2 对话补足）", cfg.Schedule.BlackcatHours)
	}
	switch {
	case !cfg.Schedule.BalanceRefreshEnabled:
		log.Printf("余额后台刷新已禁用（schedule.balance_refresh_enabled=false）")
	case cfg.BalanceRefreshInterval > 0:
		log.Printf("余额后台刷新：每 %s（签到时点照常额外刷新）", cfg.BalanceRefreshInterval)
	}
	if cfg.Schedule.IncludeDisabledInTasks {
		log.Printf("保号任务覆盖禁用账号（schedule.include_disabled_in_tasks=true）：禁用号仍签到 / 活跃 / 保活 / 刷新余额，但不参与选号")
	}

	// 管理面板日志镜像：标准 log（stderr）与 chat 表格日志（stdout）双路复制进
	// 面板环形缓冲，供 /panel/api/logs 读取；控制台输出行为完全不变。
	// live 承载可热改字段（api_key/多 key/soft_rate/脱敏开关），面板保存配置时在线替换。
	live := livecfg.New(buildAuthSnapshot(cfg))
	// 用量记录器：与 state 文件同目录，随 state_file 配置一起搬移。
	// datapath 由 state 文件路径推出，避免再加一个配置项。
	usagePath := usagePathFor(cfg.StateFile)
	rec, err := newUsageRecorder(usagePath, cfg.Usage.WalCheckpointAfter)
	// ADR-009：用量台账是选号与成本控制的输入，加载失败不能「从零开始」继续跑
	// （历史上只打日志，等于静默丢掉全部台账）。版本比程序新（典型是回滚到旧版）
	// 与文件损坏都在这里变成**拒绝启动**，让人看见而不是让数据被无声污染。
	if err != nil {
		log.Fatalf("usage ledger: %v", err)
	}
	rec.Start()
	defer rec.Stop()
	log.Printf("[usage] 逐请求用量记录已启用: %s (%s)", usagePath, rec.Describe())

	// 延迟分位数观测（ADR-003 D-403）：自实现固定分桶，零新依赖。
	// 进程级单例（无需落盘，重启归零——分位数是近期健康度而非历史账本）。
	latencyReg := metrics.NewRegistry()
	// 首 token （TTFB）分位数与上游错误分类计数：同源自实现，同样进程级单例。
	// TTFB 与端到端延迟分开记——两者回答的问题不同（"总共等多久" vs "上游多快
	// 开始吐"）；错误分类则把既有 Classify 的 13 种 ErrKind 导出成时序指标。
	ttfbReg := metrics.NewRegistry()
	errKinds := metrics.NewKindCounters()
	// 重试放大率直方图与慢请求计数器（F-305）：同源自实现，进程级单例。
	// 放大率回答「每个用户请求触发了几次上游调用」；慢请求是端到端超阈值的
	// 显式告警信号——两者都是纯观测，不改写请求路径。
	attemptsStats := metrics.NewAttemptsStats()
	slowReqs := metrics.NewCounter()

	// 未知条目注册表（AGENTS.md「Unknown 消解纪律」）：把「某号某模型成本未知」
	// 这类可消解的事实变成一等公民实体——登记时写明还需要什么，实测 credit 时
	// 自动消解。进程级单例（重启归零：未知本来就是"当前还不知道"的语义）。
	unknownReg := unknown.NewRegistry()
	p.SetUnknownRegistry(unknownReg)

	// 外发告警 + 运维 agent（REQ-opsagent / ADR-017）：二者共用 notifier（去重窗口
	// 一致）；agent 实例传给面板（nil = 未启用，面板有引导态）。enabled=false 时
	// buildOpsAgent 返回 nil、startOpsAgent no-op（零回归）。
	alertNotifier := newAlertNotifier(cfg)
	opsAg := buildOpsAgent(cfg.OpsAgent, p, unknownReg, alertNotifier)

	// 请求指标始终启用；JSONL 归档只写脱敏元数据，写盘失败不影响聊天请求。
	requestLog := reqlog.New(reqlog.Config{
		Dir:           stateSibling(cfg.StateFile, "request-logs"),
		Enabled:       cfg.Logging.RequestArchiveEnabled,
		RetentionDays: cfg.Logging.RequestRetentionDays,
		MaxBytes:      int64(cfg.Logging.RequestArchiveMaxMB) << 20,
	})
	defer requestLog.Close()
	rs := requestLog.Snapshot().Archive
	if rs.Enabled {
		log.Printf("[reqlog] 请求指标已启用；JSONL 归档 %s（保留 %d 天，上限 %d MiB）",
			rs.Dir, cfg.Logging.RequestRetentionDays, cfg.Logging.RequestArchiveMaxMB)
	} else {
		log.Printf("[reqlog] 请求指标已启用；JSONL 归档已关闭")
	}

	// keyRPMStatsFn 是 panel 消费 per-key RPM 观测的晚绑定闭包：server.NewHandler
	// 在 panel.New 之后才创建（main.go:440），用闭包变量先占位后接线（F6）。
	keyRPMStatsFn := func() any { return nil }
	pn := panel.New(panel.Config{
		Pool:            p,
		Usage:           rec,
		CreditCNYRate:   cfg.Usage.CreditCNYRate,
		Pricing:         buildUSDPriceMap(cfg.Usage.Pricing),
		RequestLog:      requestLog,
		Upstream:        up,
		Scheduler:       sch,
		OpsAgent:        opsAg,
		AuthDir:         cfg.AuthDir,
		APIKey:          cfg.APIKey,
		RedisMode:       redisMode,
		StickyCount:     sessCount,
		SessionSnapshot: sessSnapshot,
		Version:         appVersion,
		Live:            live,
		KeyRPMStats:     func() any { return keyRPMStatsFn() }, // F6：handler 创建后接线
		// 模型上限探测数据（scripts/probe_max_tokens.py --panel-out 写入）：
		// 与 state 文件同目录，缺省 data/output_probes.json。
		ProbeFile:  stateSibling(cfg.StateFile, "output_probes.json"),
		ConfigPath: *cfgPath,
		LoadConfig: func() (any, error) {
			return loadConfigMasked(*cfgPath)
		},
		SaveConfig: func(raw []byte) ([]string, error) {
			return saveConfig(raw, *cfgPath, live, p, up, sch)
		},
		// Key 重置（面板 Key 卡片「接入配置 → 重置密钥」）：前端不持有明文，
		// 走服务端直接改目标条目（见 resetAPIKey），其他 Key 原样保留。
		ResetKey: func(id, newKey string) error {
			return resetAPIKey(*cfgPath, id, newKey, live, p, up, sch)
		},
	})
	// 成长任务队列每日自动执行（与「执行全部待办」同管线）：Sequential 族零点解锁后
	// 无需手动扫描；hook 返回即启动（异步执行），已在跑时内部跳过。
	sch.SetGrowthHook(pn.RunGrowthQueueOnce)
	log.SetOutput(io.MultiWriter(os.Stderr, pn.Logs()))
	server.SetChatLogOutput(io.MultiWriter(os.Stdout, pn.Logs()))

	// 协议漂移观测（R-102，P1）：启用时从快照文件恢复基线（缺失 = unknown），
	// /status 暴露状态；探测 goroutine 在校验循环外启动（下方）。
	driftMon := buildDriftMonitor(cfg)

	h := server.NewHandler(server.Config{
		Pool:     p,
		Upstream: up,
		APIKey:   cfg.APIKey,
		Session:  sessRouter,
		// 内容派生会话键开关（ADR-004）：关闭后只有显式会话标识才粘性。
		DisableSessionDerive: !cfg.SessionStickyDerive,
		StickyCount:          sessCount,
		RedisMode:            redisMode,
		SoftCooldown:         cfg.SoftRateDur,
		Panel:                pn,
		Live:                 live,
		Usage:                rec,
		Latency:              latencyReg,
		TTFB:                 ttfbReg,
		ErrKinds:             errKinds,
		Attempts:             attemptsStats,
		SlowRequests:         slowReqs,
		SlowRequestThreshold: time.Duration(cfg.Observability.SlowRequestThresholdSeconds) * time.Second,
		RequestLog:           requestLog,
		PromptMode:           cfg.Prompt.Mode,
		PromptText:           cfg.PromptText,
		// 来源记录开关经 livecfg 热生效；此处同时填静态字段，供 Live 为 nil 的
		// 裸用/测试路径拿到同一缺省值。
		RecordClientInfo: cfg.Logging.RequestClientInfo,
		// handler 侧第三道闸（global realm）：false（显式逃生门）时不列 global: 模型名。
		GlobalEnabled: cfg.Global.Enabled,
		// 入站边界加固（F-001/F-002/F-003）：装配期固定，改动需重启。
		MaxBodyBytes: cfg.MaxBodyBytesEffective,
		TrustedProxy: server.NewTrustedProxyMatcher(cfg.Security.TrustedProxy),
		ReadOnlyRateLimit: func() *server.ReadOnlyRateLimit {
			if cfg.Security.ReadOnlyRateLimit.Limit <= 0 || cfg.ReadOnlyRateLimitWindow <= 0 {
				return nil
			}
			return &server.ReadOnlyRateLimit{
				Limit:  cfg.Security.ReadOnlyRateLimit.Limit,
				Window: cfg.ReadOnlyRateLimitWindow,
			}
		}(),
		// 异常外发告警（P0-4）：webhook URL 空 = 不告警（零回归）。
		Alerting: alertNotifier,
		// 备用上游级联（P2）：enabled=false 或 base_url 空 = 不启用（零回归）。
		Fallback: buildFallbackConfig(cfg),
		// 协议漂移观测（R-102）：未启用 = nil（/status 省略该键 = 外层 unknown）。
		Drift: driftMon,
	})
	// F6：handler 创建后把 per-key RPM 快照接线给 panel（晚绑定闭包）。
	keyRPMStatsFn = func() any { return h.KeyRPMStats() }

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sch.Run(ctx)
	sch.StartBalanceRefresh(ctx, cfg.BalanceRefreshInterval)
	// 运维 agent（REQ-opsagent-2026-10-24 / ADR-017）：enabled=false 时 no-op。
	startOpsAgent(opsAg, ctx)
	startKeyUsageAlerts(cfg.APIKeys, rec, alertNotifier, ctx) // key 用量告警（V3-2，无配置项零回归）

	// 启动即预热模型积分倍率表：倍率只在 FetchModels/FetchGlobalModelInfos 成功时填充
	//（两者均懒触发），重启后到首次 /v1/models 或面板模型页被访问之前，ModelRate 恒空串——
	// 积分保底的目录兜底在这段空窗期内形同虚设，触底号会被当成「收费未知」放行并打穿
	//（实测：重启后 2 分钟，97 分账号打收费模型归零；倍率表当时尚未建立）。
	// 异步执行：不阻塞监听启动；失败仅记日志（下一轮懒触发或本轮重试仍可补上）。
	go warmModelRates(ctx, up, p)

	// 协议漂移周期探测（R-102）：按间隔探模型目录端点形状，喂给观测器。
	// ctx 取消即退出（R9 回收路径）；探测失败不阻塞主链路。
	if driftMon != nil {
		interval := time.Duration(cfg.Drift.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = 1800 * time.Second
		}
		go runDriftProbe(ctx, up, p, driftMon, interval)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		// ReadTimeout 覆盖整个请求读取（含 body 上传）：防慢速 body 拖死连接。
		// 请求体上限见 server.max_body_bytes（默认 64MiB，显式 0 才关闭；见 :384 传入
		// cfg.MaxBodyBytesEffective），超限在读阶段即被 MaxBytesReader 截断为 413。
		// ReadTimeout 缺省 300s（issue #100：旧固定
		// 60s 会掐掉大上下文/文件块经反代链的慢速上传，客户端收到
		// 400 "read body: ... i/o timeout"）；server.read_timeout="0" 显式关闭。
		// 改动需重启进程。
		ReadTimeout: cfg.ServerReadTimeoutDur,
		// IdleTimeout keep-alive 空闲连接回收：配合 chat 出站 ctx 传播防连接泄漏堆积。
		// 注意：SSE 流式响应期间连接非空闲，不受此项掐断；不设全局 WriteTimeout
		// （长流式生成合法时长可达数分钟，全局 WriteTimeout 会误杀在途 SSE）。
		IdleTimeout: 120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		p.Flush() // 信号触发：先落盘再做优雅停机
		// 排水窗口：给在途 SSE 请求时间完成。原 5s 太短——流式生成合法时长
		// 可达数分钟，5s 一到进程强退、在途请求被硬掐（客户端收到半截流/连接
		// 重置）。提到 30s：绝大多数请求（含典型 SSE）能正常完成；仍超时则
		// 强退（记录日志，运维能看到"有在途请求被中断"）。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[shutdown] 30s 排水窗口超时（在途请求被中断）: %v", err)
		} else {
			log.Printf("[shutdown] 在途请求全部完成，进程退出")
		}
	}()

	log.Printf("yg-gateway listening on %s (api_key=%v)，管理面板 http://127.0.0.1%s/panel/", cfg.Listen, cfg.APIKey != "", panelListenPath(cfg.Listen))
	// http.ErrServerClosed 是优雅停机的正常信号（signal → ctx 取消 → Shutdown
	// 把 ListenAndServe 踢出来）——走正常返回路径，所有 defer（usage 落盘、
	// requestLog.Close）执行。真实错误（端口占用等）才 Fatalf（os.Exit 会跳过 defer）。
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http: %v", err)
	}
	log.Printf("bye")
}

// warmModelRates 启动预热各域模型积分倍率表（供积分保底的目录兜底判定）。
//
// 为什么需要：倍率表只在 FetchModels（CN）/ FetchGlobalModelInfos（global）成功时
// 填充，两者都是懒触发（被 /v1/models 或面板模型页访问才跑）。重启后到首次触发
// 之间的空窗期里 ModelRate 恒返回空串，保底的目录兜底判不出收费，触底号会被
// 当成「收费未知」放行并打穿（实测：重启后 2 分钟，97 分的账号打收费模型归零）。
//
// 失败处理：单域失败只记 WARN（不阻塞、不致命——后续懒触发仍会补上）；global 域
// 仅在其路由开关开启时预热（逃生门关锁时按 CN 处理，无需探测）。
func warmModelRates(ctx context.Context, up *upstream.Client, p *pool.Pool) {
	// 预热不得拖住进程退出：ctx 取消（SIGINT/SIGTERM）时立刻放弃剩余域。
	if ctx.Err() != nil {
		return
	}
	// CN：有可用 CN 账号才拉（与面板 models 同口径，避免无谓上游调用）。
	if uids := p.AvailableUIDsForRealm("cn"); len(uids) > 0 {
		if a := p.AuthByUID(uids[0]); a != nil {
			if _, err := up.FetchModels(a); err != nil {
				log.Printf("WARN: [upstream] warm model rates (cn): %v", err)
			} else {
				log.Printf("[upstream] warm model rates: cn ok")
			}
		}
	}
	// global：独立目录端点（workbuddy.ai），倍率按 "global" 域键存储。
	if up.GlobalEnabled && ctx.Err() == nil {
		if uids := p.AvailableUIDsForRealm("global"); len(uids) > 0 {
			if a := p.AuthByUID(uids[0]); a != nil {
				// FetchGlobalModelInfos 无错误返回（内部负缓存自行节流），
				// 仅按结果条数判断是否拿到目录。
				if infos := up.FetchGlobalModelInfos(a); len(infos) == 0 {
					log.Printf("WARN: [upstream] warm model rates (global): empty model list")
				} else {
					log.Printf("[upstream] warm model rates: global ok (%d models)", len(infos))
				}
			}
		}
	}
}

// driftCatalogFetch 拉模型目录条数（可测接缝：测试替换为假实现，免真上游）。
var driftCatalogFetch = func(c *upstream.Client, a *auth.Auth) (int, error) {
	infos, err := c.FetchModels(a)
	return len(infos), err
}

// driftProbeOnce 执行一次目录探测并喂给观测器（不含调度；空池/无号 = 跳过，不算失败）。
func driftProbeOnce(up *upstream.Client, p *pool.Pool, mon *upstream.DriftMonitor) {
	uids := p.AvailableUIDsForRealm("cn")
	if len(uids) == 0 {
		return
	}
	a := p.AuthByUID(uids[0])
	if a == nil {
		return
	}
	n, err := driftCatalogFetch(up, a)
	mon.ObserveCatalog(n, err == nil)
	if err := mon.Save(); err != nil {
		log.Printf("WARN: [drift] snapshot save: %v", err)
	}
}

// runDriftProbe 协议漂移周期探测（R-102）：用池里一个可用号拉模型目录，把目录条数
// 与成败喂给漂移观测器。纯观测层：探测失败只影响观测状态，不动选号、不罚号。
// ctx 取消立即退出（R9）；首轮立即执行（启动后尽快播种/发现漂移），之后按间隔轮询。
func runDriftProbe(ctx context.Context, up *upstream.Client, p *pool.Pool, mon *upstream.DriftMonitor, interval time.Duration) {
	probe := func() {
		if ctx.Err() != nil {
			return
		}
		driftProbeOnce(up, p, mon)
	}
	probe()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe()
		}
	}
}

// panelListenPath 从 listen 地址提取 ":port" 形式，用于启动日志拼面板 URL
// （":7863" 或 "0.0.0.0:7863" → ":7863"；异常输入原样返回）。
func panelListenPath(listen string) string {
	for i := len(listen) - 1; i >= 0; i-- {
		if listen[i] == ':' {
			return listen[i:]
		}
	}
	return listen
}

// buildUSDPriceMap 把 config.ModelPrice 表转成 usage.USDPrice 表（装配期一次性）。
// 两者同形但分属不同包，避免 usage 包依赖 cmd/server 的配置结构。
func buildUSDPriceMap(in map[string]ModelPrice) map[string]usage.USDPrice {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]usage.USDPrice, len(in))
	for k, v := range in {
		out[k] = usage.USDPrice{Input: v.Input, Output: v.Output, CacheRead: v.CacheRead}
	}
	return out
}

// saveConfig 面板保存配置：校验 → 落盘 → 热应用 → 返回需重启的字段列表。
//
// 热生效范围（设计取舍）：
//   - api_key / cooldown.soft_rate / features.sanitize_blacklist_fingerprints → livecfg 快照
//   - pool.* → pool.SetBreaker/SetMaxInFlight/SetSoftRateMax/SetWeights/SetCostExploreInterval/SetPreferExpiring/SetCreditFloor/SetEwma
//   - schedule.* → scheduler.Reconfigure/SetBalanceInterval/SetExpiringSoonWindow
//
// 需重启（监听地址、HTTP client 超时、auth_dir、告警器等装配期依赖）：完整清单
// 以 restartRequiredFields 函数体为唯一真相，此处仅摘要——新增装配期字段必须两处同步，
// 漏一处就是「面板显示已保存但不生效」的 R11 违例。摘要：
//   - listen / auth_dir / state_file / upstash / upstream.timeout_* / upstream.user_agent
//   - session_sticky.* / logging.request_archive_* / server.read_timeout / server.max_body_bytes
//   - security.trusted_proxy / security.read_only_rate_limit / alerting.*
//
// 落盘用"先写 tmp 再 rename"原子替换，且优先保留磁盘上的原始 JSON 结构（只改
// 面板表单覆盖到的键），避免把用户手写的注释性字段/未知键洗掉——这里直接整体
// 序列化校验后的配置，未知键在 json.Unmarshal 时已丢失，故先合并原始 map。
func saveConfig(raw []byte, path string, live *livecfg.Holder, p *pool.Pool, up *upstream.Client, sch *scheduler.Scheduler) ([]string, error) {
	// 1) 解析原始 JSON 为 map（保留用户手写的未知键），再叠加面板提交的键。
	oldRaw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read current config: %w", err)
	}
	var cur, incoming map[string]any
	if err := json.Unmarshal(oldRaw, &cur); err != nil {
		// 磁盘配置已损坏时不许降级成空 map 继续——那样会把用户手写的未知键整体洗掉。
		// 直接报错让用户先修文件（D-03）。
		return nil, fmt.Errorf("当前配置文件已损坏（%v），请先修复后再保存", err)
	}
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return nil, fmt.Errorf("parse submitted config: %w", err)
	}
	merged := mergeConfigMaps(cur, incoming)

	// 2) 校验（与启动同一套 Default+normalize），失败直接返回、不落盘。
	newCfg, err := ParseConfig(mergedJSON(merged))
	if err != nil {
		return nil, err
	}

	// 3) 落盘（原子替换）。
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// A single-file Docker bind mount cannot be renamed over its mount
		// target (Linux returns EBUSY / "device or resource busy"). Keep the
		// atomic path for regular files, but update the mounted file in place
		// for this specific deployment shape.
		if !errors.Is(err, syscall.EBUSY) {
			return nil, fmt.Errorf("replace config: %w", err)
		}
		f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
		if openErr != nil {
			_ = os.Remove(tmp)
			return nil, fmt.Errorf("replace config (bind mount fallback): %w", openErr)
		}
		_, writeErr := f.Write(out)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		// 写失败时保留 tmp（挂载文件已被 O_TRUNC 破坏，tmp 里是完整新内容，
		// 可手工恢复）；写成功才清理。
		if writeErr != nil {
			return nil, fmt.Errorf("replace config (bind mount fallback, 完整新内容保留在 %s): %w", tmp, writeErr)
		}
		_ = os.Remove(tmp)
		if closeErr != nil {
			return nil, fmt.Errorf("replace config (bind mount fallback): %w", closeErr)
		}
	}

	// 4) 热应用：能立即生效的字段全部应用，并列出仍需重启的字段。
	// 鉴权快照一次构建 + 一次原子切换（ADR-001 §六）：先在独立对象里完成
	// 摘要计算与索引构建，成功后 Store；失败则不落盘不切换（见下方 err 检查）。
	live.Store(buildAuthSnapshot(newCfg))
	up.SanitizeFingerprints.Store(newCfg.Features.SanitizeBlacklistFingerprints)
	p.SetBreaker(newCfg.Pool.BreakerThreshold, newCfg.BreakerCooldownDur, newCfg.BreakerCooldownMaxD)
	p.SetMaxInFlight(newCfg.Pool.MaxInFlight)
	p.SetMaxInFlightGlobal(newCfg.Pool.MaxInFlightGlobal)
	p.SetDegrade(newCfg.Pool.DegradeThreshold, newCfg.DegradeCooldownDur, newCfg.DegradeCooldownMaxD)
	p.SetSoftRateMax(newCfg.SoftRateMaxDur)
	p.SetCostExploreInterval(newCfg.CostExploreIntervalDur) // costTier 探索窗口热生效（0 关停）
	p.SetBreakerProbeGap(newCfg.BreakerProbeGapDur)         // half-open 试探间隔热生效（ADR-003 D-400）
	p.SetGlobalMaxInFlight(newCfg.Pool.GlobalMaxInFlight)   // 全局在途上限热生效（ADR-003 D-401）
	p.SetCreditFloor(newCfg.Pool.CreditFloor)               // 积分保底热生效（0 = 关闭）
	p.SetWeights(newCfg.Pool.IdleWeightPerHour, newCfg.Pool.IdleWeightMax)
	p.SetPreferExpiring(newCfg.Pool.PreferExpiring)
	p.SetEwma(newCfg.Pool.Ewma.Enabled, newCfg.Pool.Ewma.Alpha, newCfg.Pool.Ewma.MinSamples) // EWMA 表现回灌热生效（ADR-005 D-502）
	p.SetPaceMode(newCfg.Pool.PaceMode)                                                      // pace 选号热生效（F9）
	sch.SetExpiringSoonWindow(newCfg.ExpiringSoonDur)
	sch.Reconfigure(
		newCfg.Schedule.CheckinHours, newCfg.Schedule.TravelHours,
		newCfg.Schedule.ActivityHours, newCfg.Schedule.KeepaliveHours, newCfg.Schedule.BlackcatHours,
		newCfg.Schedule.GrowthHours,
		!newCfg.Schedule.CheckinEnabled, !newCfg.Schedule.TravelEnabled,
		!newCfg.Schedule.ActivityEnabled, !newCfg.Schedule.KeepaliveEnabled, !newCfg.Schedule.BlackcatEnabled,
		!newCfg.Schedule.GrowthEnabled)
	sch.SetBalanceInterval(newCfg.BalanceRefreshInterval)
	sch.SetIncludeDisabledInTasks(newCfg.Schedule.IncludeDisabledInTasks)

	return restartRequiredFields(newCfg), nil
}

// resetAPIKey 重置指定 Key 的密钥（面板 Key 卡片「接入配置 → 重置密钥」）。
// 前端不持有明文（loadConfigMasked 脱敏），全量提交 api_keys 会误删其他 Key
// （normalizeAPIKeys 过滤空 key 条目），故由服务端直接从 config 文件改目标条目：
// 读文件 → 改该 id 的 key → 序列化 → saveConfig（同一套校验+落盘+热应用，
// 鉴权索引重建走 buildAuthSnapshot）。其他 Key 与顶层 api_key 原样保留。
func resetAPIKey(path, id, newKey string, live *livecfg.Holder, p *pool.Pool, up *upstream.Client, sch *scheduler.Scheduler) error {
	id = strings.TrimSpace(id)
	newKey = strings.TrimSpace(newKey)
	if id == "" {
		return fmt.Errorf("id 不能为空")
	}
	if newKey == "" {
		return fmt.Errorf("新密钥不能为空")
	}
	if id == httpauth.SingleKeyID {
		return fmt.Errorf("id %q 是保留名（旧版单 api_key 的合成身份），不可重置", id)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	keys, ok := m["api_keys"].([]any)
	if !ok {
		return fmt.Errorf("未找到 Key %q（config 无 api_keys）", id)
	}
	found := false
	for _, k := range keys {
		km, ok := k.(map[string]any)
		if !ok {
			continue
		}
		if km["id"] == id {
			km["key"] = newKey
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("未找到 id=%q 的 Key", id)
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	_, err = saveConfig(out, path, live, p, up, sch)
	return err
}

// loadConfigMasked 加载配置并脱敏密钥明文，供面板 GET /panel/api/config 回显。
//
// 阻断项 2（第一轮）：不得把 api_key / api_keys[].key 明文直接返回给面板。
// 阻断项 1（第二轮）：脱敏占位符会被前端 collectConfig 当真实值提交，覆盖原密钥。
// 因此 api_key 字段**留空**（前端空值 = 不修改），另加 api_key_configured 状态字段
// 让前端显示「已配置」；api_keys[].key 同样留空 + 每项带 key_configured。
func loadConfigMasked(path string) (any, error) {
	c, err := Load(path)
	if err != nil {
		return nil, err
	}
	// 序列化成 map 再脱敏，避免依赖 *Config 的具体类型（panel 只当 any 用）。
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	// api_key：留空（不返回真实值，也不返回占位符，前端空 = 不修改），
	// 另加 api_key_configured 状态（前端据此显示「已配置」而不误提交）。
	if v, ok := m["api_key"]; ok {
		if s, _ := v.(string); s != "" {
			m["api_key"] = ""
			m["api_key_configured"] = true
		} else {
			m["api_key_configured"] = false
		}
	}
	// api_keys[].key：留空 + 每项 key_configured 状态。
	if keys, ok := m["api_keys"].([]any); ok {
		for _, k := range keys {
			if km, ok := k.(map[string]any); ok {
				if s, _ := km["key"].(string); s != "" {
					km["key"] = ""
					km["key_configured"] = true
				} else {
					km["key_configured"] = false
				}
			}
		}
	}
	return m, nil
}

// buildAuthSnapshot 把 Config 的鉴权配置（单 api_key + 多 api_keys）转成运行时快照。
//
// 这是热更新的原子单元：在这里完成「配置 → 摘要 → 索引」的全部构建，返回的
// Snapshot 由调用方一次 Store（ADR-001 §六）。APIKeys 已 normalize 过（空条目已过滤、
// id/realm 已校验），这里只做摘要计算与运行时 Key 组装。
//
// 单 api_key 也作为一个合成条目（ID="single"）纳入索引：这样「单 Key + 多 Key 并存
// 时两者都放行」由索引统一承载，鉴权侧无需区分来源（ADR-001 §四 兼容策略表第 3 行）。
func buildAuthSnapshot(c *Config) livecfg.Snapshot {
	keys := make([]httpauth.Key, 0, len(c.APIKeys)+1)
	if c.APIKey != "" {
		sum := sha256.Sum256([]byte(c.APIKey))
		keys = append(keys, httpauth.Key{ID: httpauth.SingleKeyID, Digest: sum[:]})
	}
	for _, k := range c.APIKeys {
		// Enabled=nil/true=启用；false=禁用（不进索引，但保留在 config.json 里以便面板管理）
		if k.Enabled != nil && !*k.Enabled {
			continue
		}
		sum := sha256.Sum256([]byte(k.Key))
		keys = append(keys, httpauth.Key{
			ID:                k.ID,
			Name:              k.Name,
			Digest:            sum[:],
			Models:            k.Models,
			Realm:             k.Realm,
			RPM:               k.RPM,
			MonthlyTokenQuota: k.MonthlyTokenQuota,
			Group:             k.Group,
			AllowedCIDRs:      parseKeyCIDRs(k.AllowedCIDRs),
		})
	}
	snap := livecfg.BuildSnapshot(c.APIKey, keys)
	snap.SoftCooldown = c.SoftRateDur
	snap.SanitizeFingerprints = c.Features.SanitizeBlacklistFingerprints
	snap.RecordClientInfo = c.Logging.RequestClientInfo
	snap.ModelAliases = c.ModelAliases
	return snap
}

// parseKeyCIDRs 把配置态的 IP/CIDR 字符串转成运行时 *net.IPNet 列表（R-108）。
// 非法条目在 normalizeAPIKeys 阶段已 fail fast，正常路径不会走到这里；这里做防御性
// 跳过（不静默吞错——跳过的是「已校验通过」的清单，异常即返回空名单=不限，与
// 启动期已拒绝配置的语义不冲突）。裸 IP 按 /32 收进 CIDR。
func parseKeyCIDRs(specs []string) []*net.IPNet {
	if len(specs) == 0 {
		return nil
	}
	out := make([]*net.IPNet, 0, len(specs))
	for _, s := range specs {
		if _, ipnet, err := net.ParseCIDR(s); err == nil {
			out = append(out, ipnet)
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			out = append(out, &net.IPNet{IP: ip, Mask: fullMask(ip)})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fullMask 按 IP 族返回全量掩码（IPv4 /32，IPv6 /128），用于裸 IP 收为单主机 CIDR。
func fullMask(ip net.IP) net.IPMask {
	if ip.To4() != nil {
		return net.CIDRMask(32, 32)
	}
	return net.CIDRMask(128, 128)
}

// restartRequiredFields 返回本次改动中无法热生效、需要重启进程的字段名。
// 恒返回完整清单中的"与当前进程装配期依赖相关"的项——面板据此提示用户。
func restartRequiredFields(c *Config) []string {
	var out []string
	// 这些字段在进程内被监听地址/HTTP client/目录句柄等装配期对象捕获。
	if c.Listen != "" {
		out = append(out, "listen")
	}
	if c.AuthDir != "" {
		out = append(out, "auth_dir")
	}
	if c.StateFile != "" {
		out = append(out, "state_file")
	}
	out = append(out, "upstream.timeout_seconds", "upstream.header_timeout_seconds", "upstream.idle_timeout_seconds")
	// upstream.user_agent 在装配期被写进出站 client（main.go 的 up.UserAgent = ...），
	// 之后不再读取——不在 livecfg 热快照里，也无法热改。此前漏列，导致面板改完
	// 显示"已保存"却不提示需要重启，用户以为没生效（issue #102 附带发现 2）。
	out = append(out, "upstream.user_agent")
	if c.Upstash.URL != "" || c.Upstash.Token != "" {
		out = append(out, "upstash")
	}
	out = append(out, "session_sticky.ttl", "session_sticky.gc_interval")
	// session_sticky.enabled 同样是装配期字段：sessRouter 只在启动时按它创建
	// （见 main 的 session.New），运行期改它不会生效。此前漏列 → 面板保存后显示
	// "已保存并立即生效"却不生效，用户以为开着粘性其实没开（R11 违反）。
	out = append(out, "session_sticky.enabled")
	// session_sticky.derive 同理：handler 在 NewHandler 时把开关复制进自己的 cfg
	// （DisableSessionDerive），而 saveConfig 不重建 handler（只热改 live/pool/
	// upstream/scheduler），故运行期改它不生效。
	out = append(out, "session_sticky.derive")
	out = append(out, "logging.request_archive_enabled", "logging.request_retention_days", "logging.request_archive_max_mb")
	out = append(out, "server.read_timeout", "server.max_body_bytes")
	// 入站边界加固字段在装配期被固化进 handler（body 上限 / 受信任代理匹配器 /
	// 限流器实例），无法热改。
	out = append(out, "security.trusted_proxy", "security.read_only_rate_limit")
	// alerting.* 同样是装配期字段：Notifier 在启动时构造（alerting.New），cfg 之后
	// 不再读取——面板改完只落盘、不热生效，此前无任何重启提示（D-01，与 issue #102
	// 附带发现 2 同类：告警是积分打穿的安全网，静默发旧地址比不告警更危险）。
	out = append(out, "alerting.webhook_url", "alerting.min_credit_threshold", "alerting.consecutive_failures", "alerting.dedup_minutes")
	return out
}

// mergeConfigMaps 把 incoming 深合并进 cur（原地），返回 cur。
// 对嵌套对象逐键覆盖而不是整体替换：面板表单只提交它管理的键，
// 未提交的兄弟键（含用户手写的未知键）保持原样。
func mergeConfigMaps(cur, incoming map[string]any) map[string]any {
	for k, v := range incoming {
		if inMap, ok := v.(map[string]any); ok {
			if curMap, ok := cur[k].(map[string]any); ok {
				cur[k] = mergeConfigMaps(curMap, inMap)
				continue
			}
		}
		cur[k] = v
	}
	return cur
}

// mergedJSON 把合并后的 map 序列化回 JSON（供 ParseConfig 校验）。
func mergedJSON(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// newPool 构造账号池，并把 state.json 的加载错误上抛（ADR-009 同族）。
// 抽成函数的目的：main() 里的 log.Fatalf 不可测，而「加载失败 → 拒绝启动」这条判定必须被测试覆盖
// （它是「损坏文件不再静默当成没有状态」的唯一保证）。
func newPool(stateFile string) (*pool.Pool, error) {
	p := pool.New(stateFile)
	if err := p.LoadErr(); err != nil {
		return p, err
	}
	return p, nil
}

// newUsageRecorder 同上，针对 usage.json 台账（ADR-009）。
// walCheckpointAfter 装配期传入（<=0 由 usage 包回落默认 20000）。
func newUsageRecorder(path string, walCheckpointAfter int) (*usage.Recorder, error) {
	rec := usage.NewWithCheckpoint(path, walCheckpointAfter)
	if err := rec.LoadErr(); err != nil {
		return rec, err
	}
	return rec, nil
}

// loadAccountsAndPool 启动期装配：扫描 auths → 构造池（含 state.json 加载判定）。
// 返回 skippedAuths 供调用方决定对齐方式（>0 → 只增不删，ADR-010 §2.2）。
//
// 抽成函数的原因：main() 里的 log.Fatalf 不可测，而这里的两条语义都必须被覆盖——
// 「扫描不完整 → 只增不删」与「状态文件不可用 → 拒绝启动」。
func loadAccountsAndPool(cfg *Config) (auths []*auth.Auth, skipped []string, p *pool.Pool, err error) {
	auths, skipped, err = auth.LoadDirReport(cfg.AuthDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load auths: %w", err)
	}
	log.Printf("loaded %d account(s) from %s", len(auths), cfg.AuthDir)
	if len(skipped) > 0 {
		log.Printf("WARN: %d 个账号文件被跳过（见上方 WARN）—— 本次对齐只增不删", len(skipped))
	}
	// ADR-009 同族：state.json 加载失败不得静默「当成没有状态」——
	// 历史上损坏文件被直接 return 忽略，健康度观测全丢，选号依据被无声重置。
	p, err = newPool(cfg.StateFile)
	if err != nil {
		return auths, skipped, p, fmt.Errorf("pool state: %w", err)
	}
	return auths, skipped, p, nil
}
