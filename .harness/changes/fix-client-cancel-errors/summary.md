# fix-client-cancel-errors

## 改了什么

- `internal/server/chatrun.go`
  - `handleTransportError` 增加客户端取消前置判定：`r.Context().Err() != nil`
    时记 499/interrupted，释放租约，不走 `NoteFailures`/`fail` 惩罚，不写响应。
  - 删除 `rotateAndServe` 末尾用循环前 `lastErr/modelBlock/groupFilteredOut`
    快照回写的逻辑，保留最后一次上游错误与阻塞状态。
- `internal/server/chatflow.go`
  - 上游错误 message 经 `upstreamMessageText` 压成可读文本（保留 code/msg/
    requestId），避免嵌套 JSON。
  - 上游 `ErrServer` 的业务码从 `no_healthy_account` 改为 `upstream_error`。
- 新增 `internal/server/chatrun_error_test.go` 两条回归：
  客户端取消不罚号/不记 503；上游 503 原文与 requestId 不丢失。

## 怎么验证

- `go test ./internal/server -count=1`：通过。
- `go test ./... -count=1`：通过（panel 包约 63s）。
- `scripts/pre-commit.ps1`：见提交前执行结果。
- 现场证据：`D:\yg-gateway\data\logs\yg-gateway-20261126-141949.err.log` 与
  `...144528.err.log` 的 `context canceled` 已对应到 4 条 503；09:45 那条仍缺
  stderr 证据，保持 unknown。

## 怎么回滚

- 还原上述两个源码文件的改动，删除新增测试文件；归档已写入的 499/interrupted
  行保留为审计事实。无配置/数据迁移。
