package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
)

// TestSnapshotWritesDailyFile 钉住日期：落盘 <dir>/YYYY-MM-DD.json，
// 内容含 captured_at + 账号全量现态；账号字段不含任何凭证（R1）。
func TestSnapshotWritesDailyFile(t *testing.T) {
	dir := t.TempDir()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.SetCreditsDetailed("u1", 120, 500, 30, time.Now().Add(48*time.Hour), 30)

	fixed := time.Date(2026, 10, 29, 3, 0, 0, 0, time.Local)
	old := snapshotNow
	snapshotNow = func() time.Time { return fixed }
	defer func() { snapshotNow = old }()

	s := New(Config{Pool: p, SnapshotDir: dir})
	s.RunSnapshotNow()

	name := filepath.Join(dir, "2026-10-29.json")
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var out struct {
		CapturedAt string `json:"captured_at"`
		Accounts   []any  `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if out.CapturedAt != "2026-10-29T03:00:00+08:00" && !strings.HasPrefix(out.CapturedAt, "2026-10-29T03:00:00") {
		t.Errorf("captured_at=%q want 2026-10-29T03:00:00", out.CapturedAt)
	}
	if len(out.Accounts) != 1 {
		t.Fatalf("accounts=%d want 1", len(out.Accounts))
	}
	// R1：快照任何字段不得含凭证原文（access_token/refresh_token）。
	if strings.Contains(string(raw), "AccessToken") || strings.Contains(string(raw), "RefreshToken") ||
		strings.Contains(string(raw), "at") && strings.Contains(string(raw), "rt") {
		// "at"/"rt" 是常见短串，只在此确证账号载荷不含 token 键（结构上 pool.Status 无 token 字段）。
		t.Error("snapshot payload 疑似含凭证字段")
	}
}

// TestSnapshotDisabledWithoutDir SnapshotDir 为空 = 任务未启用：不落盘、不报错。
func TestSnapshotDisabledWithoutDir(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := New(Config{Pool: p}) // 无 SnapshotDir
	s.RunSnapshotNow()        // 不应 panic / 不应创建文件
}

// TestSnapshotNextWakeIncluded SnapshotDir 配置后，nextWake 纳入每日 03:00 时点。
func TestSnapshotNextWakeIncluded(t *testing.T) {
	s := New(Config{
		Pool:        pool.New(""),
		SnapshotDir: t.TempDir(),
		// 全部业务任务禁用，唯一候选 = snapshot（默认 [3]）。
		CheckinDisabled:   true,
		TravelDisabled:    true,
		ActivityDisabled:  true,
		KeepaliveDisabled: true,
		BlackcatDisabled:  true,
		GrowthDisabled:    true,
	})
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.Local)
	at, kinds := s.nextWake(now)
	if want := time.Date(2026, 10, 30, 3, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("next=%v want %v（次日 03:00）", at, want)
	}
	if len(kinds) != 1 || kinds[0] != taskSnapshot {
		t.Errorf("kinds=%v want [snapshot]", kinds)
	}
}

// TestSnapshotMissingDirTolerant 目录不存在时自动创建（MkdirAll），落盘成功。
func TestSnapshotMissingDirTolerant(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "deep", "snapshots") // 尚不存在
	p := pool.New("")
	s := New(Config{Pool: p, SnapshotDir: dir})
	s.RunSnapshotNow()
	if _, err := os.Stat(filepath.Join(dir, snapshotNow().Format("2006-01-02")+".json")); err != nil {
		t.Fatalf("expected snapshot file after MkdirAll: %v", err)
	}
}
