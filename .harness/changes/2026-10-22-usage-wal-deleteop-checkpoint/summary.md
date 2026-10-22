# usage-wal-deleteop-checkpoint 变更摘要

需求：`docs/prd/REQ-usage-incremental-2026-10-22.md` §六 v2 修订
分级：L2（业务逻辑 / 持久化）
ADR：`docs/adr/ADR-016-usage-incremental-wal.md` §六修订记录（同主题，不新增编号）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `internal/usage/wal.go` | `walRecord` 加 `D string`（delete 键）；`replayWAL` 顺序处理 delete（`delete(buckets,D)`，不套 merge-max）；头注释写顺序权威语义；包常量改名 `walCheckpointAfterDefault` |
| `internal/usage/usage.go` | Recorder 加 `walDeletes` + `walCheckpointAfter` 实例字段；新增 `NewWithCheckpoint(path, n)`（`New(path)` 保留默认构造）；flush **先 delete 后 upsert**、用实例阈值、写完清空 walDeletes |
| `internal/usage/cap.go` | 两处折叠（hour→day、day→ancient）源键加入 `walDeletes` |
| `internal/usage/wal_test.go` | +5 用例：折叠-崩溃-往返、delete+upsert 重立、delete 与 merge-max 共存、阈值触发基线、默认回落 |
| `cmd/server/config.go` | 既有 `Usage` 节加 `wal_checkpoint_after`（装配期，<=0 回落默认） |
| `cmd/server/main.go` | `newUsageRecorder(path, n)` 传 `cfg.Usage.WalCheckpointAfter` |

**没做的**：不做 WAL delete 与 monthly 的联动（monthly 无折叠删除场景）；不做配置热生效（装配期）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| usage 单测 | `go test ./internal/usage` | 全绿（含 +5 新用例） |
| 全量 | `go test ./...` | 20 ok + 3 no test files |
| 门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 10/10 |

**负向探针**：delete 后源桶复活 → 测试断言不存在（TestWALDeleteRemovesFoldedSource）；
merge-max 之后 delete → 测试断言键被确定性删除（TestWALDeleteCoexistsWithMergeMax）。

## 三、未完成 / 残留

- [ ] 折叠复活窗口已消除（delete op 落地），无残留。
- [ ] 阈值若要面板 UI 调节，需另立 REQ 接 livecfg——当前刻意装配期。

## 四、怎么回滚

`git revert` 本提交。旧二进制不读 WAL 的 D 字段（JSON 未知字段忽略），向后兼容；
`wal_checkpoint_after` 配置缺省即默认，旧二进制忽略该键。无凭证改动。

## 五、Learning

折叠「合并目标 + 删除源」必须成对落盘，否则 WAL 只记 upsert 会在重启回放时把删掉的源
从旧记录里「复活」——状态转移要把删除也当成一等变更记下来。
