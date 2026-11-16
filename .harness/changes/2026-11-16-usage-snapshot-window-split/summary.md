# 变更：把窗口聚合切成深模块（usage.go 解除最大文件压力）

- 日期：2026-11-16
- 分诊：L1（纯内部重构，无对外契约变化；`SnapshotWindow` 签名与 JSON 形状不变）
- 前置：`4121c94`

## 为什么

`internal/usage/usage.go` 是全仓最大文件（**997 行非空，阈值 1000**，只差 3 行——任何新代码都会红），
其中 `SnapshotWindow` 是最大函数（**193 代码行**）。但它长的方式不是"逻辑复杂"，而是**维度样板重复**：

- 11 个维度累加器写成 11 个局部 map，每个都要手抄「nil 就 new 再 add」两行；
- 一个巨循环里对每桶重复这 11 段；
- 过滤（窗口口径）、累加、组装三件事挤在一个函数里，改任何一段都要先读完整个函数。

删除测试：把「11 个累加器」和「逐桶累加」抽成结构体+方法后，复杂度是**集中**了（维度扩展只改一处），
而不是搬家——这是值得做的信号。

## 改动

新增 `internal/usage/snapshot_window.go`（300 行 / 代码 215），按职责三段：

| 段 | 承载 |
|---|---|
| `usageAgg` + `newUsageAgg` + `bumpAcc` | 11 个维度累加器打包成一个结构体；`bumpAcc` 取代 11 处 nil 样板 |
| `(a *usageAgg) consume` / `consumeCredit` | 一桶进全部维度（含积分三归因、倍率回填缓存） |
| `(a *usageAgg) assemble` | 维度排序、时序拼接（日→小时升序）、FileBytes、Since、窗口回显 |
| `(*Recorder) SnapshotWindow` | 只剩编排：取指针快照 → 过滤 → 逐桶 consume → assemble |

`usage.go` 保留 `Snapshot` / `SnapshotWithRates` 两个薄包装（同包调用，签名未变）。

## 数字

| 指标 | 前 | 后 |
|---|---|---|
| `usage.go` 非空行 | 997 | **764**（总 823 / 代码 607 / 注释 157） |
| `SnapshotWindow` 代码行 | 193 | 编排约 60（三段已拆出） |
| 全仓 >120 行函数 | 4 | **3** |
| `maxFileLines`（棘轮） | 997 | **942**（现最大是 `cmd/server/main.go`） |
| `oversizeFiles` | 0 | 0 |

## 验证

1. `go build ./...` = 0；`go vet ./internal/usage` = 0；`gofmt -l internal cmd` 无输出
2. `go test ./...` 全绿；`internal/usage` 覆盖率 **83.0%**（门槛 75%）
3. **牙齿验证（关键）**：把 `consume` 里 `a.total.add(b, usd)` 故意改成 `a.total.add(b, 0)`，
   `TestUSDValuePricing` **立即红**；改回后立即绿——证明既有测试真的锁住了这次搬运的口径，
   不是"搬完恰好全绿"的空跑。
4. 既有 37 条测试正好覆盖重构涉及的全部维度：`TestSnapshotWindowFilter` /
   `TestSnapshotExplicitWindow`（窗口口径）、`TestSnapshotExcludesAncientBucket`（远古桶）、
   `TestCreditDimensionsRateGrouping` / `TestCreditLegacyRateFallback`（积分归因 + 倍率回填）、
   `TestByKeyAttribution`、`TestUSDValuePricing`、`TestCacheHitRateAccumulation`。
5. 棘轮：`check-depth -Update` 输出 `+ maxFileLines 997 → 942`、`+ oversizeFuncs 4 → 3`

## 回滚

`git revert <this-commit>`；纯包内重构，无数据/配置/接口影响。累计状态文件与 WAL 格式未动。

## 主动未做

- 未顺手拆 `main`（228）、`Aggregate`（211）、`pick`（180）：一次只动一个目标，避免把两个口径
  同时搬动后无法归因。
- 未给 `usageAgg` 加并发保护：它只在 `SnapshotWindow` 内单 goroutine 使用，桶指针快照已由
  COW 保证一致时点（原注释保留）。
