package server

import (
	"testing"
	"time"

	"yg-gateway/internal/httpauth"
)

// TestKeyRPMGuardAllow 限流语义（record+allow 拆分后与旧实现逐位一致：
// 每窗口恰好放行 RPM 次，之后 429）。
func TestKeyRPMGuardAllow(t *testing.T) {
	g := newKeyRPMGuard()
	limited := &httpauth.Key{ID: "k1", RPM: 2}
	unlimited := &httpauth.Key{ID: "k2", RPM: 0}

	// unlimited: RPM=0 恒放行，打 100 次都过。
	for i := 0; i < 100; i++ {
		if !g.allow(unlimited) {
			t.Fatalf("RPM=0 的 key 应恒放行（第 %d 次被拒）", i)
		}
	}

	// limited: 上限 2，前 2 次过，第 3 次拒（与 withAuth 同流程：先 record 后 allow）。
	hit := func() bool {
		g.record(limited)
		return g.allow(limited)
	}
	if !hit() {
		t.Fatal("第 1 次应放行")
	}
	if !hit() {
		t.Fatal("第 2 次应放行")
	}
	if hit() {
		t.Fatal("第 3 次应被拒（超过 RPM=2）")
	}

	// nil key（未启用鉴权）恒放行。
	if !g.allow(nil) {
		t.Fatal("nil key 应恒放行")
	}
	g.record(nil) // 不 panic
}

func TestKeyRPMGuardIsolation(t *testing.T) {
	g := newKeyRPMGuard()
	k1 := &httpauth.Key{ID: "k1", RPM: 1}
	k2 := &httpauth.Key{ID: "k2", RPM: 1}

	// k1 打满后，k2 不受影响（计数按 keyID 隔离）。
	g.record(k1)
	if !g.allow(k1) {
		t.Fatal("k1 第 1 次应放行")
	}
	g.record(k1)
	if g.allow(k1) {
		t.Fatal("k1 第 2 次应被拒")
	}
	g.record(k2)
	if !g.allow(k2) {
		t.Fatal("k2 不受 k1 影响，第 1 次应放行")
	}
}

// TestKeyRPMRecordCountsDeniedAndUnlimited F6 统计口径：被 429 的请求与无 rpm 配置
// 的 key 的请求也计入实际速率（Snapshot 可见），且限流语义不被统计破坏。
func TestKeyRPMRecordCountsDeniedAndUnlimited(t *testing.T) {
	g := newKeyRPMGuard()
	k := &httpauth.Key{ID: "k", RPM: 1}

	g.record(k) // #1 放行
	if !g.allow(k) {
		t.Fatal("#1 应放行")
	}
	g.record(k) // #2 被 429，但仍计数
	if g.allow(k) {
		t.Fatal("#2 应被拒")
	}
	g.record(k) // #3 同样计数
	if g.allow(k) {
		t.Fatal("#3 应被拒")
	}

	unlimited := &httpauth.Key{ID: "u", RPM: 0}
	for i := 0; i < 5; i++ {
		g.record(unlimited)
		if !g.allow(unlimited) {
			t.Fatalf("unlimited #%d 应放行", i)
		}
	}

	snap := g.Snapshot()
	byID := map[string]map[string]any{}
	for _, s := range snap {
		byID[s["key_id"].(string)] = s
	}
	if got := byID["k"]["rpm"].(int); got != 3 {
		t.Errorf("k rpm=%d want 3（含被 429 的）", got)
	}
	if got := byID["u"]["rpm"].(int); got != 5 {
		t.Errorf("u rpm=%d want 5（无限额 key 也统计）", got)
	}
}

// TestKeyRPMSnapshotRollover F6 分钟翻转：当前计数压入历史桶，桶封顶 rpmHistoryLen，
// history 尾元素 = 当前窗口计数（曲线连续），输出按 key_id 排序。
func TestKeyRPMSnapshotRollover(t *testing.T) {
	g := newKeyRPMGuard()
	k := &httpauth.Key{ID: "b"}

	// 模拟跨分钟：直接拨旧窗口（不依赖真实 sleep）。
	g.mu.Lock()
	st := g.ensureLocked(k.ID, time.Now())
	st.cur = rpmWindow{count: 3, start: time.Now().Add(-2 * time.Minute)}
	g.mu.Unlock()

	g.record(k) // 触发翻转：旧窗口 3 入桶，新窗口 1
	g.record(k) // 新窗口 2

	snap := g.Snapshot()
	if len(snap) != 1 || snap[0]["key_id"] != "b" {
		t.Fatalf("snapshot=%v", snap)
	}
	s := snap[0]
	hist, _ := s["history"].([]int)
	if len(hist) != 2 || hist[0] != 3 || hist[1] != 2 {
		t.Errorf("history=%v want [3 2]（旧桶 + 当前窗口）", hist)
	}
	if s["rpm"] != 2 {
		t.Errorf("rpm=%v want 2（当前窗口）", s["rpm"])
	}

	// 桶封顶：直塞超量旧桶 + 强制分钟翻转 → 翻转时环形截断到 rpmHistoryLen。
	g.mu.Lock()
	st2 := g.ensureLocked("c", time.Now())
	for i := 0; i < rpmHistoryLen+3; i++ {
		st2.buckets = append(st2.buckets, i+1)
	}
	st2.cur = rpmWindow{count: 99, start: time.Now().Add(-2 * time.Minute)}
	g.mu.Unlock()
	g.record(&httpauth.Key{ID: "c"}) // 翻转：旧窗口 99 入桶后截断
	snap2 := map[string]map[string]any{}
	for _, s2 := range g.Snapshot() {
		snap2[s2["key_id"].(string)] = s2
	}
	if hist2 := snap2["c"]["history"].([]int); len(hist2) != rpmHistoryLen+1 {
		t.Errorf("bucket cap: len=%d want %d（12 闭合桶封顶 + 当前窗口 1）", len(hist2), rpmHistoryLen+1)
	}
}

// TestKeyRPMSnapshotSortedAndEmpty F6：快照按键 id 排序；无观测返回空切片。
func TestKeyRPMSnapshotSortedAndEmpty(t *testing.T) {
	g := newKeyRPMGuard()
	if got := g.Snapshot(); len(got) != 0 {
		t.Fatalf("empty snapshot len=%d want 0", len(got))
	}
	g.record(&httpauth.Key{ID: "z"})
	g.record(&httpauth.Key{ID: "a"})
	snap := g.Snapshot()
	if len(snap) != 2 || snap[0]["key_id"] != "a" || snap[1]["key_id"] != "z" {
		t.Fatalf("snapshot not sorted: %v", snap)
	}
}
