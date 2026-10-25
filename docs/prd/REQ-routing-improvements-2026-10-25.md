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

## 方案（2026-10-25 二轮源码研读后修正——原 ×0.5/替换 idle 两处为拍脑袋，已按 magpie 实现实锤改写；依据见 reference/magpie-deep-read §五）

### P1 软带（soft band）

- **分层思想（对齐 magpie tier 实现）**：`credit_floor > 0` 时账号分三档——
  `credits > floor×bandMult` 正常 / `floor < credits ≤ floor×bandMult` 近底线档 /
  `credits ≤ floor` 硬拦（现状语义不变）。magpie 的「软」是字典序分层（带内排
  fine 之后、failover 才轮到）；YG 是加权抽签体系，**正确翻译是带内权重降档**
  （×0.5）而非排序——加权体系里所有候选本就都会被抽到，概率降低即「排后」。
- **bandMult 参数不再拍 1.2，改为「按典型请求扣费定义」**：带内含义 =「还敢接
  N 次收费请求」。N 取 2（一次误判余量）；扣费量级从 modelCost 实测台账取该账号
  近期 P50（无观测时回退全池 P50，再回退 bandMult=1.2 常量）。真机分布出来后
  复核（这是执行期任务，不是研究期）。
- 决策溯源：pickReason 加 `softband` 标记。

### P2 近期用量衰减因子（修正：不是替换 idle 补偿）

- **magpie 实锤**：半衰期作用于**token 记账维度**（`exp2(-Δt/1h)`，served 时累加
  衰减值），作为策略层内的**次级键**，不乘权重。原方案「替换线性 idle 补偿」
  是误读，废弃。
- **YG 翻译**：每账号维护「衰减 token 账」——`RecordTokenUsage` 已有累计值，补
  时间戳：`decayed = (decayed + Δtokens) × 2^(-Δt/1h)`（served 时先衰减再累加，
  读取时衰减到当前）。选号时作为**乘性因子**参与 routingWeightOf：
  `w *= 2^(-decayed/scaleTokens)`，scaleTokens 默认 200k（≈一次重度 agent 会话）。
- **1h 半衰期对 CodeBuddy 节奏的适配论证**：magpie 的 1h 面向 Claude 5h 窗
  （窗长的 1/5）；CodeBuddy 的节拍是签到 09/21（两次 12h）+ 硬冷却到次日 04:00——
  「让刚用过的号歇会儿」的自然尺度是**小时级**（两次签到之间），1h 半衰期使
  2 次签到间隔（12h）后历史用量仅剩 1/4096 ≈ 归零，恰好等于「新一轮签到周期
  重新开始」的语义。**结论：1h 合适，无需改**；config 留 `halflife` 键以便真机调。
- 与软带、EWMA、快过期因子正交叠乘（都是乘性因子）。默认关
  （`pool.recent_usage_factor: false`）。

## Given-When-Then（随方案修正更新）

- When 账号 credits 在 (floor, floor×bandMult] 带内，Then 仍可被选中、权重×0.5、
  pickReason 含 softband；credits ≤ floor 仍硬拦（现状）。
- When 衰减因子开启且账号刚服务过 100k token，Then 其权重因子 = 2^(-0.5)（1h
  半衰期、scale=200k）；12h 后因子 ≈ 1（签到周期归零语义）。
- When 两个因子全关（默认），Then 行为与现状逐位一致（零回归锚定测试）。
- When 软带号被选中后余额跌破 floor，Then 下轮 pick 硬拦（软带不是逃逸口）。

## 验证与回滚

- 定向测试：带内/带外/硬拦三态权重比、halflife 数值表、idle 模式零回归；
  全量 pre-commit；真机观察一轮（opsagent audit 视图可见 softband 分布）。
- 回滚：单提交 revert；P2 本身默认关，回滚面更小。
