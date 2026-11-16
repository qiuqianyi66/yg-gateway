# 评审：窗口聚合切深模块

- 日期：2026-11-16
- 评审对象：`internal/usage/snapshot_window.go`（新增）、`internal/usage/usage.go`（删 246 行）、
  `.harness/depth-baseline.json`（棘轮收紧）
- 分诊：L1
- **结论强度：自评审 + 全仓测试全绿 + 牙齿验证。独立复审未执行**

## 六维

### 1. 正确性 🟢

| 判据 | 证据 |
|---|---|
| 口径未变 | 37 条既有测试全绿；覆盖窗口过滤 / 显式区间 / 远古桶 / 积分三归因 / 倍率回填 / Key 归因 / 美元折算 / 缓存率 |
| 判据有牙齿 | 故意把 `total.add(b, usd)` 改成 `total.add(b, 0)` → `TestUSDValuePricing` 红；改回绿 |
| 签名未变 | `SnapshotWindow(w, nicks, keyNames, currentRate, pricing)` 与 `Snapshot` / `SnapshotWithRates` 完全一致 |
| 边界保留 | `r == nil` 短路、`ancientDayScope` 只进 totals、脏 scope 跳过、`since` 不受窗口影响、`explicit` 才回显区间 |

### 2. 性能 🟢

搬运未改变访问模式：锁内仍只拷桶指针（R-205 优化保留原文），`usd` 仍每桶只算一次，
`rateCache` 仍按 realm+model 缓存。循环内部从 11 次 map 查找 + 11 段 nil 判断，变成
11 次 `bumpAcc` 调用（内联后等价），无额外分配——`newUsageAgg` 预先把 map 全部初始化，
省掉了运行期的惰性创建分支（严格说不增反降）。

### 3. 安全 🟢

不涉及鉴权、入站/出站、日志脱敏。`nicks` / `keyNames` 仍只做展示名映射、不含明文（沿用原约束）。
无对外暴露面变化（JSON 字段与形状不变）。

### 4. 一致性 🟢

- 注释全部随代码搬迁并保留原文（口径解释、R-205、ADR-011、ADR-001 等引用未丢）。
- 新的 `consume` / `consumeCredit` / `assemble` 各自只做一件事，命名与包内既有风格
  （`aggAcc.add` / `creditAcc.add` / `keyed`）同构。
- `bumpAcc` 抽掉了 11 处手抄样板——与原实现逐段对照过（累加字段与顺序一致）。

### 5. 可维护性 🟢

- 维度扩展从「改 11 个局部变量 + 11 段 nil 样板 + 巨循环」变成「加一个 map 字段 + 一行 bumpAcc」。
- 三段拆分后，改窗口口径只碰 `SnapshotWindow` 的过滤段；改维度只碰 `consume`；改展示只碰 `assemble`。
- `usage.go` 从 997 降到 764 非空行，全仓最大文件变成 `main.go`（942），棘轮同步收紧。

### 6. 架构合规 🟢

依赖方向未变（usage 仍不依赖上层包）；未引入新依赖；R3/R8/R10/R14 无关；`gofmt`/`vet` 干净；
棘轮只收紧不放宽（`maxFileLines 997→942`、`oversizeFuncs 4→3`）。

## Findings

| 级别 | 内容 | 处置 |
|---|---|---|
| 🟢 | `usageAgg` 无并发保护，依赖「只在 SnapshotWindow 内单 goroutine 使用」的隐含契约 | 已在结构体注释写明该契约与原 COW 依据；当前无第二个调用方，未加锁（加锁反而暗示可并发） |
| 🟢 | `assemble` 参数含 7 个（w/explicit/matched/since/path/nicks/keyNames） | 全部是只读输入、无状态耦合；再抽结构体属过度设计，保持现状 |
| 🔵 | 未做新旧实现的**双跑对照**（只靠既有单测锁定） | 单测 83% 语句覆盖 + 牙齿验证足以证明口径；双跑需要构造临时 worktree，收益低于成本，登记备查 |

🔴 零，🟡 零。

## 缺口（显式声明）

**独立复审未执行。** 本轮为「纯搬运 + 结构体化」的 L1 重构，判据由 37 条既有测试 + 一次
主动注入偏差的牙齿验证承担。若要升强度，复审者应重点核对：`consumeCredit` 的三维度
键构造（`model+"\x00"+rate`、空 Key 分组）与原实现逐字一致。

证据可复跑：
`go test ./internal/usage -count=1` / `go test ./... -cover` /
`powershell -NoProfile -File scripts/check-depth.ps1`
