# Rollback — bugfix-authlink（2026-10-22）

## 回滚方式

`git revert <本提交 hash>` 一条命令即可。本批次不涉及数据迁移、凭证文件、wire 协议字段
或部署产物，纯代码回滚无副作用。

## 各改动点的逆操作（若手工回滚）

| 编号 | 文件 | 逆操作 |
|---|---|---|
| X-1 | `internal/server/anthropic.go` | 错误分支改回只 `WriteHeader(iw.status)` 不写 body 缓冲 |
| X-2 | `internal/server/anthropic.go` | `http.MaxBytesReader` 包装改回裸 `io.ReadAll` |
| X-3 | `internal/server/handler.go` | 删除 `committed bool` 终止逻辑（恢复原级联续写） |
| X-4 | `internal/server/handler.go` | known 表删 `"/v1/messages"` 条目 |
| F-4 | `internal/upstream/openai_compat.go` + `internal/server/handler.go` | `forwardToOne` 改回 `http.DefaultClient.Do`；删 `OpenAICompatEndpoint.Do` |
| A-1 | `internal/auth/auth.go` | 错误串改回完整 UID |
| A-2 | `internal/auth/auth.go` | skipped 分支删 `lockdownSkippedFile` 调用 |

## 回滚后验证

1. `go build ./...` + `go test ./internal/server ./internal/auth ./internal/upstream -count=1`
   → 新增 5 个 bugfix 测试应失败（回滚后行为回归旧语义，测试即红——预期，证明测试钉住了修复）。
2. 注意 A-2 回滚后，半写坏凭证文件将回到旧 ACL 姿态——回滚前确认生产 `auths/` 无坏文件在途。
