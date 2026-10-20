# REQ：usage 桶数硬上限真正生效（修复 Rollup 不收敛的无界增长路径）

> **状态：DRAFT v0（2026-10-20）。L1（单文件内部修复，不新增端点 / 规则 / 依赖方向）。**
> **关联**：`docs/code-review/2026-10-20-four-dimensions.md` 性能待改 [P1]；`internal/usage/usage.go`
> **决策记录**：`docs/adr/ADR-011-usage-bucket-cap.md`

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**

`internal/usage` 的 `maxBuckets = 400_000` 是自 v1 起就写死的桶数硬上限，注释说「超过时立即触发一次折叠，避免异常流量把内存/文件撑爆」。但 `Rollup` **只把 >90 天的小时桶折成日桶**（日桶永久保留）。稳态小时桶量 ≈ 活跃组合数（realm×uid×model×rate×key）× 90天×24小时，对一个繁忙网关**轻易超过 40 万**。于是：

- `Start()` 每 30s 发现 `len(buckets) > maxBuckets` 就调 `Rollup`；
- 但 `Rollup` 只折 >90 天的桶，而 90 天窗口内的小时桶一个都折不掉；
- 桶数**压不回上限**，每新一小时又加一批新桶 → 相对上限**无界增长**；
- `usage.json` 与常驻内存随运行时间单调膨胀，最终 OOM 或超大文件拖慢 `Snapshot` 聚合。

这是 `docs/code-review/2026-10-20-four-dimensions.md` 标出的 **[P1]**。

**Q2 不做会怎样？** 高流量部署下 usage 台账内存与磁盘占用随运行时间无界增长，7×24 常驻服务稳定性受损（正是本仓最看重的维度）。

**Q3 现状是不是已经变了？** 没变。`maxBuckets` 自 v1 起即此行为，审查前一直没人在真实高负载下撞到上限，所以没暴露。

**Q4 有没有更便宜的等效手段？** 没有——这是容量保护的**正确性**修复，不是新功能；上限本身要不要调是可以另议的运营问题，不在本期。

**结论：做，单文件最小改动** —— 只让 `Rollup` 在超上限时真的把桶数压回上限，不动 wire、不动出站（R6）、不新增依赖方向（R6 反向）、不新增端点。

---

## 二、方案要点

1. **容量保护（核心修复）**：`Rollup` 在 `len(buckets) > maxBuckets` 时，把小时保留窗口从 `hourlyKeep`（90d）**逐次减半**（90d→45d→…→`minHourlyKeep` 24h 触底），每轮把窗口内小时桶折成日桶，直到桶数压到 `0.85×maxBuckets`（target）或窗口触底；触底仍超则启动**日桶丢弃安全阀**：把最老日桶聚合进一个合成「远古」日桶 `d:0001-01-01`（维度全空），直到桶数 ≤ target。**总量（totals）经累加保留，不丢**；只丢极老历史的时分辨率与按维度下钻。
2. **正常负载零回归**：桶数 ≤ maxBuckets 时，`Rollup` 行为**完全不变**（只折 >90d 小时桶）。压力路径仅在超上限时触发，正常部署零开销。
3. **参数（包内常量，ADR-011 引用）**：`maxBuckets = 400_000`；`minHourlyKeep = 24h`（最少保留最近 24h 小时粒度）；target = `0.85×maxBuckets`。
4. **红线纪律**：错误不静默（R10）——折叠 / 丢弃都打 WARN 点明数量；goroutine 沿用 `Start()` 的 ticker（R9 已满足）；不改出站（R6）；无新依赖方向（`scripts/check-boundaries.ps1` 不变）。

---

## 三、四态 / 数据正确性口径

- 折叠（小时→日）与丢弃安全阀（日→远古）都是**累加**合并，不丢总量，不双计（幂等）；
- 远古桶在 `Snapshot` 中**只进 totals**，不进 `by_realm/by_account/by_model/by_key/时序`——避免 Years-ago 的聚合污染图表最左端或生成空维度行；
- 任何桶的 `usage` 缺失语义不变（本 REQ 不碰 Add 的四态处理）。

---

## 四、验收（Given-When-Then）

- **GIVEN** 桶数 ≤ maxBuckets 且含 >90d 的日桶与 <90d 的小时桶 **WHEN** `Rollup` **THEN** 仅 >90d 小时桶被折（行为不变），窗口内小时桶与日桶数量不减少。
- **GIVEN** 桶数 > maxBuckets 且全为 90d 窗口内小时桶 **WHEN** `Rollup` **THEN** 桶数降至 ≤ target（0.85×maxBuckets），且**幂等**（再调一次桶数与总量不变）。
- **GIVEN** 触发日桶丢弃安全阀（触底仍超）**WHEN** `Rollup` **THEN** 桶数 ≤ target，且**总量（各维度之和）不变**（远古桶聚合保总量），`d:0001-01-01` 桶存在且仅 1 个。
- **GIVEN** 注入一个远古桶（`d:0001-01-01`，维度全空，PT=999）**WHEN** `SnapshotWindow`（全历史）**THEN** `Totals.PromptTokens` 含 999，但 `ByAccount/ByModel/ByKey/时序` 不含该桶。
- **GIVEN** 改动后 **WHEN** 跑 `scripts/check-boundaries.ps1` 与 `go test ./internal/usage` **THEN** 依赖方向无新增违规、用量包测试全绿。

---

## 五、范围与明确不做

- **做**：`Rollup` 容量保护重构 + 折叠/丢弃辅助方法 + `Snapshot` 排除远古桶 + 单测。
- **不做**：动态调参 / 可配置上限（保持单二进制零依赖、配置面不膨胀）；不改 `hourlyKeep` 默认；不做按账号/模型配额（那是 P1-A 已有的 per-key 月配额，不相关）；不引入新端点或新规则。
- 与 `REQ-harness-engineering` 的边界：纯内部稳定性修复，不新增任何治理检项。
