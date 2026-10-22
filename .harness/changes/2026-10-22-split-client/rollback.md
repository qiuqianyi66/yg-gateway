# 2026-10-22-split-client 回滚方案

> 纯重构任务：无部署、无数据迁移。回滚 = git revert。

## 触发条件（什么情况下必须回滚）

- `go test ./...` 在拆分后出现任何红（预期：全绿；变红即说明搬移破坏了同包可见性）
- `check-boundaries.ps1` / `check-depth.ps1` 门禁红
- 部署后冒烟异常（本任务不改二进制行为；若误触部署，按 deploy-prod 回滚）

## 回滚步骤

1. 纯代码改动：`git revert <本提交 hash>`（恢复 client.go 原三段，删三个新文件）
2. 基线 json：随 revert 一并恢复（depth-baseline.json 回到 maxFileLines=2058）
3. 数据 / 状态：**无影响**——本任务不触碰 `auths/`、`state.json`、配置。

## 回滚后必须验证

- `go build ./...` 0 error、`go test ./...` 全绿（20 ok + 3 no test files）
- `git log --oneline -1` 与磁盘一致

## 已知风险 / 回滚不了的部分

无。本任务是纯同包搬移，revert 完全可逆。
