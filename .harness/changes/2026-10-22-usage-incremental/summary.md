# usage-incremental 变更摘要

需求：`docs/prd/REQ-usage-incremental-2026-10-22.md`
分级：L2（业务逻辑 / 持久化）
ADR：`docs/adr/ADR-016-usage-incremental-wal.md`

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `internal/usage/wal.go`（新增） | WAL 侧车：`walPathOf` / `appendWAL` / `truncateWAL` / `replayWAL` / `mergeBucketMax`（逐字段取大回放） |
| `internal/usage/wal_test.go`（新增） | 5 个新用例：增量不重写基线 / force 截断 / 坏行跳过 / 取大不覆盖 / 旧 v6 可读 |
| `internal/usage/usage.go` | ①`Recorder` 加 `walDirty`/`walMonthly`/`walCount` 脏集；②`Add()` 改 COW 发布（拷副本改完换指针，标记脏集）；③`SnapshotWindow` 锁内改取 `[]*bucket` 指针切片（不再整桶深拷贝）；④`flush()` 改「WAL 追加 + 阈值/force 才重写基线并截断 WAL」；⑤`load()` 加基线缺失分支 + 末尾回放 WAL |
| `internal/usage/cap.go` | 两处折叠合并（hour→day、day→ancient）改 COW 发布 + 标记 walDirty |

**没做的（明写）**：
- 不改 `cmd/server/config.go`（并行分支在动）；walCheckpointAfter 是包内常量，无全局开关
- 不做 WAL 压缩合并器 / 删除记录（折叠掉的桶无 WAL 删除 op，极端时序下短暂复活，下次基线自愈）
- 不动 wire 形状（`/status`、`/v1/models` 字段零变化）；`bucketUSD` 四态折算一行未动

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 全量测试 | `go test ./...` | 20 ok + 3 no test files（cmd/credit、signin、trial） |
| vet / gofmt | `go vet ./...` / `gofmt -l ./cmd ./internal` | gofmt 无输出（初次漂移已 `-w` 修） |
| 门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 10 项全绿 |

**负向探针**：旧 WAL 记录（pt=80）vs 新基线（pt=100）→ 回放后取大=100，不被旧记录拉回
（`TestWALReplayTakesMax`）；WAL 注入半截坏行 → 跳过不炸、好行正常回放（`TestWALBadLineSkipped`）。

## 三、未完成 / 待补（诚实清单）

- [ ] **折叠复活窗口**：Rollup 折叠掉的桶无 WAL 删除记录；若崩溃恰好落在「折叠后、下次基线上线前」，
      被折走的旧桶会从旧 WAL 记录短暂复活（下次基线自愈）。requires：需要一个 WAL delete op
      或折叠时写一条「失效」记录才能消解——当前判定为低概率极端（仅容量压力 >400k 触发），不做。
- [ ] **config 开关**：walCheckpointAfter 若要做成可配，需动 `cmd/server/config.go`——本分支禁碰，
      requires：并行 config 分支合并后再议。

## 四、怎么回滚

`git revert` 本提交即回到「全量深拷贝 + 全量重写」旧行为。旧二进制不读 `usage.wal.jsonl` 侧车，
残留 WAL 文件被忽略、无害。无凭证改动。

## 五、Learning

根因：快照要「一致时点」与「不持锁过久」天然冲突——解法是把不可变性下放到数据对象
（COW 发布），而不是在锁策略上绕。
