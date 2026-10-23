package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/upstream"
)

// TestAnthropicNonStreamErrorFlushed X-1：/v1/messages 非流式错误路径必须透传
// chatCompletions 捕获的状态码与错误信封——修复前 interceptWriter 把它们吞进 buf，
// 客户端拿到 200 空体。
//
// 牙齿：把错误分支改回 `return`（不透传 buf）→ 本测试 FAIL（status=200 body 空）。
func TestAnthropicNonStreamErrorFlushed(t *testing.T) {
	// 空池：chatCompletions 选号失败必走错误信封，interceptWriter 捕获 status+body。
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: upstream.New()})
	// 空池：chatCompletions 必然走错误信封（503），interceptWriter 捕获 status+body。
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, req)

	if rec.Code == http.StatusOK && rec.Body.Len() == 0 {
		t.Fatal("X-1 回归：错误路径返回 200 空体（interceptWriter 吞了状态码与信封）")
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("X-1 回归：错误路径仍返回 200，body=%s", rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("错误信封被丢弃：响应 body 为空")
	}
}

// TestAnthropicBodyLimit X-2：/v1/messages 必须受 MaxBodyBytes 约束——
// 修复前裸 io.ReadAll 绕过入站 body 上限。
//
// 牙齿：把 anthropicMessages 的 MaxBytesReader 去掉 → 本测试 FAIL（不再 413）。
func TestAnthropicBodyLimit(t *testing.T) {
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: upstream.New(), MaxBodyBytes: 32})
	big := strings.Repeat("a", 256)
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(big))
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("X-2 回归：超大 body status=%d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}

// TestFallbackCascadeStopsAfterCommit X-3：端点 A 已写响应头后失败（流式中断），
// 级联必须终止——禁止续写端点 B（A/B 流会拼成损坏响应）。
//
// 牙齿：去掉 committed 守卫 → 端点 B 被命中，body 出现 B 的标记。
func TestFallbackCascadeStopsAfterCommit(t *testing.T) {
	bHit := false
	// 端点 A：吐一块 SSE 后掐断连接（io.Copy 拿读错误 → forwardToOne 返回 false）。
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: chunkA\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			if conn != nil {
				_ = conn.Close()
			}
		}
	}))
	defer srvA.Close()
	// 端点 B：正常 200。
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bHit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[],"marker":"B"}`))
	}))
	defer srvB.Close()

	h := &Handler{cfg: Config{Fallback: &FallbackConfig{Endpoints: []FallbackEndpoint{
		{BaseURL: srvA.URL, APIKey: "k1", Models: map[string]bool{"glm-5.3": true}},
		{BaseURL: srvB.URL, APIKey: "k2", Models: map[string]bool{"glm-5.3": true}},
	}}}}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	if h.fallbackForward(rec, req, []byte(`{}`), "glm-5.3") {
		t.Fatal("端点 A 流式中断后应返回 false")
	}
	if bHit {
		t.Fatal("X-3 回归：已写头后仍续写端点 B（A/B 流会拼接损坏）")
	}
	body, _ := io.ReadAll(rec.Body)
	if strings.Contains(string(body), "marker") {
		t.Fatalf("响应混入端点 B 的内容: %s", body)
	}
}
