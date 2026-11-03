package panel

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGamifyJSServing 走完整路由（p.ServeHTTP）而非直调 handler：
// 拆分成独立文件后最容易漏的是 routes() 里那行注册（漏了就是运行时 404，
// 面板游戏化动效全部静默失效——测试走本地文件是看不出路由缺失的）。
func TestGamifyJSServing(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/app_views_gamify.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /panel/app_views_gamify.js code=%d（routes() 里漏注册？）", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("缺 CSP 头")
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("app_views_gamify.js 响应为空（go:embed 未生效？）")
	}
}

// TestGamifyCSSServing 游戏化样式表走完整路由：漏注册时页面 404、
// 浏览器拒绝加载外部样式（CSP style-src 'self'），动效全部失效。
func TestGamifyCSSServing(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/gamify.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /panel/gamify.css code=%d（routes() 里漏注册？）", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("缺 CSP 头")
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("gamify.css 响应为空（go:embed 未生效？）")
	}
}

// TestGamifyComponentsEmbedded 钉住两个新资源确实被 go:embed 进二进制且不是空壳：
// embed 指令漏写/文件名打错时 Go 编译期不报错（go:embed 只校验模式匹配），
// 运行时页面会静默缺样式/缺动效——同 app_qr_test.go 的"嵌入为空"纪律。
func TestGamifyComponentsEmbedded(t *testing.T) {
	if len(appViewsGamifyJS) == 0 {
		t.Fatal("app_views_gamify.js 嵌入为空（go:embed 未生效？）")
	}
	for _, must := range []string{"function setCount", "function celebrate", "function renderStreak"} {
		if !bytes.Contains(appViewsGamifyJS, []byte(must)) {
			t.Fatalf("app_views_gamify.js 里找不到 %s —— 拆文件搬漏了？", must)
		}
	}
	if len(gamifyCSS) == 0 {
		t.Fatal("gamify.css 嵌入为空（go:embed 未生效？）")
	}
	for _, must := range []string{"confetti-fly", "hero-ring::after", ".streak"} {
		if !bytes.Contains(gamifyCSS, []byte(must)) {
			t.Fatalf("gamify.css 里找不到 %s —— 样式搬漏了？", must)
		}
	}
	// 反查：游戏化样式不许再留在 index.html（否则就是两份实现，改一处忘一处）。
	idx := servePanelIndexForTest(t)
	for _, must := range []string{"hero-ring::after", "confetti-fly", "@keyframes tst-in"} {
		if bytes.Contains(idx, []byte(must)) {
			t.Fatalf("index.html 里仍残留 %s —— 应只存在于 gamify.css（避免双份实现）", must)
		}
	}
}

// servePanelIndexForTest 取面板首页 HTML（只读，供资源归属断言）。
func servePanelIndexForTest(t *testing.T) []byte {
	t.Helper()
	p := New(Config{Version: "test", APIKey: "test-key"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /panel/ code=%d", rec.Code)
	}
	return rec.Body.Bytes()
}
