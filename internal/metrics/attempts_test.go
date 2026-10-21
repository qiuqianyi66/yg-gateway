package metrics

import "testing"

// TestAttemptsHistogramBuckets 整数分桶落点：边界值（1/2/3/4/6/8）必须精确落桶，
// 1 次的请求不能被记进 2 桶。
//
// 牙齿：把 attemptsIndex 的 `v <= b` 改成 `v < b`（边界样本错桶）→ 本测试 FAIL。
func TestAttemptsHistogramBuckets(t *testing.T) {
	var h AttemptsHistogram
	for _, n := range []int{1, 1, 2, 3, 5, 8, 20} {
		h.Observe(n)
	}

	got := h.BucketCounts()
	// bounds=[1,2,3,4,6,8]：≤1 两次、≤2 一次、≤3 一次、≤4 零、≤6 一次、≤8 一次、+Inf 一次。
	want := []int64{2, 1, 1, 0, 1, 1, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket[%d] = %d want %d (got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
	if h.Count() != 7 {
		t.Errorf("count=%d want 7", h.Count())
	}
	if h.Sum() != 40 { // 1+1+2+3+5+8+20
		t.Errorf("sum=%d want 40", h.Sum())
	}
}

// TestAttemptsHistogramNilSafe nil 接收者不 panic（未接线路径的防御）。
//
// 牙齿：去掉 Observe 的 nil 判据 → 本测试 panic。
func TestAttemptsHistogramNilSafe(t *testing.T) {
	var h *AttemptsHistogram
	h.Observe(3)
	if h.Count() != 0 || h.Sum() != 0 {
		t.Error("nil histogram must stay zero")
	}
	var s *AttemptsStats
	s.Observe("glm-5.2", 2)
	if s.Total() != 0 {
		t.Error("nil stats must stay zero")
	}
}

// TestAttemptsStatsByModel 模型维度分桶与全局同记；空 model 只记全局。
//
// 牙齿：把 Observe 里 `h.Observe(n)` 的模型维度接线删掉 → 本测试
// ModelSnapshot 断言 FAIL。
func TestAttemptsStatsByModel(t *testing.T) {
	s := NewAttemptsStats()
	s.Observe("glm-5.2", 3)
	s.Observe("glm-5.2", 1)
	s.Observe("", 1) // 无模型：只进全局

	if got := s.Global().Count(); got != 3 {
		t.Errorf("global count=%d want 3", got)
	}
	if got := s.Total(); got != 5 {
		t.Errorf("global total=%d want 5", got)
	}
	m := s.ModelSnapshot()
	if got := m["glm-5.2"].Count(); got != 2 {
		t.Errorf("by-model count=%d want 2", got)
	}
	if _, ok := m[""]; ok {
		t.Error("empty model must not create a by-model bucket")
	}
}

// TestCounter 无标签计数器 Inc/Value。
//
// 牙齿：把 Inc 的 Add(1) 删掉 → 本测试 FAIL。
func TestCounter(t *testing.T) {
	c := NewCounter()
	c.Inc()
	c.Inc()
	if got := c.Value(); got != 2 {
		t.Errorf("value=%d want 2", got)
	}
	var nilC *Counter
	nilC.Inc() // nil 安全：零开销丢弃
	if nilC.Value() != 0 {
		t.Error("nil counter must read 0")
	}
}
