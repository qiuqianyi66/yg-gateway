# 2026-10-22-split-autotask 回滚方案

## 触发条件（什么情况下必须回滚）

- 编译失败或面板测试红（拆分引入未发现的行为差异）
- 部署退出码 2（健康检查失败已自动回滚）或 4（旧版没能起来）

## 回滚步骤

1. 纯同包文件拆分：`git checkout HEAD~ -- internal/panel/autotask.go`；删除新增的
   `internal/panel/autotask_mp.go`、`internal/panel/autotask_scripts.go`。
2. 重新构建换版走 `deploy-prod.ps1`（不在本仓手工分步）；凭证目录 `auths/` **不参与回滚**。

## 回滚后必须验证

- `/healthz` 200、`/v1/models` 返回、流式与非流式各一发
- `go test ./internal/panel` 绿
- `git log --oneline -1` 与磁盘一致

## 已知风险 / 回滚不了的部份

wire 形状零变化（仅同包移动函数）；回滚本提交即恢复单文件，无残留状态。
