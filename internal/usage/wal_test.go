package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R-205：增量 WAL 落库——日常 flush 只追加 WAL，不重写基线；重启回放恢复。

// 日常 flush（force=false）：只追加 WAL，不重写 usage.json 基线；重启后回放 WAL 恢复全部数值。
func TestWALIncrementalFlushNoBaseRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r1 := New(path)
	r1.Add(time.Now(), "cn", "u1", "glm-5.2", Delta{PromptTokens: 42, HasPromptTokens: true, TotalTokens: 42, HasTotal: true}, true)
	r1.Add(time.Now(), "cn", "u1", "glm-5.2", Delta{PromptTokens: 8, HasPromptTokens: true, TotalTokens: 8, HasTotal: true}, true)
	r1.flush(false) // 增量：应只追加 WAL，不写基线

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("日常 flush 不应重写基线 usage.json（R-205 增量落库）")
	}
	raw, err := os.ReadFile(walPathOf(path))
	if err != nil {
		t.Fatalf("WAL 应已追加: %v", err)
	}
	if !strings.Contains(string(raw), "u1") {
		t.Fatalf("WAL 应包含脏桶记录: %s", raw)
	}

	// 重启：加载空基线 + 回放 WAL → 数值完整恢复。
	r2 := New(path)
	snap := r2.SnapshotWindow(Window{}, nil, nil, nil, nil)
	if snap.Totals.TotalTokens != 50 {
		t.Fatalf("回放后 totalTokens=%d，期望 50", snap.Totals.TotalTokens)
	}
}

// force flush（Stop/Save）：重写完整基线并截断 WAL。
func TestWALForceCheckpointTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r1 := New(path)
	r1.Add(time.Now(), "cn", "u1", "m", Delta{PromptTokens: 7, HasPromptTokens: true, TotalTokens: 7, HasTotal: true}, true)
	r1.flush(true) // 强制基线重写

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("force flush 应写出基线 usage.json: %v", err)
	}
	raw, err := os.ReadFile(walPathOf(path))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("WAL 应已截断/清空: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) != 0 {
		t.Fatalf("基线写完后 WAL 应截断为空，实际: %s", raw)
	}

	r2 := New(path)
	if got := r2.SnapshotWindow(Window{}, nil, nil, nil, nil).Totals.TotalTokens; got != 7 {
		t.Fatalf("基线恢复后 totalTokens=%d，期望 7", got)
	}
}

// WAL 坏行（崩溃残留半行）跳过不炸；好行正常回放。
func TestWALBadLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	// 手工构造：一个好记录（u1=42）+ 一个半截坏行。
	wl := walPathOf(path)
	if err := os.MkdirAll(filepath.Dir(wl), 0o755); err != nil {
		t.Fatal(err)
	}
	good := `{"b":{"s":"h:2026-10-22T10","r":"cn","u":"u1","m":"m","p":42,"t":42,"q":1}}`
	if err := os.WriteFile(wl, []byte(good+"\n{\"b\":{\"s\":\"broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	// 好行回放成功。
	if got := r.SnapshotWindow(Window{}, nil, nil, nil, nil).Totals.TotalTokens; got != 42 {
		t.Fatalf("坏行应跳过、好行应回放，totalTokens=%d，期望 42", got)
	}
}

// 回放合并规则 = 逐字段取大：旧 WAL 记录不覆盖更新的基线值（消解回滚时序回退）。
func TestWALReplayTakesMax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	// 基线：u1 PT=100。
	base := `{"version":6,"saved":"2026-10-22T10:00:00Z","buckets":[{"s":"h:2026-10-22T10","r":"cn","u":"u1","m":"m","p":100,"t":100,"q":1}]}`
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	// WAL：同一桶 PT=80（更旧）→ max 应保留基线 100，不被旧 WAL 覆盖。
	wl := walPathOf(path)
	old := `{"b":{"s":"h:2026-10-22T10","r":"cn","u":"u1","m":"m","p":80,"t":80,"q":1}}`
	if err := os.WriteFile(wl, []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	if got := r.SnapshotWindow(Window{}, nil, nil, nil, nil).Totals.PromptTokens; got != 100 {
		t.Fatalf("旧 WAL 不得覆盖更新基线（逐字段取大），pt=%d，期望 100", got)
	}
}

// 旧格式（v6，无 WAL）文件可正常加载（向后兼容，升级不丢数据）。
func TestLoadLegacyV6StillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	legacy := `{"version":6,"saved":"2026-10-22T10:00:00Z","buckets":[{"s":"h:2026-10-22T10","r":"cn","u":"u1","m":"m","p":13,"t":13,"q":1}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	if got := r.SnapshotWindow(Window{}, nil, nil, nil, nil).Totals.TotalTokens; got != 13 {
		t.Fatalf("旧 v6 基线应正常加载，tt=%d，期望 13", got)
	}
}
