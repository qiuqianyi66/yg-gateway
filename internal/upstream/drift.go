package upstream

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DriftMonitor 上游协议漂移观测层（R-102，P1）。
//
// 为什么需要：CodeBuddy 上游的响应形状（模型目录端点、目录条数）一旦静默变更，
// 网关不会报错——选号照常跑、请求照常透传，但模型目录悄悄变空/变少，面板与
// 客户端拿到的 /v1/models 悄悄缩水。这类漂移没有任何错误码可挂，只能靠
// 「播种基线 → 周期比对」主动发现。本层只做观测：不改动任何出站报文（R8 红线），
// 不改写上游响应，只把观测到的响应形状与首见快照比对。
//
// 四态纪律：快照文件缺失 / 从未播种 = unknown（不是「无漂移」）。Status() 在
// 未播种时返回 state="unknown"，由调用方决定是否输出该键（/status 口径：
// 未启用整个 drift 模块时省略键，与 task_metrics 同纪律）。
//
// 判定规则（保守，宁漏报不误报）：
//   - 首次观测：播种快照，state=ok（这是 baseline，不记漂移）。
//   - state=ok 时：目录探测连续失败 → drift（reason=catalog_probe_failing）；
//     探测成功但模型条数较播种值缩水 ≥50% → drift（reason=catalog_shrink）。
//   - state=drift 时：连续 driftRecoverAfter 次正常观测 → 自愈回 ok
//     （上游回滚/修复后不需要重启进程）。
type DriftMonitor struct {
	mu sync.RWMutex

	path string // 快照文件路径（保存用；空 = 不落盘，纯内存）

	// snapshot 播种基线；nil = 未播种 = unknown。
	snapshot *DriftSnapshot

	state       string // "unknown" | "ok" | "drift"
	driftReason string
	driftSince  time.Time

	// consecutiveOK 漂移后连续正常观测计数（自愈用）；ok 态下也累计，
	// 仅用于日志可读性，不参与判定。
	consecutiveOK int
}

// DriftSnapshot 协议快照：上游响应特征的「首次播种」基线。
// 带 Version：未来特征集扩展时旧快照可识别、可作废重播种。
type DriftSnapshot struct {
	Version    int           `json:"version"`
	ObservedAt time.Time     `json:"observed_at"`
	Features   DriftFeatures `json:"features"`
}

// DriftFeatures 被观测的上游响应形状特征集（只观测、不改写出站）。
type DriftFeatures struct {
	// CatalogModels 模型目录探测到的条数（v3/config ∪ console 合并目录）。
	// 目录条数腰斩是上游目录端点形状变更最常见的可观测信号。
	CatalogModels int `json:"catalog_models"`
}

// driftSnapshotVersion 当前特征集版本。特征集加字段时 +1，旧快照按 unknown 处理。
const driftSnapshotVersion = 1

// driftRecoverAfter 漂移后连续多少次正常观测才自愈回 ok（防抖：单次抖动不反复翻牌）。
const driftRecoverAfter = 2

// driftShrinkRatio 模型条数缩水到播种值的该比例以下即判漂移（保守：腰斩才算）。
const driftShrinkRatio = 0.5

// NewDriftMonitor 构造观测器。path 为空表示纯内存模式（不落盘，重启即重新播种）。
func NewDriftMonitor(path string) *DriftMonitor {
	return &DriftMonitor{path: path, state: "unknown"}
}

// Load 从快照文件恢复播种基线。文件缺失 = unknown（不报错，四态纪律）；
// 文件存在但版本不符/损坏 = 报错（调用方决定是否重置）。
func (m *DriftMonitor) Load() error {
	if m.path == "" {
		return nil
	}
	raw, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 快照缺失 = unknown，不是错误
		}
		return err
	}
	var snap DriftSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return err
	}
	if snap.Version != driftSnapshotVersion {
		// 版本不符：旧基线作废，回到 unknown 等重新播种（不静默沿用错误基线）。
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot = &snap
	m.state = "ok"
	m.consecutiveOK = 0
	return nil
}

// ObserveCatalog 喂一次模型目录探测观测（周期探测 goroutine 调用）。
// probeOK=false 表示探测本身失败（目录端点形状可能已变）；models 为探测成功时的目录条数。
func (m *DriftMonitor) ObserveCatalog(models int, probeOK bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 未播种：首次成功观测才播种（连续失败的探测不播种——失败形态可能是本地问题，
	// 不是上游基线）。播种即 ok，这是 baseline 不是漂移。
	if m.snapshot == nil {
		if probeOK {
			m.snapshot = &DriftSnapshot{
				Version:    driftSnapshotVersion,
				ObservedAt: time.Now(),
				Features:   DriftFeatures{CatalogModels: models},
			}
			m.state = "ok"
			m.consecutiveOK = 1
			log.Printf("INFO: [drift] protocol snapshot seeded: catalog_models=%d (first observation)", models)
		}
		return
	}

	if !probeOK {
		m.observeLockedFailure()
		return
	}
	m.observeLockedOK(models)
}

// observeLockedOK 记录一次成功观测（调用方持锁）。
func (m *DriftMonitor) observeLockedOK(models int) {
	m.consecutiveOK++
	switch m.state {
	case "ok":
		// 缩水检测：目录条数腰斩才算漂移（保守，避免正常上下播抖动反复翻牌）。
		if m.snapshot.Features.CatalogModels > 0 &&
			models < int(float64(m.snapshot.Features.CatalogModels)*driftShrinkRatio) {
			m.flipDriftLocked("catalog_shrink",
				"catalog models=%d below %.0f%% of seeded baseline=%d", models, driftShrinkRatio*100, m.snapshot.Features.CatalogModels)
			return
		}
	case "drift":
		// 自愈：连续多次正常观测才翻回 ok（防抖）。
		if m.consecutiveOK >= driftRecoverAfter {
			m.state = "ok"
			m.driftReason = ""
			m.driftSince = time.Time{}
			log.Printf("WARN: [drift] recovered (probe healthy x%d): catalog_models=%d", driftRecoverAfter, models)
		}
	}
}

// observeLockedFailure 记录一次探测失败（调用方持锁）。
func (m *DriftMonitor) observeLockedFailure() {
	m.consecutiveOK = 0
	if m.state == "ok" {
		m.flipDriftLocked("catalog_probe_failing", "model catalog probe failing (seeded baseline=%d models)", m.snapshot.Features.CatalogModels)
	}
}

// flipDriftLocked 翻牌到 drift（WARN 日志——R10 不静默吞；漂移必须让运维看见）。
func (m *DriftMonitor) flipDriftLocked(reason, format string, args ...any) {
	m.state = "drift"
	m.driftReason = reason
	m.driftSince = time.Now()
	log.Printf("WARN: [drift] protocol drift detected: %s ("+format+")", append([]any{reason}, args...)...)
}

// Status 返回 /status 暴露视图（四态 map）。
//
// 缺失 ≠ 无漂移：未播种时输出 state="unknown" 与空 features，而不是省略键/报 ok。
// 调用方（/status）在整个 drift 模块未启用时省略本键；模块启用但未播种时键在、值为
// unknown——两者语义不同，不得合并。
func (m *DriftMonitor) Status() any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	view := map[string]any{
		"state": m.state,
	}
	if m.snapshot != nil {
		view["seeded_at"] = m.snapshot.ObservedAt.UTC().Format(time.RFC3339)
		view["seeded_features"] = m.snapshot.Features
	}
	if m.state == "drift" {
		view["drift_reason"] = m.driftReason
		view["drift_since"] = m.driftSince.UTC().Format(time.RFC3339)
	}
	return view
}

// Save 把播种快照落盘（best-effort：失败仅记日志，不阻塞观测主路径）。
// 不落盘配置（path 空）直接返回 nil。
func (m *DriftMonitor) Save() error {
	if m.path == "" {
		return nil
	}
	m.mu.RLock()
	snap := m.snapshot
	m.mu.RUnlock()
	if snap == nil {
		return nil
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
