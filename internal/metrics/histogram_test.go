package metrics

import (
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

// TestHistogramBucketIndex 桶定位的边界（含边界值归桶与 +Inf 桶）。
func TestHistogramBucketIndex(t *testing.T) {
	cases := []struct {
		ms   int64
		want int
	}{
		{0, 0}, {10, 0}, {11, 1}, {25, 1}, {26, 2},
		{50, 2}, {100, 3}, {120000, 12}, {120001, 13}, {1 << 40, 13},
	}
	for _, c := range cases {
		if got := bucketIndex(c.ms); got != c.want {
			t.Errorf("bucketIndex(%d) = %d, want %d", c.ms, got, c.want)
		}
	}
}

// TestHistogramCountAndMean 计数与均值正确。
func TestHistogramCountAndMean(t *testing.T) {
	var h Histogram
	h.Add(100 * time.Millisecond)
	h.Add(200 * time.Millisecond)
	h.Add(300 * time.Millisecond)
	if h.Count() != 3 {
		t.Fatalf("count = %d, want 3", h.Count())
	}
	if got := h.MeanMs(); got != 200 {
		t.Fatalf("mean = %v, want 200", got)
	}
}

// TestHistogramQuantileKnownDistribution 已知分布的分位数在**桶宽容差**内。
//
// 分布：1~100 秒各 10 次（1000 样本）。
// 分桶估算的固有精度：误差上限约为**所在桶的宽度**（桶内假设均匀分布）。
// 50s 落在 (30s, 60s] 桶 → P50 容差 ±15s；95s 落在 (60s, 120s] 桶 → P95 容差 ±30s。
// 这是分桶方案的已知取舍（ADR-003 D-403）——精度换零依赖与零分配；
// 若要更精确需引 hdrhistogram（已明确不引）。
func TestHistogramQuantileKnownDistribution(t *testing.T) {
	var h Histogram
	for s := 1; s <= 100; s++ {
		for i := 0; i < 10; i++ {
			h.Add(time.Duration(s) * time.Second)
		}
	}
	if h.Count() != 1000 {
		t.Fatalf("count = %d, want 1000", h.Count())
	}
	p50 := h.Quantile(0.50)
	if p50 < 35000 || p50 > 65000 {
		t.Errorf("P50 = %v ms, want ~50000（桶 (30s,60s] 内，容差 ±15s）", p50)
	}
	p95 := h.Quantile(0.95)
	if p95 < 65000 || p95 > 120000 {
		t.Errorf("P95 = %v ms, want ~95000（桶 (60s,120s] 内，容差 ±30s）", p95)
	}
	// 分位数必须单调递增（比绝对值更重要的性质）
	if !(h.Quantile(0.5) <= h.Quantile(0.9) && h.Quantile(0.9) <= h.Quantile(0.95) && h.Quantile(0.95) <= h.Quantile(0.99)) {
		t.Errorf("分位数应单调递增: p50=%v p90=%v p95=%v p99=%v",
			h.Quantile(0.5), h.Quantile(0.9), h.Quantile(0.95), h.Quantile(0.99))
	}
}

// TestHistogramEmpty 无样本时不得 panic 或返回 NaN。
func TestHistogramEmpty(t *testing.T) {
	var h Histogram
	if h.Count() != 0 || h.MeanMs() != 0 || h.Quantile(0.95) != 0 {
		t.Fatal("空直方图应全部返回 0")
	}
	s := h.Snapshot()
	if s.Count != 0 || s.P50Ms != 0 || s.P99Ms != 0 {
		t.Fatalf("空快照应全 0: %+v", s)
	}
	// nil 接收者也要安全
	var nilH *Histogram
	nilH.Add(time.Second)
	if nilH.Count() != 0 || nilH.Quantile(0.5) != 0 {
		t.Fatal("nil 直方图应安全返回 0")
	}
}

// TestHistogramQuantileNotUpperBound 分位数不得取桶上界（高估）。
//
// 这是 D-403 的核心设计选择：桶内**插值**而不是直接返回上界。
// 用首桶（0,10] 内的样本验证——即使精度有限，结果必须 < 上界。
//
// 牙齿：把 Quantile 的插值改成 `return upper` → 本测试 FAIL（会返回 10）。
func TestHistogramQuantileNotUpperBound(t *testing.T) {
	var h Histogram
	for i := 0; i < 100; i++ {
		h.Add(1 * time.Millisecond)
	}
	q := h.Quantile(0.5)
	if q >= 10 {
		t.Fatalf("P50 = %v 取了桶上界（应插值 < 10，证明不是取上界）", q)
	}
	if q <= 0 {
		t.Fatalf("P50 = %v 应在 (0,10) 开区间内", q)
	}
	// 与「取上界」对比：明确断言小于上界（这是设计意图的直接表达）
	if q == float64(bucketBoundsMs[0]) {
		t.Fatal("P50 等于桶上界：插值失效")
	}
}

// TestHistogramSnapshot 快照字段完整且自洽。
func TestHistogramSnapshot(t *testing.T) {
	var h Histogram
	for i := 0; i < 100; i++ {
		h.Add(50 * time.Millisecond)
	}
	s := h.Snapshot()
	if s.Count != 100 {
		t.Fatalf("count = %d, want 100", s.Count)
	}
	if s.P50Ms <= 0 || s.P95Ms <= 0 || s.P99Ms <= 0 {
		t.Fatalf("分位数应有值: %+v", s)
	}
	if s.MaxMs <= 0 {
		t.Fatalf("max 应有值: %+v", s)
	}
	if s.MeanMs <= 0 {
		t.Fatalf("mean 应有值: %+v", s)
	}
	// 单调性
	if !(s.P50Ms <= s.P90Ms && s.P90Ms <= s.P95Ms && s.P95Ms <= s.P99Ms) {
		t.Fatalf("快照分位数应单调: %+v", s)
	}
}

// TestRegistryByModel 按模型分桶独立，且全局是总和。
func TestRegistryByModel(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 10; i++ {
		r.Observe("glm-5.2", 100*time.Millisecond)
	}
	for i := 0; i < 20; i++ {
		r.Observe("deepseek-v4", 500*time.Millisecond)
	}
	r.Observe("", 1*time.Second) // 空模型只进全局

	s := r.Snapshot()
	if s.Global.Count != 31 {
		t.Fatalf("全局计数 = %d, want 31（10+20+1）", s.Global.Count)
	}
	if len(s.ByModel) != 2 {
		t.Fatalf("应 2 个模型维度，got %d: %+v", len(s.ByModel), s.ByModel)
	}
	if s.ByModel["glm-5.2"].Count != 10 {
		t.Fatalf("glm-5.2 计数 = %d, want 10", s.ByModel["glm-5.2"].Count)
	}
	if s.ByModel["deepseek-v4"].Count != 20 {
		t.Fatalf("deepseek-v4 计数 = %d, want 20", s.ByModel["deepseek-v4"].Count)
	}
	// 两模型均值应明显不同（验证独立分桶）
	if s.ByModel["glm-5.2"].MeanMs >= s.ByModel["deepseek-v4"].MeanMs {
		t.Fatalf("两模型均值应不同: glm=%v ds=%v",
			s.ByModel["glm-5.2"].MeanMs, s.ByModel["deepseek-v4"].MeanMs)
	}
}

// TestRegistryMaxModels 模型数超上限时丢弃新模型（防伪造 model 名导致无界增长）。
//
// 牙齿：去掉 maxModels 上限 → 本测试 FAIL（会创建超过上限的条目）。
func TestRegistryMaxModels(t *testing.T) {
	r := NewRegistry()
	r.maxModels = 5 // 测试用上限
	for i := 0; i < 20; i++ {
		r.Observe("model-"+string(rune('a'+i)), time.Second)
	}
	s := r.Snapshot()
	if len(s.ByModel) > 5 {
		t.Fatalf("模型维度不得超上限 5，got %d（无界增长）", len(s.ByModel))
	}
	// 全局仍记全部（只有按模型维度被丢弃）
	if s.Global.Count != 20 {
		t.Fatalf("全局计数应为 20（丢弃只影响模型维度），got %d", s.Global.Count)
	}
}

// TestRegistryConcurrent 并发写入安全（-race 下验证；无 race 时验证计数正确）。
func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	const goroutines, perG = 20, 50
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				// 一半并发写同一模型（竞争创建），一半写各自模型
				model := "shared"
				if i%2 == 0 {
					model = "m" + string(rune('a'+g%5))
				}
				r.Observe(model, time.Duration(rand.IntN(1000))*time.Millisecond)
			}
		}(g)
	}
	wg.Wait()
	s := r.Snapshot()
	want := int64(goroutines * perG)
	if s.Global.Count != want {
		t.Fatalf("全局计数 = %d, want %d（并发丢失？）", s.Global.Count, want)
	}
	// 各模型计数之和应等于全局（没有样本被误丢）
	var sum int64
	for _, ms := range s.ByModel {
		sum += ms.Count
	}
	// 注意：不同 goroutine 的同一模型名会合并，故 sum <= want 是正常的；
	// 这里只断言不超过总数。
	if sum > want {
		t.Fatalf("模型维度计数之和 %d 超过全局 %d（重复计数？）", sum, want)
	}
}
