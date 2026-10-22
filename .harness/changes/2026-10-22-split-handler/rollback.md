# 2026-10-22-split-handler 回滚方案

> 纯重构任务：无部署、无数据迁移。回滚 = git revert。

## 触发条件

- `go test ./...` 出现红（预期全绿；变红即搬移破坏了同包可见性）
- `check-boundaries.ps1` / `check-depth.ps1` 门禁红

## 回滚步骤

1. `git revert <本提交 hash>`（handler.go 恢复原四段，删四个新文件）
2. 基线 json 随 revert 一并恢复
3. 数据 / 状态：**无影响**——不触碰 `auths/`、`state.json`、配置

## 回滚后必须验证

- `go build ./...` 0 error、`go test ./...` 全绿（20 ok + 3 no test files）

## 已知风险 / 回滚不了的部分

无。纯同包搬移，revert 完全可逆。
