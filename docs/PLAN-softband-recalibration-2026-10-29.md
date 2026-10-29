# PLAN · 软带带宽校准（softband recalibration，2026-10-29）

> 分支 `feat/softband-recalibration`。只出方案，不实施；决策在用户。
> 依据：V3-3 真机回填（b5ffee2）+ 代码链路全读（softband.go / pick.go / state.go / 测试锚定）。

## 一、问题（真机证据，derivation）

`softBandTypicalTokens = 20_000` 假设被真机 reqlog 双峰分布推翻：

| 段 | 行数 | 单次 token | 单次扣费 |
|---|---|---|---|
| 聊天 | 383 | p50 = 762 | ~0.02-0.2 credit |
| 评测（deepseek-harness） | 215+3 | 200k-500k | ~4.4 credit |

按现状公式 `带宽 = floor + 2×median(CostPer1k)/1000×20000`：
评测一次（4.4 credit）≈ 20k 假设带宽（~0.3 credit）的 **15 倍**——号在带内接一次评测
就足以打穿 floor 再触发硬冷却，软带失效。

**修正认知**：V3-3 回填里写的「按流量类分口径」经代码复核后不成立——
双峰下 P50 恒落在聊天侧（383/596 ≈ 64% 样本），P50 对评测不敏感；
能反映「该号真实风险」的稳健分位是 **P90**（落在评测段，且带宽被 cap 在 floor×1.5）。

## 二、方案（推荐：实测单次扣费 P90）

**核心**：带宽定义从「单价 × 假设 token 数」改为「该号近期实测单次扣费的 P90 × N=2」，
P90 本身就是「典型中的最重请求」，双峰下唯一稳健。

### 改动面（S 级，纯 pool 内部）

1. `entry` 增运行时记账 `recentCharges []float64`（最近 ≤32 条收费请求的单次 credit，
   credit>0 才记；**不持久化**——state.json 大小不变，重启后 warmup 窗口空 → 回退）。
2. `NoteModelCost`（state.go:214，写锁内）顺带写入 `recentCharges`——零新增请求、零新锁。
3. `softBandUpper`（softband.go:34）：
   - `len(recentCharges) > 0` → `upper = floor + 2×P90(recentCharges)`（P90 用排序取，
     32 条排序开销可忽略，且只对近底线号执行——既有预筛已保证）。
   - 空 → 保留现有 20k 公式（迁移期回退，行为不变）。
   - cap `floor×1.5` 与预筛 `floor×1.5` 不变。
4. 常量 `softBandTypicalTokens` 保留（仅回退路径用），语义注释更新。

### 效果推演（floor=100）

- 纯聊天号：P90 ≈ 0.2 → upper ≈ 100.4，与 floor×1.2=120 取 max → 120（回退同值，聊天场景零回归）。
- 评测混合号：P90 ≈ 4.4 → upper = 108.8，cap 150 内生效——进带号能扛 2 次评测，防打穿。
- 大额台账 cap 语义不变（500 → 150，测试锚定不动）。

### 不做的变体（拒绝理由）

- **按流量类（UA/kind）分流**：把选号逻辑与流量来源耦合，破坏单一事实来源，且 `kind`
  chat/probe/farming 是占位词表（REQ-reqlog-kind），不支持此语义；拒绝。
- **带宽取 P50**：双峰下 P50 恒在聊天侧，评测风险不可见；拒绝。
- **持久化 recentCharges**：state.json 体积与迁移成本 > 收益（重启后 32 条很快回温）；拒绝。

## 三、验收（GWT）

- When 某号 recentCharges = [0.1,0.2,4.4]（3 条，floor=100），Then `softBandUpper` = 100+2×4.4=108.8，
  且 credits=108 ∈ 带内权重 ×0.5；credits=109 ∈ 带外。
- When recentCharges 为空（重启后），Then `softBandUpper` 与现状逐位一致（20k 公式 + floor×1.2 兜底）。
- When 大额扣费导致带宽 > floor×1.5，Then 仍 cap 到 floor×1.5（现有 cap 测试不动）。
- When credit=0 免费请求 / 失败请求，Then 不入 recentCharges（unknown 不当 0 记，四态纪律）。
- 全量 pre-commit + softband_recent_test.go 现有断言全绿（行为兼容）。

## 四、回滚

单提交 revert；`recentCharges` 不持久化，无迁移残留；回退路径即现状公式。

## 五、待决策（用户拍板）

1. 方案 A（本文推荐）：实测单次扣费 P90，N=2 语义保留。
2. 方案 B：只把 20k 常量提为 config 可调（`pool.softband_typical_tokens`），默认 20k——
   最小改动但双峰下仍是拍脑袋，不推荐。
3. 保持现状：软带 48h 实弹 0 命中（无号进带），低风险窗口内可不改——但假设已证伪，
   号真触底时失效。
