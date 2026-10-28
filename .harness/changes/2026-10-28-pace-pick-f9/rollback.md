# Rollback — 2026-10-28 pace-pick-f9 (F9)

## 回滚方式

`git revert <F9 commit>` 单提交回滚。代码改动 + 文档，config 默认关，无生产行为
影响（除非已手动开启 `pool.pace_mode`——开启则回滚后重载配置回到常量语义）。

## 受影响文件

| 文件 | 改动 | 回滚动作 |
|---|---|---|
| `internal/pool/pace.go` | 新增 | 删除 |
| `internal/pool/pace_test.go` | 新增 | 删除 |
| `internal/pool/pick.go` | 签名 + medianPace 分支 | revert 还原 |
| `internal/pool/pool.go` | paceEnabled 字段 | revert 还原 |
| `internal/pool/ewma_test.go` / `softband_recent_test.go` / `pool_test.go` / `pick_weighted_basis_test.go` | 调用点补参 | revert 还原 |
| `cmd/server/config.go` / `main.go` | PaceMode 字段 + 接线 | revert 还原 |
| `docs/prd/REQ-pace-pick-2026-10-28.md` | 新增 | revert 还原 |
| `docs/REQ-INDEX.md` / `PLAN-frontend-functional-2026-10-27.md` / `README.md` | 行回填 | revert 还原 |

## 回滚后检查

- `go test ./internal/pool`（既有 expiring 权重/分布测试全绿）
- `gofmt -l ./internal ./cmd` 无输出
- config 无 `pace_mode` 字段引用残留（revert 后 grep 确认）
