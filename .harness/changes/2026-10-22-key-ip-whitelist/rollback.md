# 2026-10-22-key-ip-whitelist 回滚方案

## 触发条件

- 403 ip_not_allowed 误拒合法客户端（NAT/出口漂移）或单测红
- 部署退出码 2/4

## 回滚步骤

1. 配置层（最安全，立即生效）：config.json 删除所有 `api_keys[].allowed_cidrs` 键，
   重载/重启即回到"不限来源"。
2. 代码层：`git checkout HEAD~ -- internal/httpauth/index.go internal/httpauth/ipallow_test.go
   cmd/server/config.go cmd/server/main.go cmd/server/api_keys_config_test.go
   internal/server/handler.go internal/server/ip_whitelist_test.go`，重新构建换版。
3. `auths/` 不参与回滚。

## 回滚后必须验证

- `/healthz` 200；`go test ./internal/httpauth ./internal/server ./cmd/...` 绿
- 配了 allowed_cidrs 的 Key 在删除该键后任意 IP 可访问（行为回到改造前）

## 已知风险

字段热生效：回滚代码后须重启加载旧二进制；仅删 config 键则无需重启即恢复不限。
