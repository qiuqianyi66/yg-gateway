package reqlog

import (
	"testing"
	"time"
)

// TestAggregateDaily 按天聚合：跨天分组、成败计数、账号去重、token 合计、
// 时间升序、0 请求天的成功率 unknown 语义（值 0 但消费方按无观测处理）。
// 时间用本地时区构造，避免测试机时区把 UTC 时刻映射到另一天（分组意图失真）。
func TestAggregateDaily(t *testing.T) {
	loc := time.Local
	d27 := time.Date(2026, 10, 27, 15, 0, 0, 0, loc)
	events := []Event{
		{Time: d27, Account: "a1", OK: true, PromptTokens: 100, CompletionTokens: 10},
		{Time: d27.Add(5 * time.Minute), Account: "a1", OK: false, PromptTokens: 200, CompletionTokens: 20},
		{Time: d27.Add(8 * time.Minute), Account: "a2", OK: true, PromptTokens: 50, CompletionTokens: 5},
		// 次日（本地日期 = 28 号）。
		{Time: d27.AddDate(0, 0, 1), Account: "a2", OK: true, PromptTokens: 300, CompletionTokens: 30},
		{Time: d27.AddDate(0, 0, 1).Add(10 * time.Minute), Account: "a3", OK: true, PromptTokens: 0, CompletionTokens: 0},
		// 空账号不计入活跃账号数（去重计数语义），但仍计入请求数。
		{Time: d27.AddDate(0, 0, 1).Add(20 * time.Minute), Account: "", OK: true, PromptTokens: 10, CompletionTokens: 1},
	}
	got := AggregateDaily(events)
	if len(got) != 2 {
		t.Fatalf("len = %d want 2（跨天分两组）", len(got))
	}
	if got[0].Date >= got[1].Date {
		t.Fatalf("应时间升序：%v >= %v", got[0].Date, got[1].Date)
	}
	// 27 号组：3 请求 / 2 成功 / 1 失败 / 2 账号。
	d0 := got[0]
	if d0.Requests != 3 || d0.Succeeded != 2 || d0.Failed != 1 || d0.ActiveAccounts != 2 {
		t.Fatalf("27 号组 = %+v", d0)
	}
	if d0.SuccessRate != 2.0/3.0 {
		t.Fatalf("27 号成功率 = %v want 2/3", d0.SuccessRate)
	}
	// 28 号组：3 请求 / 3 成功 / 0 失败 / 2 账号（空账号不入 active）。
	d1 := got[1]
	if d1.Requests != 3 || d1.Succeeded != 3 || d1.Failed != 0 || d1.ActiveAccounts != 2 {
		t.Fatalf("28 号组 = %+v", d1)
	}
	if d1.SuccessRate != 1.0 {
		t.Fatalf("28 号成功率 = %v want 1.0", d1.SuccessRate)
	}
	if d1.PromptTokens != 310 || d1.CompletionTokens != 31 {
		t.Fatalf("28 号 token = %d/%d want 310/31", d1.PromptTokens, d1.CompletionTokens)
	}
}

// TestAggregateDailyEmptyAndZeroDay 空输入回空列表（不是 nil）；只有 0 请求的
// 天不出现在序列里（没有事件就没有天）；单事件天的成功率公式正确。
func TestAggregateDailyEmptyAndZeroDay(t *testing.T) {
	if got := AggregateDaily(nil); got == nil || len(got) != 0 {
		t.Fatalf("空输入应回空列表，got %v", got)
	}
	one := []Event{{Time: mustTime("2026-10-28T00:10:00Z"), Account: "a1", OK: false}}
	got := AggregateDaily(one)
	if len(got) != 1 || got[0].Succeeded != 0 || got[0].Failed != 1 || got[0].SuccessRate != 0 {
		t.Fatalf("单失败事件 = %+v", got[0])
	}
}

// TestAggregateDailyLocalTime 时区归组用本地日期（与落盘文件名同一口径）：
// 同一绝对时刻在 +08 应归入更晚的本地日。
func TestAggregateDailyLocalTime(t *testing.T) {
	// 2026-10-28T15:00:00Z = 2026-10-28T23:00:00+08（本地仍是 28 号）。
	ts := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	events := []Event{{Time: ts, Account: "a1", OK: true}}
	got := AggregateDaily(events)
	if len(got) != 1 {
		t.Fatalf("len = %d want 1", len(got))
	}
	// 系统本地时区（测试运行机）：东八区预期 2026-10-28；其他时区按实际。
	want := ts.Local().Format("2006-01-02")
	if got[0].Date != want {
		t.Fatalf("date = %v want %v（本地日期）", got[0].Date, want)
	}
}
