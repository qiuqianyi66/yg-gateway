# 变更：选号/成本账本时钟一致性 + WAL 流式回放 + 生产死代码清理

- 日期：2026-11-21
- 分诊：L2（两处行为修正 + 一处 IO 形态改造；无外部契约变化）
- 前置：`4cd8823`

## 为什么

本轮以只读审计与 staticcheck v0.8.2 为依据，挑出三处「注释承诺与实现不一致」或
「读者无收益」的点，全部先跑基线再改：

1. `pickWeighted` 注释承诺「与 pick 的权重计算共用同一时间口径」，实现却在函数内
   重新 `p.now()`。同一次 Pick 的截断排序与抽签落在不同时刻，闲置补偿权重可能
   不可比（生产是 µs 级，测试注入冻结时钟时会被放大）。`2026-11-17-pick-alloc-merge`
   的「没有改的优化」清单已把它标为**行为变更，需单独评估**，本轮补上。
2. `NoteModelCost` 的 `LastSeen` 直接用 `time.Now()`，而读侧 `modelCostOf` 的 TTL
   用调用方传入的 `now`。测试注入时钟后台账永远「刚观测过」，TTL 行为不可复现。
3. `replayWAL` 用 `os.ReadFile + strings.Split` 全量载入；WAL 是崩溃恢复侧车，
   记录上限默认 20000，文件越大启动峰值内存越高。格式不变即可改流式。

另清理 staticcheck 生产侧诊断：`growth` 两个跨包拆分后遗留的重复常量
（真实常量在 `internal/upstream/growth_stub.go`）、`pool.degradeReason`（从未写入，
`/status` 实际用 `CoolKind="degrade"` 透出）、`server.formatInt`（无调用）、
`global_register` 二次状态查询的无用 `needsRegion` 赋值；`persist.go` 两处
同字段结构体字面量改类型转换。

## 改动

- `internal/pool/pick.go`
  - `pickWeighted(..., now time.Time)`：由 `selectFromCandidates` 传入同一个 `now`，
    删除函数内 `now := p.now()`；权重切片改固定 `[5]int64`（eligible 恒 ≤ top5）。
  - 行为边界：只改变「同一次 Pick 内的时间基准」，不改变权重公式、随机源或截断。
- `internal/pool/state.go`：`NoteModelCost` 的观测时刻改用 `p.now()`，TTL 读写同源。
- `internal/pool/pool.go`：`now()` 的适用范围注释同步（pick 系 + 成本账本 TTL）。
- `internal/usage/wal.go`：`replayWAL` 改 `bufio.Reader` 逐行读取；抽出
  `replayWALLine`（坏行/空行跳过、delete 先于同批 upsert、monthly 取大的语义不变）；
  删除从未写入/读取的 `walFileVersion`，注释明确 ADR-016 **不承诺版本头**。
- 死代码：`growth.schoolBase`、`growth.mpPlatform`、`pool.degradeReason`、
  `server.formatInt`；`upstream.GlobalCompleteRegistration` 第二次 `needsRegion` 改 `_`。
- 测试：`TestPickWeightedUsesCallerNow`（把 `p.now()` 故意拨到不同时刻，断言
  total 只可能来自传入 `now`）、`TestModelCostTTLUsesInjectableClock`（冻结时钟下
  TTL 内有效/超期失效）、`TestWALReplayStreamsTruncatedTail`（完整行照常、末尾
  半截记录跳过）。

## 验证

- `go test ./... -count=1`：全绿（32 包，含 panel 59s / scheduler 20s）。
- `go vet ./...`：0。
- `gofmt -l ./cmd ./internal`：无输出。
- `scripts/check-depth.ps1 -Detail`：`✅ 深度指标未回退`；最大文件 801 行、
  超长函数 0、前端超 1000 行文件 1（app.js 未动）。
- `go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./internal/...`：
  **生产代码诊断 0**；剩余 11 条全部在测试文件（含 `gosafe_test` 故意 nil map
  写入、`transition_test` 刻意短路，属误报/测试自身）。
- `BenchmarkPick46Accounts`（i7-14650HX，count=5）：约 4.54–4.78 µs/op、
  2768 B/op、4 allocs/op，与改前（4.70–4.93 µs/op）同量级；本轮不声称性能提升，
  固定数组只是去掉一个不确定的分配点。

## 回滚

`git revert <this-commit>`。三处测试钉住行为：
- 回退 `pickWeighted` 的 `now` 参数 → `TestPickWeightedUsesCallerNow` FAIL；
- 回退 `LastSeen` 到 `time.Now()` → `TestModelCostTTLUsesInjectableClock` FAIL；
- 回退 WAL 流式读取但漏掉坏行语义 → `TestWALReplayStreamsTruncatedTail` FAIL。
