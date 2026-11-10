# REQ — 归档写入 goroutine 加 panic 隔离（2026-10-22 审查 C-4）

## 必要性

`internal/reqlog` 的 `archiveWriter.run()` 是独立 goroutine（每秒 flush + prune，队列逐条写盘）。
全仓非测试代码里 `recover()` 只有 1 处（`internal/upstream/sse.go:1013`），归档写入这条没有。
HTTP handler 的 panic 由 net/http 自己兜住，但**后台 goroutine 的 panic 会直接带走进程**——
对一个 7×24 常驻网关，代价是整机重启 + 审计日志断流，而不是一次 500。

这不是"假设会 panic"：`writeEvent` 里对 `w.buf`、`w.file`、配置字段的解引用都在同一 goroutine
内，任何一处 nil/越界都会以进程退出收场，而且事后从日志里看不出是谁先死的。

## 需求

`run()` 的每次动作（write / flush / prune / close）都经过 `step(stage, fn)`：panic 被 recover 拦住，
写入 goroutine 存活，失败信封进 `w.lastErr`（面板 `ArchiveStats.LastError` 可见，R10 不静默）。

## 验收（GWT）

- Given `step("write", func(){ panic("boom") })` → Then 调用方不 panic，`errString()` 含 `panic`。
- Given panic 之后再 `enqueue` 一条事件 → Then 该事件在 4s 内落盘（writer 没死）。
- Given 去掉 recover（变异） → Then `TestArchiveWriterStepSurvivesPanic` 报红
  （`LastError="" 未记录 panic`）。
- Given 全量门禁 → Then `pre-commit.ps1` 退出码 0。

## 不做

- 不记 panic 的值（只记 `%T` 类型）：Event 可能带客户端信息，panic 值进错误串有脱敏风险（R2）。
- 不给整个进程加顶层 recover（net/http 已按连接兜住 handler；顶层兜会掩盖状态不一致，
  属另一议题）。
- 不改归档格式、队列语义、flush/prune 周期。
