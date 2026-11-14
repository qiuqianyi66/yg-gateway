package router

import (
	"testing"
	"time"
)

func healthy(n int) []View {
	vs := make([]View, n)
	for i := range vs {
		vs[i].Healthy = true
	}
	return vs
}

func TestOrderPicksFirstHealthy(t *testing.T) {
	vs := healthy(3)
	vs[0].Healthy = false
	if i, ok := (Order{}).Pick(vs); !ok || i != 1 {
		t.Errorf("Order.Pick = (%d,%v)，want (1,true)（首个不可用应切下一个）", i, ok)
	}
}

func TestOrderNoHealthy(t *testing.T) {
	vs := healthy(2)
	vs[0].Healthy, vs[1].Healthy = false, false
	if _, ok := (Order{}).Pick(vs); ok {
		t.Error("全不健康时 ok 应为 false")
	}
}

func TestOrderEmpty(t *testing.T) {
	if _, ok := (Order{}).Pick(nil); ok {
		t.Error("空视图 ok 应为 false")
	}
}

func TestRotateCyclesDeterministically(t *testing.T) {
	r := &Rotate{}
	vs := healthy(3)
	want := []int{0, 1, 2, 0}
	for k, w := range want {
		i, ok := r.Pick(vs)
		if !ok || i != w {
			t.Fatalf("第 %d 次 Rotate.Pick = (%d,%v)，want (%d,true)", k+1, i, ok, w)
		}
	}
}

func TestRotateSkipsUnhealthy(t *testing.T) {
	r := &Rotate{}
	vs := healthy(3)
	vs[1].Healthy = false
	// 从 0 开始：0 选中 → next=1；下次从 1 起，1 不健康 → 2 选中。
	if i, _ := r.Pick(vs); i != 0 {
		t.Fatalf("第一次应选 0，实得 %d", i)
	}
	if i, _ := r.Pick(vs); i != 2 {
		t.Errorf("第二次应跳过不健康的 1 选 2，实得 %d", i)
	}
}

func TestUsagePicksLeastInflight(t *testing.T) {
	vs := healthy(3)
	vs[0].InFlight = 3
	vs[1].InFlight = 1
	vs[2].InFlight = 2
	if i, ok := (Usage{}).Pick(vs); !ok || i != 1 {
		t.Errorf("Usage.Pick = (%d,%v)，want (1,true)", i, ok)
	}
}

func TestUsageTieBreaksByLastUsed(t *testing.T) {
	early := time.Now().Add(-time.Hour)
	late := time.Now()
	vs := healthy(2)
	vs[0].InFlight, vs[1].InFlight = 0, 0
	vs[0].LastUsed, vs[1].LastUsed = late, early
	if i, _ := (Usage{}).Pick(vs); i != 1 {
		t.Errorf("并列时取 LastUsed 最早（1），实得 %d", i)
	}
}

func TestUsageNoHealthy(t *testing.T) {
	vs := healthy(2)
	vs[0].Healthy, vs[1].Healthy = false, false
	if _, ok := (Usage{}).Pick(vs); ok {
		t.Error("全不健康时 ok 应为 false")
	}
}
