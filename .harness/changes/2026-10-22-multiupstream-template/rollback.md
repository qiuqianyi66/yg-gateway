# 2026-10-22-multiupstream-template 回滚方案

## 触发条件

- 级联转发后 SSE 流断 / usage 归因错 / 主聊天链路回归。

## 回滚步骤

1. `git revert <本提交>`；删除 `internal/upstream/openai_compat.go`。
2. 还原 config.go（endpoints 字段）/ main.go（buildFallbackConfig）/ handler.go（FallbackConfig 形态 + fallbackForward）/ 测试 / README。
3. config.json 无 schema 破坏：回滚后旧 fallback.base_url/api_key 仍按单端点生效。

## 回滚后必须验证

`go test ./...` 全绿；fallback 单端点转发 SSE flush + 半截流判失败 + usage realm=fallback 三条既有行为回归。

## 已知风险

配置向后兼容（空 endpoints 回退单字段）已被 buildFallbackConfig 单测覆盖；级联循环只在选号全失败后触发，主链路零改动。
