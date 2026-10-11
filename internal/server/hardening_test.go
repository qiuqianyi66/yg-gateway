// hardening_test.go 入站边界加固（F-001 body 上限 / F-003 只读端点限流）的牙齿测试。
package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestChatBodyTooLargeRejected F-001 牙齿：设了 MaxBodyBytes 后，超大 body 返回 413，
// 且不触发任何上游调用（在读取阶段即被 MaxBytesReader 截断，半截 JSON 不喂上游）。
// 还原「去掉 MaxBytesReader」的 bug 时，此用例必须 FAIL（超大 body 会被整体读入）。
func TestChatBodyTooLargeRejected(t *testing.T) {
	var upstreamCalls int
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		upstreamCalls++
		return 200, sseOK, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, MaxBodyBytes: 1 << 20}) // 1MB 上限

	// 2MB body，明显超限
	pad := strings.Repeat("a", 2<<20)
	body := []byte(`{"model":"glm-5.2","messages":[],"pad":"` + pad + `"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body)))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code=%d want 413 (body exceeds limit)", rec.Code)
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream calls=%d want 0 (超限不得打上游)", upstreamCalls)
	}
	if !strings.Contains(rec.Body.String(), "request_too_large") {
		t.Errorf("error code missing, body=%s", rec.Body.String())
	}
}

// TestChatBodyUnderLimitProceeds F-001 反证：设了上限但 body 在上限内 → 正常通过。
func TestChatBodyUnderLimitProceeds(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, MaxBodyBytes: 1 << 20})

	body := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s (under-limit body must proceed)", rec.Code, rec.Body)
	}
}

// TestReadOnlyRateLimit F-003 牙齿：只读端点限流，超阈返回 429，不影响 chat 端点。
// 还原「去掉限流」的 bug 时，此用例必须 FAIL（高频请求不再被 429 拦截）。
func TestReadOnlyRateLimit(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, `{"object":"list","data":[]}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{
		Pool: p, Upstream: up,
		ReadOnlyRateLimit: &ReadOnlyRateLimit{Limit: 3, Window: time.Minute},
	})

	// 前 3 次应放行（200），第 4 次起 429
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d code=%d want 200 (within limit)", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit code=%d want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Errorf("error code missing, body=%s", rec.Body.String())
	}
}

// TestReadOnlyRateLimitDisabled 反证：未配置限流时旧行为保留（不限流）。
func TestReadOnlyRateLimitDisabled(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, `{"object":"list","data":[]}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up}) // 不设 ReadOnlyRateLimit

	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d code=%d want 200 (no rate limit configured)", i+1, rec.Code)
		}
	}
}

// TestReadOnlyRateLimitResetsAfterWindow 窗口滑过后计数重置。
func TestReadOnlyRateLimitResetsAfterWindow(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, `{"object":"list","data":[]}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	rl := &ReadOnlyRateLimit{Limit: 2, Window: 30 * time.Millisecond}
	h := NewHandler(Config{Pool: p, Upstream: up, ReadOnlyRateLimit: rl})

	for i := 0; i < 2; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))
	}
	// 第 3 次超限
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit code=%d want 429", rec.Code)
	}
	// 等窗口滑过，计数重置
	time.Sleep(50 * time.Millisecond)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/v1/models", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("after window reset code=%d want 200", rec2.Code)
	}
}

// TestHealthzNotRateLimited /healthz 保持不限流（负载均衡探活友好）。
func TestHealthzNotRateLimited(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{
		Pool:              p,
		ReadOnlyRateLimit: &ReadOnlyRateLimit{Limit: 1, Window: time.Minute},
	})
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("healthz req %d code=%d want 200 (never rate limited)", i+1, rec.Code)
		}
	}
}
