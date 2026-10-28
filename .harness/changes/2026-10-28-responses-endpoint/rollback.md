# Rollback — 2026-10-28 responses-endpoint (F8)

## 回滚方式

`git revert <F8 commit>` 单提交回滚。改动的都是新增文件 + 两个既有文件的局部改动，
无数据/配置/二进制影响。

## 受影响文件

| 文件 | 改动 | 回滚动作 |
|---|---|---|
| `internal/server/responses.go` | 新增 | 删除 |
| `internal/server/responses_out.go` | 新增 | 删除 |
| `internal/server/responses_test.go` | 新增 | 删除 |
| `internal/server/handler.go` | +2 行（路由注册 + outProto 标记） | revert 还原 |
| `internal/server/chatrun.go` | chatRun 加字段 + serveNonStream 分支 | revert 还原 |
| `docs/prd/REQ-responses-endpoint-2026-10-28.md` | 新增（落地状态） | revert 还原 |
| `docs/REQ-INDEX.md` / `docs/PLAN-frontend-functional-2026-10-27.md` / `README.md` | 行回填 | revert 还原 |

## 回滚后检查

- `go test ./internal/server`（既有 chat 测试全绿，确认 outProto 分支移除无残留）
- `gofmt -l ./internal ./cmd` 无输出
- `/v1/responses` 恢复 404 错误信封（客户端回落 chat 协议不受影响）
