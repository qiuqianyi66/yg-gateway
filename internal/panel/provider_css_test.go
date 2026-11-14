package panel

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestProviderCSSServing provider 样式表走完整路由（F7）。漏注册时 GET 404、
// CSP style-src 'self' 下浏览器拒绝加载 → 徽标与汇总条全部无样式（正是本轮实测
// 抓到的 bug：panelStyles 加了 provider.css 但 routes.go 没注册）。
func TestProviderCSSServing(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/provider.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /panel/provider.css code=%d（routes() 里漏注册？）", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("缺 CSP 头")
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("provider.css 响应为空（go:embed 未生效？）")
	}
}

// TestProviderStylesEmbedded provider.css 确实被嵌入且不是空壳；反查 index.html
// 不许残留（否则就是两份实现，改一处忘一处，会重新吃掉 maxFrontendLines 棘轮额度）。
func TestProviderStylesEmbedded(t *testing.T) {
	if len(providerCSS) == 0 {
		t.Fatal("provider.css 嵌入为空（go:embed 未生效？）")
	}
	for _, must := range []string{".realm-tag.pv-tag", ".pv-strip", ".pv-chip", ".pv-n"} {
		if !bytes.Contains(providerCSS, []byte(must)) {
			t.Fatalf("provider.css 里找不到 %s —— 样式搬漏了？", must)
		}
	}
	idx := servePanelIndexForTest(t)
	for _, must := range []string{".pv-strip", ".pv-chip", ".realm-tag.pv-tag"} {
		if bytes.Contains(idx, []byte(must)) {
			t.Fatalf("index.html 里仍残留 %s —— 应只存在于 provider.css（避免双份实现）", must)
		}
	}
}
