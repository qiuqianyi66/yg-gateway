package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 这两个用例覆盖「启动期拒绝污染」的判定（ADR-009）。
// 判定本身抽成 newPool / newUsageRecorder，因为 main() 里的 log.Fatalf 没法在单测里跑。

func TestNewPoolRejectsUnusableStateFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("文件损坏 → 返回错误", func(t *testing.T) {
		p := filepath.Join(dir, "state-broken.json")
		if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		pl, err := newPool(p)
		if err == nil {
			t.Fatal("损坏的 state.json 必须让启动失败（否则健康度观测被静默清空）")
		}
		if pl == nil {
			t.Error("错误路径也要返回可用的池对象（便于上层记录）")
		}
		pl.Close()
	})

	t.Run("文件不存在 → 正常（首次启动）", func(t *testing.T) {
		pl, err := newPool(filepath.Join(dir, "state-none.json"))
		if err != nil {
			t.Fatalf("首次启动不该报错，got %v", err)
		}
		pl.Close()
	})
}

func TestNewUsageRecorderRejectsNewerVersion(t *testing.T) {
	dir := t.TempDir()

	t.Run("版本比程序新 → 返回错误", func(t *testing.T) {
		p := filepath.Join(dir, "usage-newer.json")
		if err := os.WriteFile(p, []byte(`{"version":999,"buckets":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		rec, err := newUsageRecorder(p)
		if err == nil {
			t.Fatal("版本更新的 usage.json 必须让启动失败（旧程序读新文件会静默丢字段）")
		}
		if rec == nil {
			t.Error("错误路径也要返回对象")
		}
	})

	t.Run("正常文件 → 无错误", func(t *testing.T) {
		p := filepath.Join(dir, "usage-ok.json")
		if err := os.WriteFile(p, []byte(`{"version":6,"buckets":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		rec, err := newUsageRecorder(p)
		if err != nil {
			t.Fatalf("当前版本应可加载，got %v", err)
		}
		if rec == nil {
			t.Error("成功路径必须返回可用的 Recorder")
		}
	})
}
