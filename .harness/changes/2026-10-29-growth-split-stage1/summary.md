# 2026-10-29-growth-split-stage1 — growth 域行为级测试基线（M3）

> 状态：已实现并提交。解锁 REQ-growth-package-split 的①（行为级测试基线补齐）。
> 验收：新增测试全绿，**不做搬移**（纯测试补强，生产代码零改动）。

## 现状核对（2026-10-29，观测，非转述）

REQ 立项（2026-10-24）时 growth 域「部分靠 coverage_* 间接覆盖」——实测发现
10-25~10-28 期间的 coverage_parse_test.go / coverage_sequences_test.go（各 ~100k）
已补上**直接断言**：promoClock/promoActive（parse:111/141）、blackcat need（parse:2316）、
ListTasks/MP（seq:725）、AcceptTasks bodies（seq:791）、ClaimReward+GrowthTasks（seq:916）、
GrowthStreak full（seq:960）、School vouchers（seq:1100）、School season（seq:1199）、
ClaimTrial（seq:2329）、TrialAlreadyErr（seq:2393）。加上独立测试：
streak ×4 / travel ×7 / school ×3 / taskmetrics ×2 / blackcat ×2 / tasks ×3 /
checkin 重试 ×4+already / UserResource ×2 / enterprise ×4 / meter paths ×2 /
CreditPackages（sse_test:906）——REQ 名单（school/travel/blackcat/streak/tasks/
taskmetrics/billing/trial/promo）逐项有直接断言。

**唯一缺口（grep 实证）**：`applyModelPromotions` 的挂载语义（priority 择优 /
badge-only 不挂 factor / 目录外模型不挂 / disabled 跳过）无直接测试。

## 改了什么

- 新增 `internal/upstream/promo_apply_test.go`：
  - `TestApplyModelPromotionsPriorityAndMounting`：同模型多条命中取 priority 最高；
    discount 命中全挂（Factor/Credits/Badge/Hover）；badge-only 只挂 Label+Note、
    factor 留 nil；promo 声明目录外模型不挂；disabled 与 enabled 并存只取 enabled。
  - `TestApplyModelPromotionsEmptyNoop`：空列表/空目录无操作。
- 无生产代码改动（纯测试文件）。

## 怎么验证的

- `go test ./internal/upstream -run TestApplyModelPromotions -v` 全绿；
- gofmt 干净；全量 pre-commit 11/11。

## 怎么回滚

删除本测试文件即可（无生产代码，无回滚面）。若后续拆包（阶段②③），
本测试随 promo 逻辑迁入 `internal/growth` 或保留在 upstream，迁移时同步。

## 对解锁动作②的贡献

① 完成后 growth 域行为基线 = coverage 直接断言 + 本文件。ADR-017 定接口形态
（候选 A/B）时，R8 金样 + 本基线作为搬移安全网。
