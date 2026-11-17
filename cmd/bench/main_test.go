package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSnap 在临时目录写一份快照文件，返回目录路径。
func writeSnap(t *testing.T, dir, name, uid string) {
	t.Helper()
	body := `{"accounts":[{"uid":"` + uid + `","disabled":false,"paused":false,"cooling":false}]}`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("写快照 %s 失败: %v", name, err)
	}
}

// TestReadSnapshotsEmptyInputs 断言三种"没有现态"的输入都安全返回 nil 而不 panic。
//
// 为什么重要：readSnapshots 的返回值直接喂给 bench.Eval 的 M1（选号到期偏置）样本。
// 目录没配、目录不存在、目录空都会让 M1 样本为 0 —— 这是**合法状态**（该指标未知），
// 不是崩溃点。若这里 panic，整个 bench 报告就出不来，决策输入直接断供。
func TestReadSnapshotsEmptyInputs(t *testing.T) {
	t.Run("空参数", func(t *testing.T) {
		if got := readSnapshots(""); got != nil {
			t.Errorf("dir 为空应返回 nil，实际 %v", got)
		}
	})
	t.Run("目录不存在", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "not-there")
		if got := readSnapshots(missing); got != nil {
			t.Errorf("目录不存在应返回 nil，实际 %v", got)
		}
	})
	t.Run("空目录", func(t *testing.T) {
		if got := readSnapshots(t.TempDir()); got != nil {
			t.Errorf("空目录应返回 nil，实际 %v", got)
		}
	})
}

// TestReadSnapshotsPicksLatest 断言只取文件名最大的那一份。
//
// 牙齿：把 sort 去掉或改成取 names[0] → 本测试 FAIL。这条不是形式主义：
// 快照按 YYYY-MM-DD.json 命名，字典序 = 时间序；取错一份意味着 M1 指标
// 拿昨天的账号现态冒充今天，而报告里没有任何东西会提示这个错误。
func TestReadSnapshotsPicksLatest(t *testing.T) {
	dir := t.TempDir()
	writeSnap(t, dir, "2026-11-15.json", "uid-old")
	writeSnap(t, dir, "2026-11-17.json", "uid-new")
	writeSnap(t, dir, "2026-11-16.json", "uid-mid")

	got := readSnapshots(dir)
	if len(got) != 1 {
		t.Fatalf("应返回 1 个账号，实际 %d 个: %+v", len(got), got)
	}
	if got[0].UID != "uid-new" {
		t.Errorf("应取文件名最大的 2026-11-17.json（uid-new），实际 %q", got[0].UID)
	}
}

// TestReadSnapshotsIgnoresNonJSON 断言非 .json 文件不参与"最新一份"的竞争。
//
// 场景：写入方先落 .json.tmp 再改名，中途留下的 .json.tmp 若被当成快照，
// 要么解析失败（这份是半截文件），要么把更新但未完成的文件当成最新 —— 两种都错。
func TestReadSnapshotsIgnoresNonJSON(t *testing.T) {
	dir := t.TempDir()
	writeSnap(t, dir, "2026-11-17.json", "uid-real")
	// 名字更大但不是 .json：不能被选中。
	writeSnap(t, dir, "2026-11-19.json.tmp", "uid-tmp")
	writeSnap(t, dir, "2026-11-20.txt", "uid-txt")

	got := readSnapshots(dir)
	if len(got) != 1 || got[0].UID != "uid-real" {
		t.Fatalf("应只取 2026-11-17.json（uid-real），实际 %+v", got)
	}
}

// TestReadSnapshotsEmptyAccountsIsNil 断言"文件存在但 accounts 为空"也返回 nil。
//
// 语义：空数组与"没有现态"在 M1 上等价（样本 0）。返回非 nil 的空切片会让
// 调用方误以为"有现态且一个账号都没"，这两种含义在报告里无法区分。
func TestReadSnapshotsEmptyAccountsIsNil(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-11-17.json"), []byte(`{"accounts":[]}`), 0o644); err != nil {
		t.Fatalf("写空快照失败: %v", err)
	}
	if got := readSnapshots(dir); got != nil {
		t.Errorf("accounts 为空应返回 nil，实际 %+v", got)
	}
}
