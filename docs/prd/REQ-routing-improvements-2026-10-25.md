---
name: req-routing-improvements-2026-10-25
description: 选号路由吸收 magpie 深读成果——90% 带软降权 + usage 半衰期可选策略（含必要性分析）。
status: 已立项待执行
owner: maintainer
---

# REQ: 选号路由改进（软带 + 半衰期策略）

## 必要性

- **真实问题**（2026-10-25 magpie 源码级深读结论，docs/reference/magpie-deep-read）：
  1. **悬崖问题**：`credit_floor` 是硬过滤——余额一低于 floor，账号瞬间从「可选」
     变「不可选」。实测后果（pick.go 注释在案）：floor 拦掉健康号后兜底路径压力
     骤增，容量规则（opsagent capacity）随即告急——「保底」和「容量」两个好意
     互相放大。magpie 的 smart 模式用 **90% 用量带**（"接近上限的账号不休眠、
     保留份额但排序靠后"）证明软降权是更平滑的工程答案。
  2. **线性闲置补偿的形状问题**：现 `idleWeight = hours × perHour`（封顶）是线性
     ramp——用过 5 分钟和用过 50 分钟的号在 1 小时后权重几乎无差。magpie 的 usage
     模式用 **1h 半衰期**（"least used, with an usageHalfLife of 1h"）：近期使用
     的降权陡、久置渐近满分，指数遗忘比线性 ramp 更符合「刚被压过的号让一让」
     的养号语义。
- **不做会怎样**：静默低效——floor 悬崖在多号触底时把流量集中到少数号（与防惊群
  目标相悖）；线性补偿对「刚用过的号」让位不够快。
- **现状**：两者都有既有机制（floor 硬拦 / idle 线性），改进是**行为精化**非补缺。
- **更便宜的等效手段**：调大 floor 值只是移动悬崖位置；调 idleWeightPerHour 不改
  形状。均不解决「悬崖/形状」本身。

## 方案

### P1 软带（soft band）

- `credit_floor > 0` 时新增软带：`floor < credits ≤ floor×1.2` 的账号**不硬拦**，
  路由权重 ×0.5（通过 weightOf 之后的因子实现，与 EWMA 因子同挂法）。
- `credits ≤ floor` 仍硬拦（现状语义不变，保底底线不退）。
- config 无新键（软带倍率 1.2 先内定，有实测诉求再暴露）。
- 决策溯源：pickReason 加 `softband` 标记（读到带内降权时）。

### P2 半衰期策略（可选，默认关）

- `pool.weight_mode`：`"idle"`（默认，现状零回归）| `"halflife"`。
- halflife 模式下闲置补偿项替换为 `maxBoost × 0.5^(hoursSinceUsed)`，从未使用给
  满分；`idleWeightPerHour/Max` 键在该模式下不参与。
- 与软带、EWMA、快过期因子正交叠乘（都是乘性因子，无耦合）。

## Given-When-Then

- When 账号 credits=180、floor=150（带内），Then 该账号仍可被选中，权重为带外的
  0.5 倍，pickReason 含 softband；credits=120（≤floor）时仍被硬拦（现状）。
- When `weight_mode=halflife` 且账号 1 小时前用过，Then 其闲置补偿 = 满分×0.5；
  4 小时前用过 = 满分×0.0625；从未用过 = 满分。
- When `weight_mode` 留空/="idle"，Then 行为与现状逐位一致（零回归锚定测试）。
- When 软带号被选中后余额跌破 floor，Then 下轮 pick 硬拦（软带不是逃逸口）。

## 验证与回滚

- 定向测试：带内/带外/硬拦三态权重比、halflife 数值表、idle 模式零回归；
  全量 pre-commit；真机观察一轮（opsagent audit 视图可见 softband 分布）。
- 回滚：单提交 revert；P2 本身默认关，回滚面更小。
