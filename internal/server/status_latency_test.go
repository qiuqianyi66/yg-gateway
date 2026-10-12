package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"yg-gateway/internal/metrics"
)

// TestStatusLatencyPercentiles /status 必须透出 latency 分位数（ADR-003 D-403）。
//
// 牙齿：把 status 里的 "latency" 键删掉 → FAIL。
func TestStatusLatencyPercentiles(t *testing.T) {
	reg := metrics.NewRegistry()
	// 灌入已知分布：10 次快（50ms）+ 1 次慢（30s）→ P99 应显著高于 P50。
	for i := 0; i < 10; i++ {
		reg.Observe("glm-5.2", 50*time.Millisecond)
	}
	reg.Observe("glm-5.2", 30*time.Second)

	h := NewHandler(Config{Pool: testPoolWith(), Latency: reg})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Latency struct {
			// /status 的 latency 是 RegistrySnapshot（全局 + 按模型两层），
			// 不是扁平的单一直方图快照。
			Global struct {
				Count  int64   `json:"count"`
				P50Ms  float64 `json:"p50_ms"`
				P99Ms  float64 `json:"p99_ms"`
				MeanMs float64 `json:"mean_ms"`
			} `json:"global"`
			ByModel map[string]struct {
				Count int64 `json:"count"`
			} `json:"by_model"`
		} `json:"latency"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if body.Latency.Global.Count != 11 {
		t.Fatalf("latency.global.count = %d, want 11", body.Latency.Global.Count)
	}
	if body.Latency.Global.P50Ms <= 0 || body.Latency.Global.P99Ms <= 0 {
		t.Fatalf("分位数应有值: %+v", body.Latency.Global)
	}
	// 关键性质：P99 必须显著高于 P50（长尾可见）——这正是"均值看不见长尾"的修复目标。
	if body.Latency.Global.P99Ms <= body.Latency.Global.P50Ms {
		t.Fatalf("P99 (%v) 应显著高于 P50 (%v)：长尾未被观测到",
			body.Latency.Global.P99Ms, body.Latency.Global.P50Ms)
	}
	// 按模型维度也要透出
	if m, ok := body.Latency.ByModel["glm-5.2"]; !ok || m.Count != 11 {
		t.Fatalf("按模型维度应有 glm-5.2 且计数 11: %+v", body.Latency.ByModel)
	}
}

// TestStatusLatencyNilWhenDisabled 未启用观测时 latency 为 null（零回归）。
func TestStatusLatencyNilWhenDisabled(t *testing.T) {
	h := NewHandler(Config{Pool: testPoolWith()}) // 不设 Latency
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if v, ok := body["latency"]; !ok {
		t.Fatal("latency 键应始终存在（值为 null）")
	} else if v != nil {
		t.Fatalf("未启用观测时 latency 应为 null，got %v", v)
	}
}

// TestStatusGlobalInFlight /status 透出全局在途计数（ADR-003 D-401）。
func TestStatusGlobalInFlight(t *testing.T) {
	h := NewHandler(Config{Pool: testPoolWith()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if _, ok := body["global_in_flight"]; !ok {
		t.Fatal("/status 缺少 global_in_flight（准入观测）")
	}
}
