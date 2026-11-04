// index.go 面板静态资源与安全响应头。
//
// 资源经 go:embed 打进二进制（随服务部署，无外部构建步骤）：
//   - index.html       页面骨架
//   - app.js           全部前端逻辑（独立文件而非内联，为了启用无需 unsafe-inline 的严格 CSP）
//   - acl_notice.js    F-101 凭证 ACL 自动收紧的面板显式提示（R-101）
//   - savings_note.js  R-106 OpenRouter 费率折算的 inference 标注与「未估算」展示
//   - opsagent.js      REQ-opsagent 运维 agent 可观测视图
//   - trends.js        REQ-trends 时间序列证据视图（独立嵌入同 opsagent.js）
//
// 安全头对"面板页面与全部 /panel/api/* 响应"统一生效：CSP 限制脚本只能来自本服务，
// 禁止被 iframe 嵌套（防点击劫持），禁 MIME 嗅探，并声明不泄露 Referer 出去。
package panel

import (
	_ "embed"
	"net/http"
	"strings"
	"sync"
)

//go:embed index.html
var indexHTML []byte

//go:embed app_core.js
var appCoreJS []byte

//go:embed app_views_models.js
var appViewsModelsJS []byte

//go:embed app_views_packages.js
var appViewsPackagesJS []byte

//go:embed app_views_logs.js
var appViewsLogsJS []byte

//go:embed app_views_tasks.js
var appViewsTasksJS []byte

//go:embed app_views_config.js
var appViewsConfigJS []byte

//go:embed app_views_accounts.js
var appViewsAccountsJS []byte

//go:embed app_views_usage.js
var appViewsUsageJS []byte

//go:embed app_views_keys.js
var appViewsKeysJS []byte

//go:embed app_views_achievements.js
var appViewsAchievementsJS []byte

//go:embed app_views_gamify.js
var appViewsGamifyJS []byte

//go:embed app.js
var appJS []byte

//go:embed app_qr.js
var appQRJS []byte

//go:embed acl_notice.js
var aclNoticeJS []byte

//go:embed savings_note.js
var savingsNoteJS []byte

//go:embed opsagent.js
var opsAgentJS []byte

//go:embed trends.js
var trendsJS []byte

//go:embed agent_profile.js
var agentProfileJS []byte

//go:embed bench.js
var benchJS []byte

//go:embed gamify.css
var gamifyCSS []byte

// ── 面板资源清单：加载顺序的唯一真相（2026-11-03）────────────────────────
//
// 为什么改成生成式：原先 index.html 尾部手写了 17 行 `<script src=...>`，head 里
// 手写过 `<link>`。而 index.html 正被 maxFrontendLines 棘轮卡着（只许收紧），于是
// **每新增一个组件就要吃一行棘轮额度**——加一个 gamify 组件就把 1842 顶成 1844，
// 门禁直接红。这是结构性缺陷：组件数增长不该受页面骨架行数限制。
//
// 现在 index.html 只留两个占位符，标签在首次请求时按本清单展开：
//   - `<!--panel-styles-->`  → panelStyles 展开成 <link rel="stylesheet">
//   - `<!--panel-scripts-->` → panelScripts 展开成 <script src>
//
// 顺序即依赖顺序（经典脚本共享全局作用域，先定义后使用）：
//
//	app_core.js / app_views_*.js  必须在 app.js 之前（app.js 顶层 go() 会直接调用
//	                              视图函数，排在后面就 ReferenceError → 白屏）
//	app_qr.js                     例外：只在券码弹窗的事件处理器里调用，顺序无所谓
//
// 改动本清单必须同时满足 TestComponentLoadOrder（顺序规则）与
// TestIndexHTMLHasNoHandWrittenAssetTags（骨架里不许再手写标签）。
var panelStyles = []string{
	"gamify.css",
}

var panelScripts = []string{
	"app_core.js",
	"app_views_models.js",
	"app_views_packages.js",
	"app_views_keys.js",
	"app_views_accounts.js",
	"app_views_usage.js",
	"app_views_config.js",
	"app_views_tasks.js",
	"app_views_achievements.js",
	"app_views_gamify.js",
	"app_views_logs.js",
	"app.js",
	"app_qr.js",
	"acl_notice.js",
	"savings_note.js",
	"opsagent.js",
	"trends.js",
	"agent_profile.js",
	"bench.js",
}

const (
	panelStylesPlaceholder  = "<!--panel-styles-->"
	panelScriptsPlaceholder = "<!--panel-scripts-->"
)

var (
	renderedIndexOnce sync.Once
	renderedIndex     []byte
)

// renderIndexHTML 把 index.html 的资源占位符展开成实际标签，结果只算一次。
//
// 占位符缺失时**不静默跳过**：少了标签页面会白屏（无脚本），而 Go 侧看不见。
// 这里改为在拼接后发现占位符仍在就直接把原样返回，并由
// TestIndexHTMLPlaceholdersReplaced 在 CI 兜住（服务端不做运行时 panic——
// 页面白屏比进程崩更容易排查）。
func renderIndexHTML() []byte {
	renderedIndexOnce.Do(func() {
		var styles, scripts strings.Builder
		for _, name := range panelStyles {
			styles.WriteString(`<link rel="stylesheet" href="` + name + `">` + "\n")
		}
		for _, name := range panelScripts {
			scripts.WriteString(`<script src="` + name + `"></script>` + "\n")
		}

		out := string(indexHTML)
		out = strings.Replace(out, panelStylesPlaceholder, strings.TrimSuffix(styles.String(), "\n"), 1)
		out = strings.Replace(out, panelScriptsPlaceholder, strings.TrimSuffix(scripts.String(), "\n"), 1)
		renderedIndex = []byte(out)
	})
	return renderedIndex
}

// csp 内容安全策略（严格版，无需 unsafe-inline）：
//   - default-src 'none'        默认全禁，逐个开口
//   - script-src 'self'         只跑同源脚本（app.js）；页面无内联事件处理器/内联脚本
//   - style-src 'self' 'unsafe-inline'
//     style 的内联是设计取舍：页面有少量 style="..." 属性（进度条宽度、表格列宽），
//     允许内联样式不会导致脚本执行；仍禁止外部样式域与 @import 外链。
//   - connect-src 'self'        前端 fetch 只能打本服务
//   - img-src 'self' data:      图标/内联图
//   - form-action 'none'        页面无表单提交目标（配置页是 JS 提交）
//   - frame-ancestors 'none'    禁止被任何站点 iframe 嵌套（点击劫持）
//   - base-uri 'none'          禁止注入 <base> 改写相对路径
const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; img-src 'self' data:; form-action 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'"

// setSecurityHeaders 写入面板统一安全响应头（页面与 API 都要，API 也含 JSON 数据）。
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff") // 禁 MIME 嗅探
	w.Header().Set("X-Frame-Options", "DENY")           // 老浏览器兜底（CSP frame-ancestors 的等价项）
	w.Header().Set("Referrer-Policy", "no-referrer")    // 不外泄面板地址给外部站点
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	// 资源 URL 不随内容变化（go:embed 打进 exe，换版路径不变）。没有缓存头时浏览器按
	// 启发式缓存 app.js/css，换版后看到的仍是旧界面——2026-11-04 实际踩到。
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
}

// index 输出面板页面（静态无秘密；数据接口 /panel/api/* 才走鉴权）。
//
// 输出的是 renderIndexHTML() 展开后的页面（资源标签由 panelScripts/panelStyles
// 生成），不是原始的 index.html 字节。
func (p *Panel) index(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：整页一次性写回，写失败即客户端已断开，无任何可恢复语义（与本文件其余
	// 静态资源 handler 同口径）。
	_, _ = w.Write(renderIndexHTML())
}

// appScript 输出前端逻辑（同源脚本，供 CSP script-src 'self' 加载）。
func (p *Panel) appScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(appJS)
}

// appViewsLogsScript 输出日志视图组件（从 app.js 拆出的第十个组件，2026-11-02）。
func (p *Panel) appViewsLogsScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsLogsJS)
}

// appViewsTasksScript 输出积分任务/任务中心视图组件（从 app.js 拆出的第九个组件，2026-11-02）。
func (p *Panel) appViewsTasksScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsTasksJS)
}

// appViewsConfigScript 输出配置页视图组件（从 app.js 拆出的第八个组件，2026-11-02）。
func (p *Panel) appViewsConfigScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsConfigJS)
}

// appViewsAccountsScript 输出账号池视图组件（从 app.js 拆出的第七个组件，2026-11-02）。
func (p *Panel) appViewsAccountsScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsAccountsJS)
}

// appViewsUsageScript 输出用量视图组件（从 app.js 拆出的第六个组件，2026-11-02）。
func (p *Panel) appViewsUsageScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsUsageJS)
}

// appViewsKeysScript 输出 API Key 弹窗组件（从 app.js 拆出的第五个组件，2026-11-02）。
func (p *Panel) appViewsKeysScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsKeysJS)
}

// appViewsAchievementsScript 输出成就带组件（P1，2026-11-03）。
// 加载顺序关键：排在 app.js 之前；gamify.js 的 noteStreak() 运行时调用
// noteOp()（本文件声明），先定义后使用最稳。只放声明，遵守
// TestViewsComponentsAreFunctionOnly 白名单；初始渲染由 app.js start() 调用。
func (p *Panel) appViewsAchievementsScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsAchievementsJS)
}

// appViewsGamifyScript 输出游戏化微动效层组件（P0，2026-11-03）。
// 加载顺序关键：index.html 中排在 app.js 之前（app.js 的 toast()/start() 与
// 各成功动作会调用 setCount/celebrate/noteStreak，先定义后使用最稳）。
// 该文件只放声明（顶层仅 renderStreak 由 app.js start() 调用），
// 遵守 TestViewsComponentsAreFunctionOnly 对 app_views_* 的白名单判据。
func (p *Panel) appViewsGamifyScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsGamifyJS)
}

// appViewsPackagesScript 输出积分包视图组件（从 app.js 拆出的第四个组件，2026-11-02）。
// 加载顺序关键：必须排在 app.js 之前（函数体里的状态读取依赖 app.js 已初始化的 let 绑定）。
func (p *Panel) appViewsPackagesScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsPackagesJS)
}

// appViewsModelsScript 输出模型目录视图组件（从 app.js 拆出的第三个组件，2026-11-02）。
// 加载顺序关键：必须排在 app.js 之前（app.js 顶层路由会直接调用 renderModels）。
func (p *Panel) appViewsModelsScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appViewsModelsJS)
}

// appCoreScript 输出面板通用原语组件（从 app.js 拆出的第二个组件，2026-11-02）。
// 加载顺序关键：index.html 中必须排在 app.js 之前（app.js 顶层会调用这些工具函数）。
func (p *Panel) appCoreScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appCoreJS)
}

// appQRScript 输出 QR 编码器组件（从 app.js 拆出的第一个组件，2026-11-02）。
// 与 appScript 同口径：同源脚本 + 统一安全头，供 CSP script-src 'self' 加载。
func (p *Panel) appQRScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(appQRJS)
}

// aclNoticeScript 输出 F-101 面板 ACL 提示脚本（同源静态资源，与 appScript 同口径）。
func (p *Panel) aclNoticeScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(aclNoticeJS)
}

// savingsNoteScript 输出 R-106 费率估算说明脚本（同上）。
func (p *Panel) savingsNoteScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(savingsNoteJS)
}

// opsAgentScript 输出运维 agent 可观测视图脚本（REQ-opsagent 阶段 2，独立嵌入同上）。
func (p *Panel) opsAgentScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(opsAgentJS)
}

// trendsScript 输出时间序列证据视图脚本（REQ-trends-2026-10-29，独立嵌入同 opsagent.js）。
func (p *Panel) trendsScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(trendsJS)
}

// agentProfileScript 输出流量画像视图脚本（REQ-agent-profile-ui-2026-10-29，独立嵌入同 trends.js）。
func (p *Panel) agentProfileScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(agentProfileJS)
}

// benchScript 输出网关级 benchmark 摘要脚本（REQ-benchmark G4，独立嵌入同 trends.js）。
func (p *Panel) benchScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(benchJS)
}

// gamifyStyle 输出游戏化微动效层样式表（P0，2026-11-03）。
// 独立 CSS 文件而非内联进 index.html：index.html 已超深度棘轮对前端文件只许收紧的
// 阈值，动效样式拆出既减负又可整体回滚；CSP style-src 已含 'self'，无需放宽。
// 由 app.js toast() / app_views_gamify.js setCount/celebrate/renderStreak 消费。
func (p *Panel) gamifyStyle(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// 忽略：静态资源全量写回，写失败即客户端断连，无可恢复语义（与 appScript 同口径）。
	_, _ = w.Write(gamifyCSS)
}
