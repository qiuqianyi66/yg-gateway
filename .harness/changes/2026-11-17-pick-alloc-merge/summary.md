# 变更：选号热路径合并平行切片（Pick 4 allocs/op）

- 日期：2026-11-17
- 分诊：L1（纯内存布局，决策语义零变化）
- 前置：`5dc3ca0`

## 为什么

`BenchmarkPick46Accounts`（46 账号真实规模）优化前实测：

```
5.2–5.4 µs/op    2768 B/op    5 allocs/op
```

5 次分配逐条对账：`candTier []int`、`candCost []float64`、`ws []weighted`、
`lats []float64`（中位延迟）、`weights []int64`（定点抽签）。

其中 **`candTier` 与 `candCost` 是同索引、同生命周期、按同一循环填充的两个平行切片**
——本质是一条记录被拆成两个数组，白付一次堆分配。

## 改动

`internal/pool/pick.go` 的 `selectFromCandidates`：

```go
// 前：两次分配
candTier := make([]int, len(cands))
candCost := make([]float64, len(cands))

// 后：一次分配
type candInfo struct {
    tier   int
    cost1k float64
}
infos := make([]candInfo, len(cands))
```

三处读点同步改（`hasTier1` 探测、`ws` 构建的过滤与字段填充）。

## 实测

| 指标 | 前 | 后 |
|---|---|---|
| 耗时 | 5.2–5.4 µs/op | 5.0–5.1 µs/op |
| 分配次数 | 5 allocs/op | **4 allocs/op** |
| 分配字节 | 2768 B/op | 2768 B/op（结构体合计仍是每候选 16 B，只省了分配器开销） |

`go test ./internal/pool/ -count=1` 全绿；`go build ./...` = 0。

## 审计过但**没有**改的优化（附理由，避免下轮重复提）

| 候选 | 为什么不做 |
|---|---|
| `pickWeighted` 对 top5 重算 `routingWeightOf`（与 `ws` 已算的权重重复 5 次） | 要改签名（`[]*entry` → `[]weighted`），连带 `pick_weighted_basis_test.go` 的口径测试；省的是每 Pick 5 次权重计算（46 账号下约 11% 权重计算量），换来的 ABI 变更与测试改动不成比例 |
| `poolMedianLatencyMs` 的 `sort.Float64s`（O(n log n)） | n = 候选数（46 量级），排序约 250 次比较，微秒级；换 quickselect 的收益低于代码复杂度 |
| `poolMedianLatencyMs` 的 `lats` 分配 | 若改固定数组会因 append + sort 逃逸，反而不省栈 |
| `pickWeighted` 的 `now := p.now()` 与调用方 `now` 不同源 | 同一次 Pick 内取两次时间会带来极小的权重漂移，但这属于**行为变更**（哪怕毫秒级），不在"纯优化"范围内，需单独评估 |
| `slices.SortFunc` / 定点抽签 / 固定 `[5]*entry` 免分配 / `costTier` 预计算 | 2026-11-14 已做过，实测已是当前形态 |

## 牙齿

分配次数是**可测的**副产物，无需行为断言：`go test -bench BenchmarkPick46Accounts -benchmem`
的 `allocs/op` 列直接给出 5 → 4。任何"优化"若不改变这一列，就不是分配优化。

## 回滚

`git revert <this-commit>`。纯内存布局，决策路径逐位不变（`go test ./internal/pool/` 全绿）。
