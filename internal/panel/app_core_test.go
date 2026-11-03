package panel

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestComponentLoadOrder 钉住组件加载顺序的**规则**（不是逐个硬编码）：
//
//   - `app_core.js` 与所有 `app_views_*.js` 必须排在 `app.js` **之前**；
//   - 理由是经典脚本共享全局作用域：app.js 顶层路由 `go()` 会按 hash 直接调用
//     `renderModels()` 这类视图函数，排在后面就 ReferenceError → 面板白屏，
//     而 Go 侧只做语法检查、完全看不见。
//   - `app_qr.js` 例外：QR 只在券码弹窗的事件处理器里调用，顺序无所谓。
//
// 这条测试随拆分自动覆盖新组件：只要文件名符合 `app_views_*.js`，就必须在 app.js 之前。
func TestComponentLoadOrder(t *testing.T) {
	files := scriptFiles(t)
	appAt := -1
	for i, name := range files {
		if name == "app.js" {
			appAt = i
			break
		}
	}
	if appAt < 0 {
		t.Fatal("index.html 未引用 app.js")
	}
	if !containsStr(files, "app_core.js") {
		t.Fatal("index.html 未引用 app_core.js（组件拆分后漏了 script 标签？）")
	}
	for i, name := range files {
		mustPrecede := name == "app_core.js" || strings.HasPrefix(name, "app_views_")
		if mustPrecede && i > appAt {
			t.Errorf("加载顺序错误：%s 在第 %d 位，app.js 在第 %d 位——视图/工具组件必须先定义，"+
				"否则 app.js 顶层调用会 ReferenceError（面板白屏）", name, i+1, appAt+1)
		}
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestAppCoreLoadsBeforeApp 保留原用例（对 app_core.js 的定点断言，报错信息更直白）。
//
// 为什么这条是硬约束（不是风格问题）：app_core.js 里是纯工具函数（esc/ago/fmtTok…），
// 而 app.js 的**顶层**代码（start() / go() 按 hash 渲染）会直接调用它们。经典脚本共享
// 全局作用域，`function` 声明在各自文件里定义；若 app.js 先执行，顶层调用会撞
// ReferenceError → 面板白屏。这个失败**在 Go 侧不可见**（Go 只做语法检查），
// 只有浏览器/顶层冒烟才会炸，所以用顺序断言把它前移到 CI。
func TestAppCoreLoadsBeforeApp(t *testing.T) {
	var coreAt, appAt = -1, -1
	for i, name := range scriptFiles(t) {
		switch name {
		case "app_core.js":
			coreAt = i
		case "app.js":
			appAt = i
		}
	}
	if coreAt < 0 {
		t.Fatal("index.html 未引用 app_core.js（组件拆分后漏了 script 标签？）")
	}
	if appAt < 0 {
		t.Fatal("index.html 未引用 app.js")
	}
	if coreAt > appAt {
		t.Fatalf("加载顺序错误：app_core.js 在第 %d 位，app.js 在第 %d 位——工具函数必须先定义，"+
			"否则 app.js 顶层调用会 ReferenceError（面板白屏）", coreAt+1, appAt+1)
	}
}

// TestAppCoreScriptServing 走完整路由：200 + JS Content-Type + 非空 + CSP 头。
//
// 与 QR 组件同理——拆分后最容易漏的是 routes() 里那行注册，直调 handler 对漏注册无感。
func TestAppCoreScriptServing(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/app_core.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /panel/app_core.js code=%d（routes() 里漏注册？）", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("缺 CSP 头")
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("app_core.js 响应为空（go:embed 未生效？）")
	}
}

// TestAppCoreHasNoDuplicateInApp 工具函数只许有一份实现。
//
// 拆分最常见的退化：搬了但没删干净，两份实现并存。JS 里 `function` 重复声明**不报错**
// （后者覆盖前者），所以这种退化完全静默——只能靠断言把它钉住。
func TestAppCoreHasNoDuplicateInApp(t *testing.T) {
	names := []string{
		"esc", "ago", "dur", "parseAPITime", "fmtLocalDateTime",
		"dtLocalValue", "dtLocalParse",
		"formatTokenCount", "formatLatency", "formatRate",
		"fmtTok", "fmtMs", "fmtRate", "trimFixed", "fmtCredit", "fmtCreditRatio", "fmtModelRate",
		"csvCell",
	}
	for _, fn := range names {
		needle := []byte("function " + fn + "(")
		inCore := bytes.Contains(appCoreJS, needle)
		inApp := bytes.Contains(appJS, needle)
		if !inCore {
			t.Errorf("%s 不在 app_core.js 里（拆分回退了？）", fn)
		}
		if inApp {
			t.Errorf("%s 在 app.js 里仍有定义：工具函数只许在 app_core.js 有一份实现", fn)
		}
	}
	// 反向：纯函数文件不许出现 DOM / 网络调用（否则它不能安全地排在 app.js 之前）。
	for _, banned := range []string{"document.", "fetch(", "localStorage.", "window."} {
		if bytes.Contains(appCoreJS, []byte(banned)) {
			t.Errorf("app_core.js 出现 %q：本文件只放纯函数（不得碰 DOM / 网络 / 存储）", banned)
		}
	}
	if strings.Contains(string(appCoreJS), "new MutationObserver") {
		t.Error("app_core.js 出现 MutationObserver：那是视图层的事，不是通用原语")
	}
}
