package panel

import "net/http"

func (p *Panel) routes() {
	// 静态资源（前端壳 / JS / CSS）：不含敏感数据，不鉴权（鉴权由 API 承担）。
	p.mux.HandleFunc("GET /panel/{$}", p.index)
	p.mux.HandleFunc("GET /panel/app_views_logs.js", p.appViewsLogsScript)
	p.mux.HandleFunc("GET /panel/app_views_tasks.js", p.appViewsTasksScript)
	p.mux.HandleFunc("GET /panel/app_views_config.js", p.appViewsConfigScript)
	p.mux.HandleFunc("GET /panel/app_views_accounts.js", p.appViewsAccountsScript)
	p.mux.HandleFunc("GET /panel/app_views_usage.js", p.appViewsUsageScript)
	p.mux.HandleFunc("GET /panel/app_views_keys.js", p.appViewsKeysScript)
	p.mux.HandleFunc("GET /panel/app_views_gamify.js", p.appViewsGamifyScript)
	p.mux.HandleFunc("GET /panel/app_views_achievements.js", p.appViewsAchievementsScript)
	p.mux.HandleFunc("GET /panel/app_views_packages.js", p.appViewsPackagesScript)
	p.mux.HandleFunc("GET /panel/app_views_models.js", p.appViewsModelsScript)
	p.mux.HandleFunc("GET /panel/app_core.js", p.appCoreScript)
	p.mux.HandleFunc("GET /panel/app.js", p.appScript)
	p.mux.HandleFunc("GET /panel/app_qr.js", p.appQRScript)
	p.mux.HandleFunc("GET /panel/acl_notice.js", p.aclNoticeScript)
	p.mux.HandleFunc("GET /panel/savings_note.js", p.savingsNoteScript)
	p.mux.HandleFunc("GET /panel/opsagent.js", p.opsAgentScript)
	p.mux.HandleFunc("GET /panel/trends.js", p.trendsScript)
	p.mux.HandleFunc("GET /panel/agent_profile.js", p.agentProfileScript)
	p.mux.HandleFunc("GET /panel/bench.js", p.benchScript)
	p.mux.HandleFunc("GET /panel/gamify.css", p.gamifyStyle)

	// 面板 API：一律经 p.api 注册（内部强制 withAuth）——「API 必须鉴权」是注册期
	// 结构性保证，新增端点不可能裸奔（P1-3 面板鉴权组合拳落地，见 api() 注释）。
	p.api("GET", "opsagent/status", p.opsAgentStatus)
	p.api("GET", "overview", p.overview)
	p.api("GET", "logs", p.logsHandler)
	p.api("GET", "request_metrics", p.requestMetrics)
	p.api("GET", "trends", p.trends)
	p.api("GET", "agent_profile", p.agentProfile)
	p.api("GET", "benchmark", p.benchmark)
	p.api("GET", "request_logs", p.requestLogs)
	p.api("GET", "models", p.models)
	p.api("POST", "login/start", p.loginStart)
	p.api("GET", "login/poll", p.loginPoll)
	p.api("GET", "login/regions", p.loginRegions)
	p.api("POST", "import/cockpit", p.importCockpit)
	p.api("POST", "accounts/{uid}/revive", p.accountRevive)
	p.api("POST", "accounts/{uid}/disable", p.accountDisable)
	p.api("POST", "accounts/{uid}/pause", p.accountPause)
	p.api("POST", "accounts/{uid}/resume", p.accountResume)
	p.api("POST", "accounts/{uid}/checkin", p.accountCheckin)
	p.api("POST", "accounts/{uid}/balance", p.accountBalance)
	p.api("POST", "accounts/{uid}/remove", p.accountRemove)
	p.api("GET", "accounts/{uid}/tasks", p.accountTasks)
	p.api("POST", "accounts/{uid}/tasks/accept", p.accountTaskAccept)
	p.api("POST", "accounts/{uid}/tasks/accept_all", p.taskAcceptAll)
	p.api("POST", "accounts/{uid}/tasks/claim", p.accountTaskClaim)
	p.api("POST", "accounts/{uid}/tasks/auto", p.accountTaskAuto)
	p.api("POST", "accounts/{uid}/tasks/auto_all", p.accountTaskAutoAll)
	p.api("POST", "tasks/scan_all", p.tasksScanAll)
	p.api("POST", "tasks/run_queue", p.tasksRunQueue)
	p.api("GET", "tasks/queue", p.tasksQueueStatus)
	p.api("GET", "school/vouchers", p.schoolVouchers)
	p.api("POST", "checkin_all", p.checkinAll)
	p.api("POST", "travel_all", p.travelAll)
	p.api("POST", "activity_all", p.activityAll)
	p.api("POST", "keepalive_all", p.keepaliveAll)
	p.api("POST", "balance_all", p.balanceAll)
	p.api("GET", "packages", p.packages)
	p.api("GET", "usage", p.usage)
	p.api("GET", "usage/monthly", p.usageMonthly)
	p.api("POST", "usage/save", p.usageSave)
	p.api("GET", "model_probes", p.modelProbes)
	p.api("GET", "config", p.getConfig)
	p.api("POST", "config", p.saveConfig)
	p.api("POST", "key-reset", p.keyReset)
}

// api 注册面板 API 端点：强制包 withAuth（Bearer 管理员鉴权）。
//
// 为什么收敛到本方法（P1-3 面板鉴权组合拳）：此前每个端点手工
// `mux.HandleFunc(..., p.withAuth(handler))`，新增端点漏包 = 管理端点静默裸奔
// （可读写配置、禁用/移除账号、重置密钥）。改走本方法后，「/panel/api/* 必须
// 鉴权」成为注册期的结构性保证：新增端点时要么走 api()（自动带鉴权）、要么
// 显式用裸 mux.HandleFunc（审阅时一眼看出，且路径仍落在 /panel/api/ 前缀下
// 会被既有测试的无 token 401 断言兜住——见 TestPanelApiRegistryRequiresAuth）。
func (p *Panel) api(method, path string, h http.HandlerFunc) {
	p.mux.HandleFunc(method+" /panel/api/"+path, p.withAuth(h))
}

// ServeHTTP 统一入口：先写安全响应头再分发，保证页面、静态资源、API
// 与 401 错误响应全都带上（API 也可能在浏览器里被直接打开）。
func (p *Panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	p.mux.ServeHTTP(w, r)
}
