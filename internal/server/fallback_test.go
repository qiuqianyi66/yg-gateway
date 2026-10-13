package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/usage"
)

func TestFallbackForwardDisabled(t *testing.T) {
	h := &Handler{cfg: Config{}} // Fallback=nil
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("Fallback=nil 应返回 false")
	}
}

func TestFallbackForwardEmptyConfig(t *testing.T) {
	h := &Handler{cfg: Config{
		Fallback: &FallbackConfig{BaseURL: "", APIKey: ""},
	}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("BaseURL/APIKey 空应返回 false")
	}
}

func TestFallbackForwardJSON(t *testing.T) {
	// mock 备用源：返回 200 + usage 字段。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path=%s, want /chat/completions", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("Authorization=%q, want Bearer sk-test", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`))
	}))
	defer srv.Close()

	rec := usage.New("")
	h := &Handler{cfg: Config{
		Fallback: &FallbackConfig{BaseURL: srv.URL, APIKey: "sk-test"},
		Usage:    rec,
	}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if !h.fallbackForward(w, req, []byte(`{"model":"glm-5.3"}`), "glm-5.3") {
		t.Fatal("正常转发应返回 true")
	}
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), `"total_tokens":150`) {
		t.Fatalf("响应 body 未透传: %s", body)
	}
	// usage 应记录了 fallback 的 token。
	if got := rec.MonthlyUsed("fallback"); got != 150 {
		t.Fatalf("fallback 月累计 = %d, want 150", got)
	}
}

func TestFallbackForwardStream(t *testing.T) {
	// mock 备用源：返回 SSE stream。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: chunk1\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	h := &Handler{cfg: Config{
		Fallback: &FallbackConfig{BaseURL: srv.URL, APIKey: "sk-test"},
	}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if !h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("stream 转发应返回 true")
	}
	body, _ := io.ReadAll(w.Result().Body)
	if !strings.Contains(string(body), "chunk1") {
		t.Fatalf("stream body 未透传: %s", body)
	}
}

func TestFallbackForwardUpstreamError(t *testing.T) {
	// mock 备用源：返回 401。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	h := &Handler{cfg: Config{
		Fallback: &FallbackConfig{BaseURL: srv.URL, APIKey: "wrong"},
	}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	// 4xx 也返回 true（已写 header，不能改状态码了）。
	if !h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("备用源 4xx 也应返回 true（已写 header）")
	}
	if w.Code != 401 {
		t.Fatalf("status=%d, want 401（透传）", w.Code)
	}
}
