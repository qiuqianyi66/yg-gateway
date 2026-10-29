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
)

//go:embed index.html
var indexHTML []byte

//go:embed app.js
var appJS []byte

//go:embed acl_notice.js
var aclNoticeJS []byte

//go:embed savings_note.js
var savingsNoteJS []byte

//go:embed opsagent.js
var opsAgentJS []byte

//go:embed trends.js
var trendsJS []byte

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
}

// index 输出面板页面（静态无秘密；数据接口 /panel/api/* 才走鉴权）。
func (p *Panel) index(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}

// appScript 输出前端逻辑（同源脚本，供 CSP script-src 'self' 加载）。
func (p *Panel) appScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(appJS)
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
