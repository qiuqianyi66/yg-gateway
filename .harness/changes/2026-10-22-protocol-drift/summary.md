# 2026-10-22-protocol-drift 变更摘要

> 需求：`docs/prd/REQ-protocol-drift-2026-10-22.md`
> 分级：L3（上游协议观测）
> ADR：`docs/adr/ADR-014-protocol-drift-observer.md`

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `internal/upstream/drift.go`（新增） | DriftMonitor：播种/比对/自愈/Save/Load，快照 JSON 落盘 data/ |
| `internal/upstream/drift_test.go`（新增） | 播种/失败不播种/失败翻牌/缩水翻牌/抖动不误报/自愈/Save-Load 往返/缺失=unknown |
| `cmd/server/config.go`（修改） | 新增 `drift` 配置节（enabled/interval_seconds/snapshot_path） |
| `cmd/server/main.go`（修改） | 装配 driftMon（Load 恢复基线）；新增 `runDriftProbe` 周期探测 goroutine |
| `internal/server/handler.go`（修改） | Config 加 Drift 字段；/status 增 `protocol_drift` 键（nil=省略=外层 unknown） |

**没做的（明写）**：SSE 帧级观察（sse.go/chatrun.go 不在可改域）；自动切备用（只观测不处置）；面板 UI 展示。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 单测 | `go test ./internal/upstream/ ./internal/server/ ./cmd/server/` | 全绿 |
| R8 回归 | `go test ./internal/upstream/`（含 r8_golden_test） | 通过 |
| 门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 全绿（见提交记录） |

**负向探针**：把 ObserveCatalog 缩水处理阈值调到 0.0 → TestDriftFlipsOnShrink 应红（牙齿在）。

## 三、未完成 / 待补（诚实清单）

- [ ] SSE 帧级形状观测：requires 放开 chatrun.go/sse.go 文件域（当前禁区）。
- [ ] 漂移自动处置（切备用）：另立 REQ，本只观测。

## 四、怎么回滚

`git revert <本提交>`。新增文件 drift.go/drift_test.go 删除；config.go/main.go/handler.go 三处改动还原。快照文件 protocol-snapshot.json 删了即重新播种，无副作用。

## 五、Learning

根因：观测喂点受文件域约束，周期探测 goroutine 复用既有 FetchModels 是零侵入最优解。
