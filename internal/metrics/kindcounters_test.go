package metrics

import (
	"testing"
)

// TestKindCountersIncAndSnapshot 按标签计数与快照。
func TestKindCountersIncAndSnapshot(t *testing.T) {
	k := NewKindCounters()
	k.Inc("soft_rate")
	k.Inc("soft_rate")
	k.Inc("waf_block")
	snap := k.Snapshot()
	if snap["soft_rate"] != 2 {
		t.Errorf("soft_rate=%d want 2", snap["soft_rate"])
	}
	if snap["waf_block"] != 1 {
		t.Errorf("waf_block=%d want 1", snap["waf_block"])
	}
	// 未出现过的标签不应出现在快照里（避免 /metrics 输出一堆 0 值系列）。
	if _, ok := snap["server"]; ok {
		t.Error("absent kind must not appear in snapshot")
	}
}

// TestKindCountersEmptyKey 空标签按 "(none)" 计：区分「未分类」与「缺失」。
func TestKindCountersEmptyKey(t *testing.T) {
	k := NewKindCounters()
	k.Inc("")
	if got := k.Snapshot()["(none)"]; got != 1 {
		t.Errorf("empty key counted as %q=%d want (none)=1", "(none)", got)
	}
}

// TestKindCountersBounded 标签超上界后不再新增（防御无界增长）。
//
// 牙齿：把 maxKeys 判据去掉 → 本测试 FAIL（map 会随输入无限增长）。
func TestKindCountersBounded(t *testing.T) {
	k := NewKindCounters()
	for i := 0; i < defaultMaxKinds+50; i++ {
		k.Inc(string(rune('a'+i%26)) + string(rune('a'+i/26)) + itoa(i))
	}
	if got := len(k.Snapshot()); got > defaultMaxKinds {
		t.Errorf("kinds=%d want <= %d (bounded)", got, defaultMaxKinds)
	}
}

// TestKindCountersNil nil 接收者不 panic（可选依赖未接线时的安全兜底）。
func TestKindCountersNil(t *testing.T) {
	var k *KindCounters
	k.Inc("x") // 必须不 panic
	if s := k.Snapshot(); s != nil {
		t.Errorf("nil counters snapshot = %v want nil", s)
	}
}

// itoa 本地小工具（避免为测试引 strconv）。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
