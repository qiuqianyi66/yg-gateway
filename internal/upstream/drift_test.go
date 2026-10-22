package upstream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDriftSeedsOnFirstSuccess 首次成功观测播种快照，state=ok（不是 drift）。
func TestDriftSeedsOnFirstSuccess(t *testing.T) {
	m := NewDriftMonitor("")
	if got := m.Status().(map[string]any)["state"]; got != "unknown" {
		t.Fatalf("before seed: state=%v want unknown", got)
	}
	m.ObserveCatalog(60, true)
	st := m.Status().(map[string]any)
	if st["state"] != "ok" {
		t.Fatalf("after first success: state=%v want ok", st["state"])
	}
	if st["seeded_features"] == nil {
		t.Fatalf("seeded_features missing after seed: %v", st)
	}
}

// TestDriftDoesNotSeedOnFailure 连续失败不播种（失败可能是本地问题，不是上游基线）。
func TestDriftDoesNotSeedOnFailure(t *testing.T) {
	m := NewDriftMonitor("")
	m.ObserveCatalog(0, false)
	m.ObserveCatalog(0, false)
	st := m.Status().(map[string]any)
	if st["state"] != "unknown" {
		t.Fatalf("failure-only observations: state=%v want unknown", st["state"])
	}
}

// TestDriftFlipsOnProbeFailure 播种后探测失败 → drift，带 reason。
func TestDriftFlipsOnProbeFailure(t *testing.T) {
	m := NewDriftMonitor("")
	m.ObserveCatalog(60, true)
	m.ObserveCatalog(0, false)
	st := m.Status().(map[string]any)
	if st["state"] != "drift" {
		t.Fatalf("after probe failure: state=%v want drift", st)
	}
	if st["drift_reason"] != "catalog_probe_failing" {
		t.Fatalf("drift_reason=%v want catalog_probe_failing", st["drift_reason"])
	}
}

// TestDriftFlipsOnShrink 目录条数腰斩 → drift（catalog_shrink）。
func TestDriftFlipsOnShrink(t *testing.T) {
	m := NewDriftMonitor("")
	m.ObserveCatalog(100, true)
	m.ObserveCatalog(30, true) // < 50% of 100
	st := m.Status().(map[string]any)
	if st["state"] != "drift" || st["drift_reason"] != "catalog_shrink" {
		t.Fatalf("after shrink: state=%v reason=%v want drift/catalog_shrink", st["state"], st["drift_reason"])
	}
}

// TestDriftNoFalseAlarmOnSmallShake 小幅抖动不算漂移（保守判定）。
func TestDriftNoFalseAlarmOnSmallShake(t *testing.T) {
	m := NewDriftMonitor("")
	m.ObserveCatalog(100, true)
	m.ObserveCatalog(80, true)
	if got := m.Status().(map[string]any)["state"]; got != "ok" {
		t.Fatalf("80%% of baseline: state=%v want ok", got)
	}
}

// TestDriftRecovers 漂移后连续正常观测自愈回 ok。
func TestDriftRecovers(t *testing.T) {
	m := NewDriftMonitor("")
	m.ObserveCatalog(100, true)
	m.ObserveCatalog(0, false) // drift
	m.ObserveCatalog(90, true) // 连续 1 次正常 → 仍 drift（防抖）
	if got := m.Status().(map[string]any)["state"]; got != "drift" {
		t.Fatalf("after 1 recovery obs: state=%v want still drift", got)
	}
	m.ObserveCatalog(95, true) // 连续 2 次正常 → 自愈
	if got := m.Status().(map[string]any)["state"]; got != "ok" {
		t.Fatalf("after %d recovery obs: state=%v want ok", driftRecoverAfter, got)
	}
}

// TestDriftSaveLoadRoundTrip 快照落盘 + 重启后恢复 baseline。
func TestDriftSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "protocol-snapshot.json")
	m1 := NewDriftMonitor(path)
	m1.ObserveCatalog(60, true)
	if err := m1.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}
	var snap DriftSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("snapshot not JSON: %v", err)
	}
	if snap.Version != driftSnapshotVersion || snap.Features.CatalogModels != 60 {
		t.Fatalf("snapshot=%+v", snap)
	}
	// 新实例从磁盘恢复 → state=ok（unknown 消解）。
	m2 := NewDriftMonitor(path)
	if err := m2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := m2.Status().(map[string]any)["state"]; got != "ok" {
		t.Fatalf("after Load: state=%v want ok", got)
	}
}

// TestDriftLoadMissingIsUnknown 快照文件缺失 = unknown（不报错）。
func TestDriftLoadMissingIsUnknown(t *testing.T) {
	m := NewDriftMonitor(filepath.Join(t.TempDir(), "nope.json"))
	if err := m.Load(); err != nil {
		t.Fatalf("missing snapshot must not error: %v", err)
	}
	if got := m.Status().(map[string]any)["state"]; got != "unknown" {
		t.Fatalf("missing file: state=%v want unknown", got)
	}
}
