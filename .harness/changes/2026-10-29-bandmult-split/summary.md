# 2026-10-29-bandmult-split — routing bandMult 分口径（M2，设计落档）

> 状态：**设计定稿待实现**。本目录只落设计（决策 + 现状实读 + 方案 + 验收），
> 实现与测试由下个会话按本文件执行。四态纪律：现状结论全部来自代码实读（observation），
> 分口径参数是 derivation（48h 真机双峰样本）。

## 决策（PLAN §四 选项 A，用户 2026-10-29 授权推荐路线）

bandMult 分口径：聊天类保持默认（现状公式）；评测/大请求类走该号近期实测扣费
（环 P90）。不选 B（单一 P50 会撑大聊天带宽）、不选 C（20k 对评测误判余量偏紧）。

## 现状实读结论（observation，2026-10-29）

- `internal/pool/softband.go`：软带上界 = `floor + 2×P90(entry.recentCharges)`（N=2 语义），
  cap `floor×1.5`；冷启动无环回退 `2×median(每1k扣费)/1000×20k`；`softBandFactor=0.5`。
- **单环** `entry.recentCharges []float64`（entry.go:242，容量 32，state.go:256-259 环形收缩），
  `NoteModelCost` 记账时**所有收费扣费进同一环**——评测大额（~4.4 credit/次）拉高 P90，
  把聊天流量的软带上界也抬高（误判余量从「2 次聊天」变「2 次评测」）。
- 判定链：`routingWeightOf`（pick.go:529）→ `softBandFactorOf(e, now)` → `softBandUpper(e, now)`；
  调用链 `Pick*` 族（pick.go:17-45）只有 model/realm/group 维度，**无请求规模/UA 信号**。
- 记账端 `NoteModelCost(uid, model, credit, tokens)`（state.go:214）**有 tokens 量级**。

## 方案（选项 A 落地）

1. **流量类**：`class = chat | bench`。判定信号 = 请求 tokens 量级，阈值 **50_000**
   （真机双峰：聊天 ≤5k、评测 ≥200k，5k-200k 间无样本，取深谷；derivation）。
2. **扣费环分裂**：`entry` 增 `recentBenchCharges []float64`（同容量 32、同环形收缩逻辑）；
   `NoteModelCost` 按 `tokens > 50_000` 分流入 bench 环 / 聊天环（原 `recentCharges` 保留为
   聊天环语义）。免费（credit=0）仍不入环（现状语义）。
3. **判定按类取环**：`softBandUpper(e, now, class)` / `softBandFactorOf(e, now, class)`——
   按 class 查对应环 P90，无环回退 20k 中位数公式（现状冷启动路径）。cap 语义不变。
4. **调用链贯穿**：`routingWeightOf(..., class)` ← `pickWeighted(..., class)` ←
   `Pick*` 族（新增 `class` 参数，默认 `chat` 保持既有签名调用点零改动）；
   server 层（chatrun/anthropic/responses handler）解析请求后按 prompt 规模估算判类；
   panel / scheduler 无请求上下文 → 默认 chat。
5. **默认语义**：生产升级后聊天流量带宽 = 纯聊天扣费口径（评测大额不再污染），
   这是分口径目的本身（行为精化，非回归）。

## 验收（下个会话执行）

- 三态权重定向测试：带内（chat 环 P90）/ 带外 / 硬拦；bench 环只收 >50k 扣费；
  评测大额只抬 bench 带宽、聊天带宽不变（新旧对比断言）。
- 既有 `softband_recent_test.go` 断言同步新语义（原环含评测大额的用例改入 bench 环）。
- 零回归：默认 class=chat 时 Pick() 无参调用行为不变（面板/调度路径）。
- pre-commit 11/11；go test 全绿；gofmt 干净；变更追踪三件套（本目录 + review.md 如 L2）。

## 已知 unknown（不改动）

- 50k 阈值真机分布：仅 48h 双峰样本（derivation），阈值样本外待真机复核；
- count_tokens 精度 / 评测带宽回填：维持 unknown（用户约束不发起付费验证）。
