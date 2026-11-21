# 变更：Pick 热路径 top-k 截断 + LRU 全序 + 护栏短路

- 日期：2026-11-21
- 分诊：L2（选号截断实现 + LRU tie-break 修正；决策集合不变，CPU profile 驱动）
- 前置：`d85c95d`

## 为什么

`BenchmarkPick46Accounts` 改前（46 账号）实测约 4.70–4.93 µs/op。CPU profile
（`go test -bench ... -cpuprofile`）显示：

- `slices.pdqsortCmpFunc` 累计约 28%——但对 46 个候选做全排序，只为了取 top5；
- `auth.(*Auth).Realm` 约 14%——默认配置下 `minIntervalBlocked` 对每个候选无条件
  调 `IsGlobal()`（内部 `Auth.Realm()` 要拿互斥锁），两个限流阈值都为 0 时纯属白锁。

另外发现两处「意图与实现不符」：

- `top5` 之后 `candsAll` 被当作全量 LRU 列表，但 LRU 只看 `usedSeq`；重启后
  `usedSeq` 全为 0，会比较不出结果，退化成按 map 遍历顺序选号，持久化的
  `lastUsed` 完全没参与。
- 等权重 Fisher-Yates 洗牌分支对结果无影响：比较器以 `cost1k → w → UID` 严格全序，
  top5 集合与输入排列无关；洗牌只改中间顺序，且 LRU 也不依赖该顺序。

## 改动

- `internal/pool/pick.go`
  - 新增 `topKBy[T]`（原地维护长度 k 的有序前缀，`len<=k` 退化为全排序）；
    `selectFromCandidates` 用 `topKBy(ws, 5, cmpCand)` 取代全量 `slices.SortFunc`。
  - 删除全排序后的 `cands` 重建：`eligible` 直接从 top5 的 `weighted` 过滤，
    `candsAll` 保留原始全候选；LRU 不依赖顺序。
  - LRU 排序键改为 `lastUsed → usedSeq → UID`：restart 后 `usedSeq=0` 也能按
    持久化 `lastUsed` 选最旧；时间戳相等再由 `usedSeq`/UID 保证确定性。
  - 删除等权重洗牌分支（证明为 no-op；注释保留推导与删除理由）。
- `internal/pool/pool.go` / `guards.go`：新增 `minRequestIntervalFor(e)` 单一实现；
  两阈值均 0 直接返回，避免 `IsGlobal()` 的 Auth 锁；`pick` 兜底路径复用。
- 注释同步：`pickSeq`/`usedSeq` 从「LRU 主排序键」改为「lastUsed 相等时 tie-break」。
- 测试：`internal/pool/topk_test.go`——随机数组下 top-k 与全排序前 k 逐位一致、
  热路径零额外分配。

## 实测

同一台 i7-14650HX，`go test ./internal/pool -run '^$' -bench BenchmarkPick46Accounts -benchmem -count=5`：

| 指标 | 改前（d85c95d） | 改后 |
|---|---|---|
| 耗时 | 4.70–4.93 µs/op | **2.87–3.10 µs/op** |
| 分配 | 2768 B/op，4 allocs/op | 2768 B/op，4 allocs/op（未变） |

profile 复测：全排序热点消失，`topKBy` 约占 10%，`auth.Realm` 不再出现在前 14。

正确性：`go test ./internal/pool -count=1` 全绿（含 starvation/LRU/成本分层/保底全套）；
`topK_test.go` 对 n=0..60、k=0..5 的所有随机数组与 `slices.Sort` 前 k 比对通过。

## 回滚

`git revert <this-commit>`。回退后 `TestPickLRUFallbackWhenTopAllRecentlyUsed`
会重新依赖「全排序恰好把最旧号排在首位」的偶然行为；`topk_test.go` 会 FAIL。
