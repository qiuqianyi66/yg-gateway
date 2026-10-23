package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newBareHandler 构造一个最小 handler（无上游、无账号），仅用于验证路由层兜底。
func newBareHandler(t *testing.T) *Handler {
	t.Helper()
	return NewHandler(Config{})
}

// TestNotFoundReturnsOpenAIEnvelope 未匹配路径必须回 OpenAI 错误信封（JSON），
// 而不是 Go ServeMux 默认的 text/plain "404 page not found"。
//
// 牙齿：删掉 `h.mux.HandleFunc("/", h.notFound)` → 响应变成 text/plain、
// Content-Type 不再是 application/json → 本测试 FAIL。
func TestNotFoundReturnsOpenAIEnvelope(t *testing.T) {
	h := newBareHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/does-not-exist", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json（裸 text/plain 会让 OpenAI 客户端解析失败）", ct)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON 信封: %v\nbody=%s", err, rec.Body.String())
	}
	if body.Error.Code != "not_found" {
		t.Fatalf("error.code = %q, want not_found（body=%s）", body.Error.Code, rec.Body.String())
	}
	if body.Error.Message == "" || body.Error.Type != "api_error" {
		t.Fatalf("error 信封不完整: %+v", body.Error)
	}
}

// TestMethodNotAllowedReturnsOpenAIEnvelope 已知路径的错误方法必须回 405 JSON 信封
// 并带 Allow 头（客户端据此判断该用哪个方法）。
func TestMethodNotAllowedReturnsOpenAIEnvelope(t *testing.T) {
	h := newBareHandler(t)
	cases := []struct {
		method, path, wantAllow string
	}{
		{"GET", "/v1/chat/completions", "POST"},
		{"PUT", "/v1/messages", "POST"},
		{"POST", "/v1/models", "GET"},
		{"DELETE", "/healthz", "GET"},
		{"PUT", "/status", "GET"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: status = %d, want 405", c.method, c.path, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != c.wantAllow {
			t.Fatalf("%s %s: Allow = %q, want %q", c.method, c.path, got, c.wantAllow)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: 响应不是 JSON 信封: %v", c.method, c.path, err)
		}
		if body.Error.Code != "method_not_allowed" {
			t.Fatalf("%s %s: error.code = %q, want method_not_allowed", c.method, c.path, body.Error.Code)
		}
	}
}

// TestNotFoundDoesNotShadowRealRoutes 兜底路由器不得吞掉合法请求。
//
// 牙齿：若把兜底写成 `h.mux.Handle("/", ...)` 且注册顺序/模式出错（例如
// ServeMux 把 POST /v1/chat/completions 判给 "/"），合法请求会拿到 404
// 而不是进入业务 handler → 本测试 FAIL。
func TestNotFoundDoesNotShadowRealRoutes(t *testing.T) {
	h, _, _ := newMultiKeyHandler(t, nil, false)
	// 合法请求：即便无账号导致业务层返回 503，也绝不能是 404/405。
	rec := doChat(t, h, "", `{"model":"m","messages":[]}`)
	if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("合法路由被兜底吞掉：status=%d body=%s", rec.Code, rec.Body.String())
	}
	// GET /healthz 同样是合法路由。
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/healthz", nil))
	if rec2.Code == http.StatusNotFound || rec2.Code == http.StatusMethodNotAllowed {
		t.Fatalf("GET /healthz 被兜底吞掉：status=%d", rec2.Code)
	}
}
