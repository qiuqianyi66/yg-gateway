# routing-p1p2 独立复审（2026-10-26，新会话独立执行）

> 上轮子 agent 复审两次被并发配额阻断（见 summary.md §独立审查状态），本轮由新
> 会话（与实现者不同会话）按 L-16 补做。只读不改，结论如下。

## 结论：🟢 通过（2 个小问题，不阻断）

代码整体干净，四态纪律/锁口径/默认关零回归/热路径预筛都到位。下面是逐文件
过一遍后的发现。

## 发现清单

### 🟡-1 注释与常量语义错位（softband.go:20-21）

```go
// softBandMinMult 无台账时的最小带宽倍率（floor×0.2）。
softBandMinMult = 1.2
```

注释写「floor×0.2」，常量值是 1.2。实际语义是：**带宽宽度 = floor×0.2**，
故 **upper = floor×(1+0.2) = floor×1.2**。注释把"宽度倍率"和"上界倍率"混了，
读者第一次看会以为是 `floor * 0.2`。建议改成：

```go
// softBandMinMult 无台账时的上界倍率（带宽宽度 = floor×0.2 → upper = floor×1.2）。
softBandMinMult = 1.2
```

非阻断，下一次碰 softband.go 时顺手改。

### 🟡-2 softBandFactorOf 预筛里的 magic number 0.01 无注释（softband.go:68）

```go
if e.credits > p.creditFloor+int64(float64(p.creditFloor)*(softBandMaxWidthMult+0.01)) {
```

`+0.01` 是浮点边界安全余量（避免 floor×1.5 恰好等于 upper 时的边界抖动），
但注释只写了"物理上不可能带内"，没解释为什么 +0.01。建议补半句：

```go
// +0.01：浮点边界余量，避免 credits 恰 = floor×1.5 时因 round 抖动漏进中位数计算。
```

非阻断。

## ✅ 核对过没问题的点

- **锁口径**：`recentTokens/recentTokensAt` 写于 `RecordTokenUsageWithTime` 持
  p.mu.Lock()，读于 `recentUsageFactorOf` 持 p.mu.RLock()——RWMutex 互斥正确。
  float64 在 amd64 上单字对齐，与既有 ewmaSuccess 等字段口径一致（不引入新风险）。
- **衰减数学**：写路径 `decayRecentTokensTo(t1)` 把存储值衰减到 t1 再累加；读路径
  `recentUsageFactorOf(t2)` 再从 t1 衰减到 t2。两次衰减接力正确，没有双重衰减
  导致的指数级塌陷。
- **四态**：`HasTotalTokens && >=0` 才入账；缺字段的 delta 不写 recentTokens
  （unknown 不当 0 记）——与项目四态纪律一致。
- **P1 硬拦边界**：`e.credits <= p.creditFloor` 在 softBandFactorOf 开头就返回
  `(1,false)`，≤floor 不进软带；硬拦仍由 `floorBlockedForRealmModel` 在 pick
  循环里 continue。软带不是逃逸口，测试锚定。
- **热路径预筛**：5ab1dec 加的 `credits > floor×1.51 恒带外` 一行，把中位数
  sort 从全候选降到 (floor, floor×1.5] 区间号——与 cap=0.5 数学上闭合
  （band upper ≤ floor×1.5，超出者物理不可能带内）。
- **pickReason.Softband**：选中号打标，决策溯源可观测。注意 line 307 对选中号
  又调了一次 `softBandFactorOf`（routingWeightOf 里已调过一次）——每 pick 多
  一次 O(1) 调用（预筛直接返回），可忽略，不必缓存。
- **默认关零回归**：`recentUsageFactor` config 默认 false；`creditFloor<=0` 时
  softBandUpper 返回 0、softBandFactorOf 第一行返回 (1,false)。无 floor 的部署
  行为与改动前逐位一致。
- **R10/R9/R6/R1/R8**：未新增 goroutine、未吞错误、未散点出站、未碰凭证、
  未改上游协议字符串。

## 建议（不强制）

1. 下一次碰 softband.go 时把上面两个 🟡 注释对齐。
2. 20k token/请求假设、200k scale、1h 半衰期三个常数目前是 REQ 里写明的假设，
   等 V3-3 生产换版 48h 数据回填后按实测中位数校准（summary 遗留段已记）。

## 证明力声明

本复审由新会话执行（与 08b5bd6/5ab1dec 的实现会话不同），但未跑测试（只读
代码 + diff + 既有 summary）。数值正确性由既有测试 `TestSoftBand*` /
`TestRecentUsage*` 锚定，本复审不重跑。按 L-16，此条作为「独立代码复审」
入库，测试覆盖仍以 pre-commit 为准。
