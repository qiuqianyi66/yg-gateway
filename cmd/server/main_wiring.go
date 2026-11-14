// main_wiring.go main() 的装配块（从 main.go 搬出，纯搬运无逻辑变更）。
//
// 为什么单独成文件（2026-11-14）：main() 原本 483 行一条直线，里面串了四类互不相干的
// 职责——pool 的 17 个 Set* 注入、upstream 的超时/UA/global 注入、调度启停的启动横幅、
// HTTP server 的构造与优雅停机。任何一个改动都要在 483 行里重新定位（git log 显示
// main.go 是近 20 次提交里改动最频繁的文件，8 次）。搬出后每块有名字、有参数边界，
// 改「上游空闲超时」不必再读懂"签到横幅什么时候打印"。
//
// 纪律：本文件只做字段装配与进程编排，**不含业务判断**（判断留在 config/pool/upstream）。
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/server"
	"yg-gateway/internal/upstream"
)

// applyPoolConfig 把 config 的 pool 段注入选号池：熔断器 + 在途上限（含 global 分档）
// + 连败降权 + 闲置补偿调优。非正值在各自的 Set* 内回退默认（这里不做二次校验）。
//
// 集中成一个函数的价值：新增一个可调参数时只有一处要改，且「哪些参数是启动期固定、
// 哪些热生效」在同一屏内可读。
func applyPoolConfig(p *pool.Pool, cfg *Config) {
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
	p.SetEwma(cfg.Pool.Ewma.Enabled, cfg.Pool.Ewma.Alpha, cfg.Pool.Ewma.MinSamples)     // EWMA 表现回灌（ADR-005 D-502，默认关 = 零回归）
	p.SetFailureWindow(cfg.FailureWindowDur)                                            // 账号级故障窗（P0-2，默认 30s；0 关闭）
	p.SetMinRequestInterval(cfg.MinRequestIntervalDur, cfg.MinRequestIntervalGlobalDur) // 主动 RPM 限流（风控规避，默认 0 = 关闭）
	p.SetRecentUsageFactor(cfg.Pool.RecentUsageFactor)                                  // 近期用量衰减（REQ-routing P2，默认关）
	p.SetPaceMode(cfg.Pool.PaceMode)                                                    // pace 选号（F9，默认关 = 零回归）
}

// applyUpstreamConfig 把 config 的出站段注入上游客户端：四层超时、SSE 心跳、指纹脱敏、
// UA 与归属头、RPM/并发闸、global realm 路由、模型目录缓存路径。
//
// 顺序约束：SetModelRateOf 的闭包（在 main 里）必须在 up 装配之后注入——所以倍率表
// 的线不在这里，留在 main（它跨 pool 与 upstream 两个组件）。
func applyUpstreamConfig(up *upstream.Client, cfg *Config) {
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
	// 出站 RPM 硬闸（REA P0，装配期字段）：每账号 60s 窗口最大出站请求数，
	// 0 = 关闭（默认）。RoundTripper 层卡槽位，覆盖全出站路径。
	up.RpmPerAccount = cfg.Upstream.RpmPerAccount
	// 出站在途并发闸（REA §2.5，装配期字段）：每账号同时在途请求数上限，
	// 0 = 关闭（默认）。与 RPM 正交；开启后 /status rhythm 键暴露在途/排队深度。
	up.ConcurrencyPerAccount = cfg.Upstream.ConcurrencyPerAccount
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
}

// logScheduleBanner 打印调度启停的启动横幅。纯观测输出，不影响任何行为——
// 之所以搬出 main()，是因为 38 行 switch 横在装配流中间，把「装配」与「公告」混在一起。
func logScheduleBanner(cfg *Config) {
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
}

// serveHTTP 构造 HTTP server、装停机排水 goroutine、监听并阻塞到退出。
//
// 返回 error 而不是在内部 Fatalf：main() 里的 log.Fatalf 会跳过全部 defer（usage 落盘、
// requestLog.Close 都在 defer 里），真实错误（端口占用等）必须走正常返回让调用方决定。
// 与旧实现逐位一致的部分：ReadTimeout/IdleTimeout 取值、30s 排水窗口、ErrServerClosed
// 视为正常停机并把退出码交给 return nil。
func serveHTTP(ctx context.Context, cfg *Config, h *server.Handler, p *pool.Pool) error {
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		// ReadTimeout 覆盖整个请求读取（含 body 上传）：防慢速 body 拖死连接。
		// 请求体上限见 server.max_body_bytes（默认 64MiB，显式 0 才关闭）；
		// 超限在读阶段即被 MaxBytesReader 截断为 413。缺省 300s（issue #100：固定
		// 60s 会掐掉大上下文/文件块经反代链的慢速上传），server.read_timeout="0" 关闭。
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
		// 排水窗口：给在途 SSE 请求时间完成。原 5s 太短——流式生成合法时长可达数
		// 分钟，5s 一到进程强退、在途请求被硬掐（客户端收到半截流/连接重置）。
		// 提到 30s：绝大多数请求（含典型 SSE）能正常完成；仍超时则强退（记录日志，
		// 运维能看到"有在途请求被中断"）。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[shutdown] 30s 排水窗口超时（在途请求被中断）: %v", err)
		} else {
			log.Printf("[shutdown] 在途请求全部完成，进程退出")
		}
	}()

	log.Printf("yg-gateway listening on %s (api_key=%v)，管理面板 http://127.0.0.1%s/panel/", cfg.Listen, cfg.APIKey != "", panelListenPath(cfg.Listen))
	// http.ErrServerClosed 是优雅停机的正常信号（signal → ctx 取消 → Shutdown 把
	// ListenAndServe 踢出来）——走正常返回路径，所有 defer 执行。真实错误（端口占用
	// 等）返回给 main，由它 Fatalf（os.Exit 会跳过 defer）。
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
