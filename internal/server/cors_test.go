package server

// CORS 行为钉住（四维度审查 [P3] 收紧：回显任意 Origin → 仅 https 源）。
// 该层此前**零测试**——与 supports_images 同款结构：功能在、语义错、没人钉。
// 这次把策略表与经 ServeHTTP 的端到端头集合都钉上。

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReflectableCORSOrigin：策略表。放行 https 源（含带端口/localhost）；
// http 源、null 源（沙箱 iframe / file:// / data:）、畸形串一律拒绝。
func TestReflectableCORSOrigin(t *testing.T) {
	cases := []struct {
		in          string
		wantReflect bool
	}{
		{"https://chat.example.com", true},
		{"https://chat.example.com:8443", true},
		{"https://127.0.0.1", true}, // https 本地页（secure context）
		{"https://localhost:7863", true},
		{"http://chat.example.com", false}, // http 源：来源不可验证
		{"http://127.0.0.1:7863", false},   // 面板同源调用不需要 CORS，不在此列
		{"null", false},                    // 沙箱 iframe / file:// / data:
		{"data:text/html,xxx", false},
		{"", false},
		{"https://", false}, // 有 scheme 无 host
		{"://chat.example.com", false},
		{"https://a.com\\@evil.com", false}, // 畸形（反斜杠非法的 host）
	}
	for _, tc := range cases {
		got, ok := reflectableCORSOrigin(tc.in)
		if ok != tc.wantReflect {
			t.Errorf("reflectableCORSOrigin(%q) ok=%v want %v", tc.in, ok, tc.wantReflect)
			continue
		}
		if ok && got != tc.in {
			t.Errorf("reflectableCORSOrigin(%q) 回显值=%q 应与输入一致（浏览器精确比对）", tc.in, got)
		}
	}
}

// TestCORSPreflightHTTPSOriginReflected：合法 https 源的预检拿齐全部头。
func TestCORSPreflightHTTPSOriginReflected(t *testing.T) {
	h := &Handler{cfg: Config{}}
	const origin = "https://chat.example.com"
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS 预检 status=%d want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Errorf("ACAO=%q want %q", got, origin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("ACAC=%q want true（Bearer 鉴权无 cookie，保留以兼容 credentials:'include' 调用方）", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary=%q want Origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Errorf("Allow-Private-Network=%q want true（https 公网页调 localhost 需要）", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Allow-Methods 不应为空")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("Allow-Headers 不应为空（Authorization 必须放行）")
	}
}

// TestCORSPreflightHTTPOriginNotReflected：http 源预检不得回显 ACAO——
// 没有 ACAO，浏览器自己会拦下跨域读（服务端不替浏览器放行）。
// 其余静态头仍然下发（无 ACAO 时浏览器忽略它们，无害）。
func TestCORSPreflightHTTPOriginNotReflected(t *testing.T) {
	h := &Handler{cfg: Config{}}
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	req.Header.Set("Origin", "http://chat.example.com")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("ACAO=%q want 空（http 源不回显）", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("ACAC=%q want 空（未回显时不得单方面发 credentials 头）", got)
	}
	if got := rec.Header().Get("Vary"); got == "Origin" {
		t.Error("未回显时不应标记 Vary: Origin")
	}
}

// TestCORSNoOriginUnaffected：无 Origin 的请求（CLI / curl / SDK）不受 CORS 逻辑
// 影响——不得出现 ACAO/Vary，静态头照旧（旧行为即如此，防回归）。
func TestCORSNoOriginUnaffected(t *testing.T) {
	h := &Handler{cfg: Config{}}
	req := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("无 Origin 时 ACAO=%q want 空", got)
	}
	if got := rec.Header().Get("Vary"); got != "" {
		t.Errorf("无 Origin 时 Vary=%q want 空", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("静态头 Allow-Methods 应照旧下发")
	}
}
