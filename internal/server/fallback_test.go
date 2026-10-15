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

// TestFallbackForwardStreamTruncated 备用源 SSE **半截流**必须判失败（R10 红线）。
//
// 修复前 `_, _ = io.Copy(w, resp.Body)` 静默吞掉传输错误 → 一律返回 true，
// 于是「客户端断连 / 备用源写到一半就断」被记成**成功转发**：观测里看不到备用源
// 的传输故障，调用方也不再走错误路径。
//
// 牙齿：把 io.Copy 的错误检查去掉（还原成 `_, _ =`）→ 本测试 FAIL。
func TestFallbackForwardStreamTruncated(t *testing.T) {
	// mock 备用源：先吐一块数据，然后把连接掐断（io.Copy 会拿到读错误）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: chunk1\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 掐断：客户端会收到 unexpected EOF / connection reset。
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			if conn != nil {
				_ = conn.Close()
				return
			}
		}
	}))
	defer srv.Close()

	h := &Handler{cfg: Config{
		Fallback: &FallbackConfig{BaseURL: srv.URL, APIKey: "sk-test"},
	}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("半截 SSE 流必须返回 false（不再是静默成功）")
	}
	// 已实际写出的部分仍在（不回滚已发出的字节）。
	if body, _ := io.ReadAll(w.Result().Body); !strings.Contains(string(body), "chunk1") {
		t.Fatalf("已写出部分应保留，got %q", body)
	}
}

// TestFallbackForwardStreamFlushes SSE 透传必须逐块 flush（不能攒缓冲）。
//
// 牙龄：把 flushWriter 换成裸 w（去掉 Flush）→ 本测试 FAIL。
func TestFallbackForwardStreamFlushes(t *testing.T) {
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
		t.Fatal("正常 SSE 转发应返回 true")
	}
	// httptest.ResponseRecorder 的 Flushed 标记：证明确实刷过（而非攒到结束）。
	if !w.Flushed {
		t.Error("SSE 透传应逐块 flush（ResponseRecorder.Flushed=false 说明没刷）")
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
