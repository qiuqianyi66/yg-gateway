package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/metrics"
	"yg-gateway/internal/unknown"
	"yg-gateway/internal/upstream"
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

// TestMetricsTTFBAndErrorKinds TTFB 分桶与上游错误分类计数都出现在 /metrics（可观测补齐）。
//
// 牙齿：把 /metrics 里 TTFB 段或错误分类段去掉 → 对应用例 FAIL。
func TestMetricsTTFBAndErrorKinds(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	ttfb := metrics.NewRegistry()
	ttfb.Observe("glm-5.2", 800*time.Millisecond)
	errs := metrics.NewKindCounters()
	errs.Inc("soft_rate")
	errs.Inc("soft_rate")
	errs.Inc("waf_block")

	h := NewHandler(Config{Pool: p, TTFB: ttfb, ErrKinds: errs})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		"yg_gateway_ttfb_ms_bucket",
		`yg_gateway_ttfb_ms_count{model="glm-5.2"} 1`,
		`yg_gateway_upstream_errors_total{kind="soft_rate"} 2`,
		`yg_gateway_upstream_errors_total{kind="waf_block"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q\n---\n%s", want, body)
		}
	}
}

// TestErrKindCountedOnErrorPolicy 错误分类计数由 applyErrorPolicy 统一汇聚：
// 调用一次即计一次，覆盖全部分类（不用在每个调用点各写一遍）。
//
// 牙齿：把 applyErrorPolicy 开头的 Inc 去掉 → 本测试 FAIL。
func TestErrKindCountedOnErrorPolicy(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	errs := metrics.NewKindCounters()
	h := NewHandler(Config{Pool: p, ErrKinds: errs})

	h.applyErrorPolicy("u1", upstream.ErrSoftRate, "", "glm-5.2", nil)
	h.applyErrorPolicy("u1", upstream.ErrSoftRate, "", "glm-5.2", nil)
	h.applyErrorPolicy("u1", upstream.ErrWafBlock, "", "glm-5.2", nil)

	snap := errs.Snapshot()
	if snap["soft_rate"] != 2 {
		t.Errorf("soft_rate=%d want 2", snap["soft_rate"])
	}
	if snap["waf_block"] != 1 {
		t.Errorf("waf_block=%d want 1", snap["waf_block"])
	}
}

// TestMetricsUnknownsGauge 未消解未知数暴露为 gauge（可观测性的诚实度指标）。
//
// 牙齿：把 unknowns_open 那一段去掉 → 本测试 FAIL。
func TestMetricsUnknownsGauge(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	reg := unknown.NewRegistry()
	p.SetUnknownRegistry(reg)
	p.TrackCostUnknown("u1", "glm-5.2")

	h := NewHandler(Config{Pool: p})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "yg_gateway_unknowns_open 1") {
		t.Errorf("missing unknowns_open gauge\n---\n%s", body)
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
