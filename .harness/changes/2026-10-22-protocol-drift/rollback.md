# 2026-10-22-protocol-drift 回滚方案

## 触发条件

- 漂移探测 goroutine 导致上游异常 / 主链路受影响（观测层本应零侵入，若发生即回滚）。
- /status 新增键破坏既有消费方。

## 回滚步骤

1. `git revert <本提交>`；删除 `internal/upstream/drift.go`、`drift_test.go`。
2. 还原 config.go（drift 节）/ main.go（driftMon 装配 + runDriftProbe）/ handler.go（Drift 字段 + protocol_drift 键）。
3. 快照文件 `data/protocol-snapshot.json` 可保留（下版重新播种）或删除。

## 回滚后必须验证

`go test ./...` 全绿；`/status` 不再含 protocol_drift 键。

## 已知风险

观测层只读不改写出站，r8_golden 钉死报文；快照损坏回 unknown 不阻塞启动。
