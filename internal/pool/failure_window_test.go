// failure_window_test.go 账号级故障窗（P0-2）：最近失败在窗内的账号临时退出
// normal 候选，窗外自动回池；全池皆窗内时兜底仍返回（不 503）。默认（未注入）
// 关闭 = 零回归。
package pool

import (
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

func TestFailureWindowDefaultOffZeroRegression(t *testing.T) {
	p := New("")
	if p.failureWindow != 0 {
		t.Fatalf("默认 failureWindow = %v, want 0（关闭，零回归）", p.failureWindow)
	}
	p.Add(&auth.Auth{UID: "u1"})
	p.NoteError("u1")
	if got := p.Pick(); got == nil || got.UID != "u1" {
		t.Fatalf("未注入窗口时失败号应立即可选（零回归），got %v", got)
	}
}

func TestFailureWindowBlocksThenRecovers(t *testing.T) {
	p := New("")
	p.SetFailureWindow(30 * time.Second)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	p.SetClock(func() time.Time { return now })
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})

	// u1 失败 → 窗内排除：只有 u2 可选。
	p.NoteError("u1")
	for i := 0; i < 3; i++ {
		if got := p.Pick(); got == nil || got.UID != "u2" {
			t.Fatalf("窗内失败号不应被选中，got %v (loop %d)", got, i)
		}
	}

	// 窗外（推进 31s）→ u1 回池，两个都健康。
	now = now.Add(31 * time.Second)
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		got := p.Pick()
		if got == nil {
			t.Fatalf("窗外应可选出号，got nil")
		}
		seen[got.UID] = true
	}
	if !seen["u1"] || !seen["u2"] {
		t.Fatalf("窗外两号都应可选，seen=%v", seen)
	}
}

func TestFailureWindowNoteFailuresFeedsWindow(t *testing.T) {
	p := New("")
	p.SetFailureWindow(30 * time.Second)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	p.SetClock(func() time.Time { return now })
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})

	// 未知失败（NoteFailures）同样计入最近失败时刻 → 窗内排除。
	p.NoteFailures("u1")
	for i := 0; i < 3; i++ {
		if got := p.Pick(); got == nil || got.UID != "u2" {
			t.Fatalf("NoteFailures 喂入后窗内应排除 u1，got %v (loop %d)", got, i)
		}
	}
}

func TestFailureWindowAllBlockedFallsBack(t *testing.T) {
	p := New("")
	p.SetFailureWindow(30 * time.Second)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	p.SetClock(func() time.Time { return now })
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})

	// 全池都在窗内 → 兜底仍返回（非 nil，不 503）。
	p.NoteError("u1")
	p.NoteError("u2")
	if got := p.Pick(); got == nil {
		t.Fatalf("全池窗内应走兜底返回账号，got nil")
	}
}

func TestFailureWindowSingleAccountFallsBack(t *testing.T) {
	p := New("")
	p.SetFailureWindow(30 * time.Second)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	p.SetClock(func() time.Time { return now })
	p.Add(&auth.Auth{UID: "u1"})

	// 单号池：窗内 → 兜底捞回（失败成本仅一轮换，不产生 503 新语义）。
	p.NoteError("u1")
	if got := p.Pick(); got == nil || got.UID != "u1" {
		t.Fatalf("单号池窗内应兜底返回 u1，got %v", got)
	}
}
