package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/usage"
)

func singleEndpoint(url, key string) *FallbackConfig {
	return &FallbackConfig{Endpoints: []FallbackEndpoint{
		{BaseURL: url, APIKey: key, Models: map[string]bool{"glm-5.3": true}},
	}}
}

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
		Fallback: &FallbackConfig{Endpoints: []FallbackEndpoint{}},
	}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("Endpoints 空应返回 false")
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
		_, _ = w.Write([]byte(`{"model":"glm-5.3-fallback","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`))
	}))
	defer srv.Close()

	rec := usage.New("")
	h := &Handler{cfg: Config{
		Fallback: singleEndpoint(srv.URL, "sk-test"),
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
	// 降级必须可见：路由、降级标记、实际服务方、上游实际模型四件。
	if got := w.Header().Get("X-YG-Route"); got != "fallback" {
		t.Fatalf("X-YG-Route=%q, want fallback", got)
	}
	if got := w.Header().Get("X-YG-Degraded"); got != "1" {
		t.Fatalf("X-YG-Degraded=%q, want 1", got)
	}
	if got := w.Header().Get("X-YG-Served-By"); !strings.Contains(got, "127.0.0.1") {
		t.Fatalf("X-YG-Served-By=%q, 应含备用源 host", got)
	}
	if got := w.Header().Get("X-YG-Upstream-Model"); got != "glm-5.3-fallback" {
		t.Fatalf("X-YG-Upstream-Model=%q, want glm-5.3-fallback", got)
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
		Fallback: singleEndpoint(srv.URL, "sk-test"),
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
	// 流式拿不到响应体 model，但路由与服务方必须在写流前就可见。
	if got := w.Header().Get("X-YG-Route"); got != "fallback" {
		t.Fatalf("stream X-YG-Route=%q, want fallback", got)
	}
	if got := w.Header().Get("X-YG-Degraded"); got != "1" {
		t.Fatalf("stream X-YG-Degraded=%q, want 1", got)
	}
	if got := w.Header().Get("X-YG-Served-By"); !strings.Contains(got, "127.0.0.1") {
		t.Fatalf("stream X-YG-Served-By=%q, 应含备用源 host", got)
	}
	// 流式没有解析响应体，实际模型名是 unknown；缺该头就是「未知」，不许猜。
	if got := w.Header().Get("X-YG-Upstream-Model"); got != "" {
		t.Fatalf("stream X-YG-Upstream-Model=%q, want 空（未解析响应体=unknown）", got)
	}
}

// TestFallbackFailureDoesNotLeakHeaders 所有备用源都失败时，网关错误响应不得
// 带任何 X-YG-* 降级头：否则客户端会把「网关报错」读成「备用源已服务」。
//
// 牙齿：把三个 Set 移回「拿到 resp 后、读体前」→ 本测试 FAIL（Served-By 残留）。
func TestFallbackFailureDoesNotLeakHeaders(t *testing.T) {
	// 端点 1：声明 Content-Length 大于实际写出字节 → 客户端 ReadAll 必报错。
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "64")
		_, _ = w.Write([]byte(`{"choices":`))
		// 不补足 64 字节，处理器返回后连接关闭 → unexpected EOF。
	}))
	defer broken.Close()

	// 端点 2：连不上（已关闭）→ 不写任何头就失败。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()

	h := &Handler{cfg: Config{Fallback: &FallbackConfig{Endpoints: []FallbackEndpoint{
		{BaseURL: broken.URL, APIKey: "k1", Models: map[string]bool{"glm-5.3": true}},
		{BaseURL: dead.URL, APIKey: "k2", Models: map[string]bool{"glm-5.3": true}},
	}}}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("两个端点都失败时应返回 false")
	}
	for _, name := range []string{"X-YG-Route", "X-YG-Degraded", "X-YG-Served-By", "X-YG-Upstream-Model"} {
		if got := w.Header().Get(name); got != "" {
			t.Fatalf("失败响应不应带 %s=%q（会把网关错误伪装成备用源已服务）", name, got)
		}
	}
}

// TestEndpointHostOnlyHost X-YG-Served-By 只允许暴露 host:port，
// 不得带 userinfo / path / query / fragment（凭证与内部路径不出网关）。
func TestEndpointHostOnlyHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://user:secret@fallback.example.com:8443/v1/chat?key=abc#frag", "fallback.example.com:8443"},
		{"https://fallback.example.com", "fallback.example.com"},
		{"", "fallback-endpoint"},
		{"://bad", "fallback-endpoint"},
	}
	for _, tc := range cases {
		if got := endpointHost(tc.in); got != tc.want {
			t.Errorf("endpointHost(%q)=%q want %q", tc.in, got, tc.want)
		}
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
		Fallback: singleEndpoint(srv.URL, "sk-test"),
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
		Fallback: singleEndpoint(srv.URL, "sk-test"),
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
		Fallback: singleEndpoint(srv.URL, "wrong"),
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

// TestFallbackCascadeOrder 多端点按序级联：第一个端点连接失败 → 落到第二个成功端点。
// R-107 牙齿：把级联循环改成只用第一个端点 → 本测试 FAIL。
func TestFallbackCascadeOrder(t *testing.T) {
	hit := ""
	// 端点 1：直接连不上（端口关）。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "dead"
	}))
	dead.Close() // 立刻关死 → 连接被拒

	// 端点 2：正常 200。
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "good"
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer good.Close()

	h := &Handler{cfg: Config{Fallback: &FallbackConfig{Endpoints: []FallbackEndpoint{
		{BaseURL: dead.URL, APIKey: "k1", Models: map[string]bool{"glm-5.3": true}},
		{BaseURL: good.URL, APIKey: "k2", Models: map[string]bool{"glm-5.3": true}},
	}}}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if !h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("端点 2 应接住级联，返回 true")
	}
	if hit != "good" {
		t.Fatalf("级联命中端点=%q want good", hit)
	}
}

// TestFallbackCascadeSkipsNonCovering 模型不在某端点白名单时跳过该端点。
func TestFallbackCascadeSkipsNonCovering(t *testing.T) {
	hit := ""
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = "good"
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	h := &Handler{cfg: Config{Fallback: &FallbackConfig{Endpoints: []FallbackEndpoint{
		// 端点 1 白名单不含 glm-5.3 → 必须跳过。
		{BaseURL: good.URL, APIKey: "k1", Models: map[string]bool{"other-model": true}},
		{BaseURL: good.URL, APIKey: "k2", Models: map[string]bool{"glm-5.3": true}},
	}}}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	if !h.fallbackForward(w, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("覆盖该模型的端点应接住")
	}
	if hit != "good" {
		t.Fatalf("应落到端点 2（good），got hit=%q", hit)
	}
	if !h.cfg.Fallback.Covers("glm-5.3") {
		t.Fatal("Covers 应识别端点 2 的白名单")
	}
	if h.cfg.Fallback.Covers("nope") {
		t.Fatal("Covers(nope) 应为 false")
	}
}
