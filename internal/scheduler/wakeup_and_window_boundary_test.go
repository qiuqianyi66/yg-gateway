package scheduler

import (
	"context"
	"testing"
	"time"
)

// TestIsOnTimeBoundary 钉住「准点」判定的边界（B3-b）。
//
// 为什么需要：`awaitWakeupGrace` 里写的是 `late <= wakeupLateThreshold` —— 正好 1s 算**不迟到**
// （timer 的正常抖动不该被当成"睡眠唤醒后的补跑"）。这条边界此前没有任何测试：
// 把 `<=` 改成 `<` 全仓测试仍绿（2026-10-17 的 B3 变异核验 MISSED）。
//
// 边界本身在原函数里不可测——`time.Since(planned)` 会持续增长，构造不出"恰好 1s"。
// 所以把判定抽成纯函数 `isOnTime`（行为等价），在这里逐点钉住。
//
// 牙齿：把 `late <= wakeupLateThreshold` 改成 `<` → `1s` 那一行 FAIL。
func TestIsOnTimeBoundary(t *testing.T) {
	cases := []struct {
		late time.Duration
		want bool
		why  string
	}{
		{0, true, "准点"},
		{time.Millisecond, true, "毫秒级抖动仍算准点"},
		{wakeupLateThreshold - time.Nanosecond, true, "阈值内"},
		{wakeupLateThreshold, true, "**正好等于阈值：仍算准点**（<= 的语义）"},
		{wakeupLateThreshold + time.Nanosecond, false, "越过阈值一纳秒即迟到"},
		{time.Minute, false, "睡眠唤醒后的大幅迟到"},
	}
	for _, c := range cases {
		if got := isOnTime(c.late); got != c.want {
			t.Errorf("isOnTime(%v) = %v，want %v（%s）", c.late, got, c.want, c.why)
		}
	}
}

// TestAwaitWakeupGraceOnTimeDoesNotSleep 准点路径必须**零延迟**放行（不睡宽限）。
// 用已取消的 ctx + 已过期的 planned 反证：迟到路径会被 ctx 立刻打断返回 false，
// 而准点路径直接返回 ctx.Err() == nil。
func TestAwaitWakeupGraceOnTimeDoesNotSleep(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	if !awaitWakeupGrace(ctx, time.Now()) {
		t.Fatal("准点触发应当返回 true（继续派发）")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("准点路径不应等待宽限，实测耗时 %v", d)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if awaitWakeupGrace(cancelled, time.Now().Add(-time.Hour)) {
		t.Fatal("ctx 已取消时迟到路径必须返回 false（本批放弃）")
	}
}

// TestReconfigureKeepsWindowsWhenEmpty 空时段参数**不得**覆盖已配置的时段（B3-c）。
//
// 为什么需要：`Reconfigure` 里每个时段都是 `if len(x) > 0 { s.cfg.X = x }`。
// 把任一处改成 `>= 0`（恒真）全仓测试仍绿（2026-10-17 B3 变异核验 MISSED）——
// 那意味着「不填 activity_hours」会把用户配好的时段**清空**，任务落在错误的时点跑，
// 而且不报错。
//
// 牙齿：把 `len(activityHours) > 0` 改成 `>= 0` → 本测试 FAIL。
func TestReconfigureKeepsWindowsWhenEmpty(t *testing.T) {
	s := &Scheduler{}
	s.Reconfigure([]int{8}, []int{9}, []int{10}, []int{11}, []int{12}, []int{13},
		false, false, false, false, false, false)
	if len(s.cfg.ActivityHours) == 0 || s.cfg.ActivityHours[0] != 10 {
		t.Fatalf("前置条件失败：首次 Reconfigure 未写入时段：%v", s.cfg.ActivityHours)
	}

	// 全空 → 六个时段必须全部保留
	s.Reconfigure(nil, nil, nil, nil, nil, nil, false, false, false, false, false, false)

	check := func(name string, got, want []int) {
		if len(got) != len(want) {
			t.Fatalf("%s 被空参数覆盖：got %v want %v", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s 被空参数覆盖：got %v want %v", name, got, want)
			}
		}
	}
	check("CheckinHours", s.cfg.CheckinHours, []int{8})
	check("TravelHours", s.cfg.TravelHours, []int{9})
	check("ActivityHours", s.cfg.ActivityHours, []int{10})
	check("KeepaliveHours", s.cfg.KeepaliveHours, []int{11})
	check("BlackcatHours", s.cfg.BlackcatHours, []int{12})
	check("GrowthHours", s.cfg.GrowthHours, []int{13})

	// 反向：非空参数**必须**生效（否则"保留"逻辑可以退化成永远不更新）
	s.Reconfigure(nil, nil, []int{3, 4}, nil, nil, nil, false, false, false, false, false, false)
	check("ActivityHours(更新)", s.cfg.ActivityHours, []int{3, 4})
}
