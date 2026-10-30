# 2026-10-30-pool-failure-window — 账号级故障窗（P0-2）

> 状态：**已实现，门禁全绿，待部署**。
> REQ：`docs/prd/REQ-pool-failure-window-2026-10-30.md`。

## 改了什么

- `internal/pool/pool.go`：新增 `failureWindow` 字段 + `SetFailureWindow(d)`（0/负值 =
  关闭，零回归；热生效，同 pool.* 其余 Set* 口径）。
- `internal/pool/pick.go`：`pick` 的 `healthyOf` 增加 `failureWindowBlocked` 判定——
  最近失败（lastErr）落在窗内的账号临时退出 **normal 候选**（healthy() 不加本判定：
  兜底/粘性路径不受影响）。`pickEarliestExpiryLocked`：窗内号（expiry 零值但仍在窗）
  以 `lastErr + 窗` 作为恢复时刻参与兜底——全池皆窗内时兜底仍返回最早失败者，不 503；
  兜底日志同步显示窗内恢复时刻（新增 bestExp 变量，替代循环内重算 expiry）。
- `internal/pool/state.go` / `internal/pool/degrade.go`：`NoteError` / `NoteFailures`
  写 lastErr 改用 `p.now()`（与 pick 窗判定同时钟口径，同 recordBreakerFailureLocked
  先例）；`NoteFailures` 补喂 lastErr（此前只更新 consecutiveFails，未知失败不触发窗）。
- `cmd/server/config.go`：`pool.failure_window`（默认 "30s"；"0" 关闭；非法/负值归一
  + 报错）→ `FailureWindowDur`。
- `cmd/server/main.go`：装配 + 热生效各一行 `SetFailureWindow`。
- 测试：`internal/pool/failure_window_test.go`（新，5 例：默认关零回归 / 窗内排除窗
  外回池 / NoteFailures 喂入 / 全池窗内兜底 / 单号池兜底）；
  `cmd/server/config_test.go`（默认 30s 断言 + `TestPoolFailureWindowNormalize`）。

## 怎么验证的

- `go test ./internal/pool/ -run FailureWindow -v`：5 例全过；`go test ./internal/pool/`
  全量 ok；`go test ./cmd/server/ -run FailureWindow` 过。
- 全量 `scripts/pre-commit.ps1`：11/11 通过（修复 gofmt 4 文件 + PRD 必要性段后）。
- 已知存量问题（非本变更面，未触碰）：`internal/server/models_handler.go` 与
  `internal/upstream/client.go` 在 HEAD 即存在 gofmt 漂移（上次 summary 已记录），
  本机 pre-commit 只查改动文件不受影响；CI `-AllFiles` 会拦，需另行处理。

## 怎么回滚

- 配置层：`pool.failure_window` 设 "0" 热生效即回退（无需重启，零回归）。
- 代码层：revert 本次提交（pool/pick/state/degrade/config/main + 测试 + REQ +
  summary），全部为新增行/新增文件，无删除与协议变化，可安全 revert。
- 部署层面：`deploy-prod.ps1` 已内建备份与失败自动回滚。
