# 2026-11-19 入口 body 形状守卫

## 改了什么

1. 新增 `internal/server/body_shape.go`：`bodyIsJSONObject`（O(1) 首字节判定）+
   `rejectIfNotJSONObject`（非对象 → 400 invalid_request）。
2. 四个入口在读完 body 后立即判：`chatflow.go`、`anthropic.go`、`responses.go`、
   `count_tokens.go`。
3. `internal/upstream/payload.go` `ensureConsoleSystem` 守卫与另三处统一为
   `err != nil || obj == nil`（它写前有 `!ok → return`，本就安全；统一是防后人误读）。
4. `cmd/server/main.go` `loadConfigMasked`：`json.Marshal` 出字面 `null` 时返回错误，
   不再把 `null` 原样回给面板。
5. 两条既有用例按新语义重写（`anthropic_error_paths_test.go`、
   `chatflow_test.go` case 3），保留变更理由。

## 怎么验证

| 验证 | 命令 | 结果 |
|---|---|---|
| 形状判定边界 | `go test ./internal/server -run TestBodyIsJSONObject` | ok（含 `null`/`[]`/标量/空/纯空白） |
| 四入口拒绝 | `-run TestNonJSONObjectBodyRejectedAtEntry` | ok（4 路径 × 6 body） |
| 不误伤 | `-run TestJSONObjectBodyNotRejected` | ok |
| 牙齿 | 注释 chat 入口调用后复跑 | **FAIL**（503/200 而非 400） |
| 包级 | `go test ./internal/server ./internal/upstream ./cmd/server` | 全 ok |

## 回滚

删四个调用点 + 删 `body_shape.go` / `body_shape_test.go` 即回旧语义。
