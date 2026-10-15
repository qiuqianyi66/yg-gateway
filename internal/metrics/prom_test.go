package metrics

import (
	"strings"
	"testing"
	"time"
)

// TestPrometheusCounterAndGauge 计数器/gauge 的基本暴露格式。
func TestPrometheusCounterAndGauge(t *testing.T) {
	p := NewPrometheus()
	p.Help("x_total", "help text").Type("x_total", "counter")
	p.Counter("x_total", 42, `outcome="ok"`)
	p.GaugeFloat("x_rate", 0.5)
	out := p.String()
	if !strings.Contains(out, "# HELP x_total help text\n") {
		t.Errorf("missing HELP line:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE x_total counter\n") {
		t.Errorf("missing TYPE line:\n%s", out)
	}
	if !strings.Contains(out, `x_total{outcome="ok"} 42`) {
		t.Errorf("missing counter sample:\n%s", out)
	}
	if !strings.Contains(out, "x_rate 0.5\n") {
		t.Errorf("missing gauge sample:\n%s", out)
	}
}

// TestPrometheusHistogram 直方图暴露：每桶一行 _bucket{le=} + _sum + _count，
// 且桶计数是**累计**的（Prometheus 直方图语义要求 cumulative）。
//
// 牙齿：把累计改成裸桶计数 → 本测试 FAIL（le 小的桶会比 le 大的桶样本多）。
func TestPrometheusHistogram(t *testing.T) {
	var h Histogram
	h.Add(5 * time.Millisecond)   // 落 10ms 桶
	h.Add(300 * time.Millisecond) // 落 500ms 桶
	h.Add(300 * time.Millisecond) // 落 500ms 桶

	p := NewPrometheus()
	p.Histogram("d_ms", &h)
	out := p.String()

	if !strings.Contains(out, `d_ms_bucket{le="10"} 1`) {
		t.Errorf("le=10 bucket wrong:\n%s", out)
	}
	if !strings.Contains(out, `d_ms_bucket{le="500"} 3`) {
		t.Errorf("le=500 bucket must be cumulative (1+2=3):\n%s", out)
	}
	if !strings.Contains(out, `d_ms_bucket{le="+Inf"} 3`) {
		t.Errorf("missing +Inf bucket:\n%s", out)
	}
	if !strings.Contains(out, "d_ms_count 3\n") {
		t.Errorf("missing _count:\n%s", out)
	}
	if !strings.Contains(out, "d_ms_sum 605\n") {
		t.Errorf("missing _sum (5+300+300=605ms):\n%s", out)
	}
}

// TestPrometheusLabelOrderStable 多标签时输出顺序稳定（抓取端 diff 干净）。
func TestPrometheusLabelOrderStable(t *testing.T) {
	p := NewPrometheus()
	p.Counter("x", 1, `z="1"`, `a="2"`)
	if !strings.Contains(p.String(), `x{a="2",z="1"} 1`) {
		t.Errorf("labels not sorted:\n%s", p.String())
	}
}

// TestPrometheusNilHistogram nil 直方图不 panic、不输出（可选依赖未接线时）。
func TestPrometheusNilHistogram(t *testing.T) {
	p := NewPrometheus()
	p.Histogram("x", nil)
	if p.String() != "" {
		t.Errorf("nil histogram must emit nothing, got %q", p.String())
	}
}
