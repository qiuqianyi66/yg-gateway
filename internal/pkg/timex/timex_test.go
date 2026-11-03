package timex

import (
	"context"
	"testing"
	"time"
)

func TestDateLayouts(t *testing.T) {
	// 固定时区，避免测试依赖 runner 的本地时区（否则 CI 与本地会给出不同日期）。
	loc := time.FixedZone("UTC+8", 8*3600)
	ts := time.Date(2026, 11, 2, 20, 10, 43, 0, loc)

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Date", Date(ts), "2026-11-02"},
		{"DateTime", DateTime(ts), "2026-11-02 20:10:43"},
		{"Hour", Hour(ts), "2026-11-02T20"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestHourStartInvariant 钉住 HourStart 与 HourLayout 的不变式：
// 写入侧 Hour(now) 与查询侧 HourStart(now) 必须落在同一个桶，否则 usage 窗口错一格。
func TestHourStartInvariant(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	// 覆盖整点、整点后 1ns、整点前 1ns、跨日边界
	stamps := []time.Time{
		time.Date(2026, 11, 2, 0, 0, 0, 0, loc),
		time.Date(2026, 11, 2, 20, 0, 0, 1, loc),
		time.Date(2026, 11, 2, 20, 59, 59, 999999999, loc),
		time.Date(2026, 11, 3, 0, 0, 0, 0, loc),
	}
	for _, ts := range stamps {
		start := HourStart(ts)
		if Hour(start) != Hour(ts) {
			t.Errorf("HourStart invariant broken at %v: Hour(start)=%q Hour(ts)=%q",
				ts, Hour(start), Hour(ts))
		}
		if start.Minute() != 0 || start.Second() != 0 || start.Nanosecond() != 0 {
			t.Errorf("HourStart(%v) = %v, want exact hour boundary", ts, start)
		}
		if start.After(ts) {
			t.Errorf("HourStart(%v) = %v is after its input", ts, start)
		}
		if ts.Sub(start) >= time.Hour {
			t.Errorf("HourStart(%v) = %v is more than an hour behind", ts, start)
		}
	}
}

func TestSleepCtxZeroOrNegative(t *testing.T) {
	// d<=0 立即放行，且要反映 ctx 状态（已取消 → false）。
	if !SleepCtx(context.Background(), 0) {
		t.Fatal("SleepCtx(bg, 0) = false, want true")
	}
	if !SleepCtx(context.Background(), -time.Second) {
		t.Fatal("SleepCtx(bg, -1s) = false, want true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if SleepCtx(ctx, 0) {
		t.Fatal("SleepCtx(cancelled, 0) = true, want false")
	}
}

func TestSleepCtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if SleepCtx(ctx, 10*time.Second) {
		t.Fatal("SleepCtx(cancelled, 10s) = true, want false")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled sleep took %v, want immediate return", elapsed)
	}
}

// TestSleepCtxCancelInterruptsWait 等待中途取消必须立即返回 false，不等满时长
// （从 scheduler 迁移过来的用例：实现搬到本包，行为断言一并搬，不留孤儿测试）。
func TestSleepCtxCancelInterruptsWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if SleepCtx(ctx, 5*time.Second) {
		t.Fatal("cancelled mid-wait: SleepCtx = true, want false")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancel response took %v, want far below the 5s wait", elapsed)
	}
}

func TestSleepCtxWaits(t *testing.T) {
	start := time.Now()
	if !SleepCtx(context.Background(), 20*time.Millisecond) {
		t.Fatal("SleepCtx(bg, 20ms) = false, want true")
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("sleep returned after %v, want >= ~20ms", elapsed)
	}
}
