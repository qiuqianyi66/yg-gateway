# 判定记录 — B2 实质①「剪枝前回填派生值」在本仓**不适用**（附证据）

- 日期：2026-10-18
- 分级：**L0**（只做判定与记录，无代码改动）
- 来源：`.harness/wiki/cc-switch-deep-read.md` §6 的「该改」①；`docs/PLAN-open-items-2026-10-17.md` 的 B2 实质

## 一、wiki 那条说的前提

> 剪枝/删除**前**先尽力回填派生值，失败只告警不阻断（dao/usage_rollup.rs:79-86）。
> 该改：cost tier 是 inference，**归档 7 天到期后永久失去纠成本的机会**。

前提是：**派生值（cost tier）只能从归档/明细里算**，所以删除明细前必须先把派生值写回。

## 二、本仓的实况（证据）

1. **推导值在摄入时就落了台账**（不是靠归档重算）：
   - `internal/server/chatrun.go:413` 与 `:488` —— 每次成功请求（带 credit）都调
     `Pool.NoteModelCost(acct.UID, bareModel, credit, total)`；
   - `internal/pool/state.go:214` `NoteModelCost` 更新该 (账号, 模型) 的成本台账并消解 unknown。
2. **归档只是副本**：`internal/reqlog/reqlog.go:79-80` 确实存了 `Credit` / `HasCredit`
   原文，但它是审计留痕，不是学习输入。
3. **谁会删数据**：
   - `internal/usage`：**没有**桶剪枝（`grep prune|剪枝|Retention` 只命中一处展示注释）；
   - `internal/reqlog`：`archiveWriter.prune()`（reqlog.go:507）按 `RetentionDays`（默认 7 天，
     reqlog.go:301）与体积删**归档文件**（`os.Remove`，:544）。

## 三、判定

**不适用，不实施。** 理由：本仓的 cost tier / EWMA / unknown 消解都在**请求路径上即时更新**，
归档被删不会「永久失去纠成本的机会」——下一次同模型调用会带来新样本并刷新台账。
真要按 wiki 那样做，得新增 `reqlog → pool` 的反向依赖（或回调注入口），
而它换来的收益为零，却增加了一处跨包耦合与一条需要维护的删除时机。

## 四、唯一残留的差异（记为 unknown，不是缺口）

`model_costs` 有 TTL（`modelCostTTL`），模型超过 TTL 没被调用 → 台账行消失、tier 回到 unknown；
而归档能留 7 天。也就是说**归档可能比台账「记得更久」**。但这不是「该回填」的场景：
用 7 天前的旧价格外推成本 tier，正是本项目四态纪律要避免的「把推断当观察」。
**结论：保持现状。** 若将来要做「成本历史趋势」类功能，那是新需求，需另立 PRD。

## 五、这一步为什么值得写下来

按 `.harness/LESSONS.md` L-15 与 AGENTS.md 的四态纪律：**「该做但没做」和「不该做」都要有证据**。
这条记录让下一个人不必重做一遍 recon，也避免把 wiki 的（针对另一个项目的）建议当成本仓的欠账。