package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/metrics"
)

// TestMetricsEndpointExposition GET /metrics 返回 Prometheus 文本格式（F-710）。
//
// 牙齿：把 /metrics 路由删掉 → 本测试 FAIL（落回 404 兜底信封）。
func TestMetricsEndpointExposition(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	lat := metrics.NewRegistry()
	lat.Observe("glm-5.2", 120*time.Millisecond)
	lat.Observe("glm-5.2", 3*time.Second)
	h := NewHandler(Config{Pool: p, Latency: lat})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type=%q want text/plain exposition format", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE yg_gateway_requests_total counter",
		`yg_gateway_requests_total{outcome="succeeded"}`,
		"# TYPE yg_gateway_accounts gauge",
		`yg_gateway_accounts{state="healthy"}`,
		"yg_gateway_global_in_flight",
		"yg_gateway_accounts_in_flight",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q\n---\n%s", want, body)
		}
	}
	// 延迟分桶：注册表启用时应出现 histogram 的 _bucket/_sum/_count。
	if !strings.Contains(body, "yg_gateway_request_duration_ms_bucket") {
		t.Errorf("missing duration histogram buckets\n---\n%s", body)
	}
	if !strings.Contains(body, `yg_gateway_request_duration_ms_count{model="glm-5.2"} 2`) {
		t.Errorf("missing per-model histogram count\n---\n%s", body)
	}
}

// TestMetricsRequiresAuth 启用鉴权后 /metrics 与 /status 同口径（401 拒绝）。
//
// 牙齿：把 metricsHandler 的 withAuth 去掉 → 本测试 FAIL（返回 200）。
func TestMetricsRequiresAuth(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, APIKey: "secret-key"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key: status=%d want 401", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("with key: status=%d want 200", rec2.Code)
	}
}

// TestMetricsMethodNotAllowed /metrics 只允许 GET：其他方法回 OpenAI 信封 405。
func TestMetricsMethodNotAllowed(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow=%q want to contain GET", allow)
	}
}

// TestMetricsDegradesWithoutOptionalDeps 未接线可选依赖（Latency/RequestLog/
// Usage 全 nil）时 /metrics 仍能返回，且不 panic、不输出可选的指标族。
//
// 牙齿：把任一可选分支的 nil 判据去掉 → 本测试 panic（nil 解引用）。
func TestMetricsDegradesWithoutOptionalDeps(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "yg_gateway_request_duration_ms_bucket") {
		t.Error("duration histogram must be absent when Latency is nil")
	}
	if strings.Contains(body, "yg_gateway_tokens_total") {
		t.Error("token counters must be absent when Usage is nil")
	}
	// 必备族仍在（账号池是无条件依赖）。
	if !strings.Contains(body, "yg_gateway_accounts") {
		t.Errorf("account gauges must always be present\n---\n%s", body)
	}
}

// TestEscapeLabel 标签值转义：伪造 model 名不能注入假指标行。
func TestEscapeLabel(t *testing.T) {
	cases := map[string]string{
		`glm-5.2`:          `glm-5.2`,
		`a"b`:              `a\"b`,
		`a\b`:              `a\\b`,
		"a\nb":             `a\nb`,
		`x"} yg_fake{v="1`: `x\"} yg_fake{v=\"1`,
	}
	for in, want := range cases {
		if got := escapeLabel(in); got != want {
			t.Errorf("escapeLabel(%q) = %q want %q", in, got, want)
		}
	}
}
