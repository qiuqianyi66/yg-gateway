# usage-bucket-cap 变更记录

> 目录：`.harness/changes/2026-10-20-usage-bucket-cap/`。
> 需求与决策真身放顶层：`docs/prd/REQ-usage-bucket-cap-2026-10-20.md`、`docs/adr/ADR-011-usage-bucket-cap.md`。
> **无数据库迁移 / 无 wire 变化**：单文件内部改动，`usage.json` 格式不变（远古桶是普通日桶，旧程序读它只当一条极老日桶）。

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | usage-bucket-cap（用量桶数容量保护） |
| 分支 | 当前分支（基于 main，领先 4 个本地提交） |
| 需求来源 | `docs/prd/REQ-usage-bucket-cap-2026-10-20.md`（四问题必要性） |
| 决策 | `docs/adr/ADR-011-usage-bucket-cap.md`（已采纳） |
| 类型 | fix + test |
| 涉及范围 | `internal/usage`（usage.go 拆出 cap.go + 单测 usage_cap_test.go） |

## 二、需求与方案

### 真问题（P1）

`maxBuckets = 400_000` 是 v1 起写死的桶数硬上限，但 `Rollup` 只折叠 **>90 天** 的小时桶
（日桶永久保留）。稳态小时桶量 = 活跃组合 × 90天 × 24小时，繁忙网关易超 40 万。触发上限后
`Rollup` 折不掉窗口内桶，桶数压不回上限 → 每新一小时又加新桶 → **相对上限无界增长**。这是
`docs/code-review/2026-10-20-four-dimensions.md` 标出的 [P1]。

### 验收标准（Given-When-Then，见 REQ §四）

- **正常（≤ 上限）**：Given 桶数 ≤ maxBuckets / When Rollup / Then 只折 >90d 小时桶，窗口内小时桶保留（行为不变）
- **超上限（窗口内小时桶）**：Given 桶数 > maxBuckets / When Rollup / Then 桶数降至 ≤ 0.85×上限，幂等
- **触底仍超（日桶过多）**：Given 折到 24h 仍超 / When Rollup / Then 最老日桶合入合成远古桶直至 ≤ 上限，总量不丢，远古桶恰好 1 个
- **Snapshot 隔离**：Given 注入远古桶 / When Snapshot / Then totals 含远古量，`by_*` / 时序 / since 不含
- **跨重启**：Given 产生远古桶并落盘 / When 新实例 load / Then 远古桶恢复且只进 totals

### 技术方案（ADR-011）

1. **正常（桶数 ≤ 上限）**：行为不变，只折 >90d 小时桶。
2. **超上限（桶数 > 上限）**：小时保留窗口从 `hourlyKeep`（90d）**逐次减半**（90d→45d→…→
   `minHourlyKeep`=24h 触底），每轮把窗口外小时桶折成日桶，直到桶数压到 `target =
   0.85×maxBuckets` 或窗口触底；触底仍超则把最老日桶聚合进合成远古桶 `d:0001-01-01`
   （维度全空，只进 totals）。
3. **Snapshot**：远古桶只进 `Totals`，不进 `by_realm/by_account/by_model/by_key` 与逐日时序，
   也不污染「数据起点」`since`。
4. **参数**：`maxBuckets/ minHourlyKeep / rollupTargetRatio` 为包内常量，注释引用 ADR-011；
   压力路径子函数 `rollupWithLimits(..., target, minKeep)` 参数化，便于单测用极小值直接验证。

### 文件体量（深度棘轮）

为把 `usage.go` 控制在千行以内（棘轮要求 oversizeFiles 只许降不许涨），容量保护逻辑（Rollup
及子流程 + `ancientDayScope` 常量）拆到同包 `cap.go`。`usage.go` 非空行 1066 → 911，
`cap.go` 171 行，均 < 1000。

## 三、影响分析

- **影响面**：`internal/usage/usage.go`（Rollup 重构）、新增 `internal/usage/cap.go`、新增
  `internal/usage/usage_cap_test.go`
- **承重墙**：❌ 不触及选号 / 冷却 / 熔断 / 凭证 / 计费链路；只改用量桶的折叠与聚合口径
- **契约变化**：无新端点、无报文形态变化；`usage.json` 字段不变（远古桶是普通日桶）
- **回滚路径**：单文件改动，`git revert` 即回「上限形同虚设」旧行为（旧行为不崩，只是无界增长）

## 四、自检清单

- [x] `go build ./...` + `go vet ./...` 通过
- [x] `go test ./internal/usage` 全绿（含 5 个新增 cap 验收用例 + 既有 rollup/snapshot 回归）
- [x] 深度棘轮：`oversizeFiles` 未回退（usage.go 911 / cap.go 171 非空行，均 < 1000）
- [x] `pre-commit.ps1` 全 10 项通过（gofmt / R10 / 依赖方向 / 深度 / 治理体检 / 预算）
- [x] 正常负载零回归：桶数 ≤ 上限时压力路径完全不触达，既有测试 `TestRollupIdempotent` 等仍 PASS

## 五、Learning

根因：容量上限「写了但折不掉窗口内桶」= 形同虚设。这类 bug 的特征是**常量有明确意图
（"硬上限"）但执行路径对触发条件无能为力**——审查时用「这个上限真的能在超了之后被拉回吗」
一问就漏出来了。修复的代价是接受「压力期部分历史从小时级降为日级」的取舍，用有界性换
粒度，正常负载完全无感。
