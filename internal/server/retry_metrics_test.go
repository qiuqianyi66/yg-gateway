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

// TestObserveRequestEndAttemptsHistogram 收口观测把 attempts 落进正确桶位：
// 1 次的请求不能被记进 2 桶；0 次尝试（网关本地拒绝）必须跳过不进直方图。
//
// 牙齿：把 observeRequestEnd 里 `h.cfg.Attempts.Observe(...)` 接线删掉 →
// 本测试 FAIL（count 恒 0）。
func TestObserveRequestEndAttemptsHistogram(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	att := metrics.NewAttemptsStats()
	h := NewHandler(Config{Pool: p, Attempts: att})

	// 三次收口：attempts=1 / 2 / 3。
	for _, n := range []int{1, 2, 3} {
		st := newChatStat(time.Now(), "glm-5.2", false)
		st.attempts = n
		h.observeRequestEnd(st, "glm-5.2")
	}
	// 一次 0 次尝试（无可用账号等网关本地拒绝）：必须跳过。
	st0 := newChatStat(time.Now(), "glm-5.2", false)
	h.observeRequestEnd(st0, "glm-5.2")

	got := att.Global().BucketCounts()
	// ≤1 桶 1 次（attempts=1 那发）、≤2 桶 1 次、≤3 桶 1 次、其余全 0。
	want := []int64{1, 1, 1, 0, 0, 0, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("global bucket[%d] = %d want %d (got=%v)", i, got[i], want[i], got)
		}
	}
	if att.Total() != 6 {
		t.Errorf("upstream attempts total = %d want 6（0 次那发必须不计入）", att.Total())
	}
	// 模型维度同记：3 发都带模型 "glm-5.2"。
	if m := att.ModelSnapshot(); m["glm-5.2"].Count() != 3 {
		t.Errorf("by-model count = %d want 3", m["glm-5.2"].Count())
	}
}

// TestObserveRequestEndSlowThreshold 慢请求阈值两侧各一例：阈值以下不计数、
// 超过则 +1；阈值=0 关闭时哪怕请求跑了 10 分钟也永不计数。
//
// 不用「恰好等于阈值」的边界例：time.Now() 两次调用之间有微秒级漂移，
// 边界值会让 d 比阈值大一点点（抖动 flake）。以下用 29s（明显低于 30s 阈值）
// 与 60s（明显高于）拉开，断言意图不变。
//
// 牙齿：把 observeRequestEnd 里 `h.cfg.SlowRequests.Inc()` 接线删掉 →
// 本测试 FAIL（计数恒 0）。
func TestObserveRequestEndSlowThreshold(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	counter := metrics.NewCounter()
	h := NewHandler(Config{Pool: p, SlowRequests: counter, SlowRequestThreshold: 30 * time.Second})

	// 阈值以下：不计数。
	stFast := newChatStat(time.Now(), "glm-5.2", false)
	h.observeRequestEnd(stFast, "glm-5.2")
	// 仍低于阈值（29s < 30s）：不计数。
	stBelow := newChatStat(time.Now().Add(-29*time.Second), "glm-5.2", false)
	h.observeRequestEnd(stBelow, "glm-5.2")
	if v := counter.Value(); v != 0 {
		t.Fatalf("below threshold: slow=%d want 0", v)
	}
	// 超过阈值：计数 +1。
	stSlow := newChatStat(time.Now().Add(-60*time.Second), "glm-5.2", false)
	h.observeRequestEnd(stSlow, "glm-5.2")
	if v := counter.Value(); v != 1 {
		t.Fatalf("above threshold: slow=%d want 1", v)
	}

	// 阈值=0 关闭：远古 start 也永不计数。
	off := metrics.NewCounter()
	h0 := NewHandler(Config{Pool: p, SlowRequests: off, SlowRequestThreshold: 0})
	stOld := newChatStat(time.Now().Add(-10*time.Minute), "glm-5.2", false)
	h0.observeRequestEnd(stOld, "glm-5.2")
	if v := off.Value(); v != 0 {
		t.Fatalf("threshold=0: slow=%d want 0（关闭态必须零计数）", v)
	}
}

// TestF305DeferHookRunsOnRealChat 端到端牙齿：一发完整 chat 请求走完
// chatCompletions，defer 收口必须恰好观测一次（attempts=1 落 le=1 桶）。
//
// 牙齿：把 chatCompletions 里 `defer h.observeRequestEnd(st, bareModel)` 那行
// 摘掉 → 本测试 FAIL（收口观测从不触发，count 恒 0）。
func TestF305DeferHookRunsOnRealChat(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	att := metrics.NewAttemptsStats()
	h := NewHandler(Config{
		Pool:     p,
		Upstream: newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true }),
		Attempts: att,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	h.ServeHTTP(rec, req)

	if got := att.Global().Count(); got != 1 {
		t.Fatalf("收口观测次数 = %d want 1（defer 必须恰好执行一次，不泄漏/不重复）", got)
	}
	if b := att.Global().BucketCounts(); b[0] != 1 {
		t.Errorf("le=1 桶计数 = %d want 1（成功单发应是 1 次上游尝试）", b[0])
	}
}

// TestMetricsF305Exposition /metrics 输出包含重试放大率与慢请求指标族
// （HELP/TYPE/样本行，与既有指标同 exposition 格式）。
//
// 牙齿：把 metricsHandler 里 F-305 段整段删掉 → 本测试 FAIL。
func TestMetricsF305Exposition(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	att := metrics.NewAttemptsStats()
	att.Observe("glm-5.2", 1)
	att.Observe("glm-5.2", 2)
	slow := metrics.NewCounter()
	slow.Inc()
	h := NewHandler(Config{Pool: p, Attempts: att, SlowRequests: slow})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# HELP yg_gateway_upstream_attempts_per_request",
		"# TYPE yg_gateway_upstream_attempts_per_request histogram",
		`yg_gateway_upstream_attempts_per_request_bucket{le="1"} 1`,
		`yg_gateway_upstream_attempts_per_request_bucket{le="2"} 2`,
		`yg_gateway_upstream_attempts_per_request_bucket{le="+Inf"} 2`,
		`yg_gateway_upstream_attempts_per_request_count{model="glm-5.2"} 2`,
		"# HELP yg_gateway_upstream_attempts_total",
		"# TYPE yg_gateway_upstream_attempts_total counter",
		"yg_gateway_upstream_attempts_total 3",
		"# HELP yg_gateway_slow_requests_total",
		"# TYPE yg_gateway_slow_requests_total counter",
		"yg_gateway_slow_requests_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q\n---\n%s", want, body)
		}
	}
}
