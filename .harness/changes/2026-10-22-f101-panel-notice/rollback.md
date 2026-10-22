# 2026-10-22-f101-panel-notice 回滚方案

## 触发条件（什么情况下必须回滚）

- 面板页面白屏/脚本加载失败，影响账号池管理主路径
- 部署退出码 2（健康检查失败已自动回滚）或 4（旧版没能起来，生产可能不可用）

## 回滚步骤

1. 纯前端展示改动：`git checkout -- internal/panel/index.html internal/panel/index.go internal/panel/panel.go`；新增文件 `internal/panel/acl_notice.js`、`internal/panel/panel_features_test.go` 随本次提交整体 revert。
2. 重新构建换版走 `deploy-prod.ps1`（不在本仓手工分步）；凭证目录 `auths/` **不参与回滚**。

## 回滚后必须验证

- `/healthz` 200、`/v1/models` 返回、流式与非流式各一发
- `git log --oneline -1` 与磁盘一致

## 已知风险 / 回滚不了的部份

无二进制状态改动；若合并入新构建后出问题，回滚本提交即恢复旧前端，无残留状态。
