# magpie 源码级深读（2026-10-25）

> 方法：读官方子系统文档（gateway-routing 288KB / providers-accounts 119KB）而非
> README 转述；关键句原文引用。目的：回答「直接用还是打磨」之后——**它哪里比我们强
> （学）、哪里比我们弱（守）、哪里我们根本没有（补）**。结论先行：两者是正交资产
> （它强在额度记账与路由排序策略，我们强在实测健康/成本治理）；建议采纳 3 项，
> 明确不抄 4 项，1 项 unknown。

## 一、它的路由栈（实测细节，非 README 口径）

选号 = 「路由组模式 × 会话粘性 × 故障转移」三层：

- **模式**：smart（默认，按 allowance 加权，有 **90% 用量带**——接近上限的账号不休眠、
  保留份额但排序靠后）/ order / rotate / usage（least-used，**1h 半衰期**）/ pace
  （按 allowance，公式未公开）/ weight（平滑加权轮询）/ free（vendor 声明的免费优先）。
- **粘性**：绑定键 = 会话 ID 或**首条消息摘要推断**；一天过期 / 512 会话重启 / 上次
  回复中断即解绑；「休息中或近耗尽的账号除外」——粘性让位于健康。
- **故障转移**：失败账号 `fallbackCooldown`（1 分钟）排队尾；重复失败**翻倍退避封顶
  10 分钟**；限流休到 retry-after（封顶 30 分钟）；额度耗尽休到 reset（封顶 8 天）。
- **让位语义（sink）**：还有额度却 429 的账号排到「自上次限流以来从未被限的所有人」
  之后——按耻辱度排序，重启丢失。
- **额度记账（它的王牌）**：查询式（厂商 API/Claude Code 的 /usage 响应告知），
  5h/7d 双窗建模，`quota-history.json` 存 **45 天 6000 点/窗**，有**周期边界检测**
  （deadline 移动/消失/**5 分钟内 deadline 不变而回充 >5% = 边界**），reset 时间从
  /usage 解析并以窗长约束消歧。

## 二、八维对比（我们 vs 它）

| 维度 | YG Gateway | magpie | 判定 |
|---|---|---|---|
| 额度窗口建模 | 签到权威值+最早到期批次 | 5h/7d 双窗+历史+边界检测 | **它强**（学边界检测思想） |
| 实测成本分层 | ✅ 每账号×模型实测 credit（四态） | ❌ 只有目录价/插件声明/用户自设 | **我们强**（对 CodeBuddy 是硬差异） |
| 健康跟踪 | EWMA 成功/延迟+熔断+half-open 自动试探 | ❌ 无成功率/延迟；手动 Try now | **我们强** |
| 近限处理 | credit_floor **硬拦**（悬崖） | 90% 带**软降权**（坡道） | **它强**（学） |
| 闲置/用量权重 | 线性闲置补偿（hours×率封顶） | usage 半衰期 1h（指数遗忘） | **它强**（半衰期更有原则） |
| 429 让位 | 软冷却（固定短冷却） | sink 耻辱排序+MaxRPM 队列 | 各有取舍 |
| 模型退役 | 11102 负缓存（BlockModelBackoff） | retired.json（410 一周/成功恢复） | 等价族，不抄 |
| 决策可审计 | pickReason（昨日刚落）+四态+unknown | ❌ | **我们独有** |

## 三、采纳清单（进 REQ-routing-improvements）

1. **90% 带软降权**（S）：`credit_floor` 之前加软带——余额低于 `floor×1.2` 的账号
   不再硬拦而是权重 ×0.5 降档；只有低于 floor 才硬拦。消除「floor 一触发容量规则
   就告急」的悬崖耦合，对养号语义（留余额给免费模型）零牺牲。
2. **usage 半衰期可选策略**（S）：`pool.weight_mode: "idle"|"halflife"`（默认 idle
   零回归）。半衰期 1h：`w *= 0.5^(hoursSinceUsed)`，替代线性闲置补偿——近期用过
   的号降权更快，久置号渐近满分，无封顶突变。
3. **周期边界检测 → opsagent 假设源**（M，backlog）：回充 >5% 且 deadline 不变 =
   一次非例行 refill（活动/手动）——登记 unknown 供对账。依赖 quota 历史留存，先
   记 BACKLOG。

## 四、明确不抄（各带理由）

- **查询式额度记账为主**：它面向「厂商有 usage API」的世界；CodeBuddy 的权威值在
  签到响应里，我们已有的插值口径（偏低方向安全）+实测分层对这个世界更强。
- **sink 耻辱排序**：内存态重启丢失，且我们的软冷却+EWMA 已覆盖「坏号让位」，
  再叠一层排序语义会让决策溯源的口径变复杂。
- **Bun 插件/中间件生态**：违背单二进制零依赖（go.mod 个位数）红线。
- **首条消息摘要做粘性键**：我们有 ADR-004 的内容派生键（可关），语义已覆盖。

## 五、Unknown（诚实登记）

- **pace 模式公式**：文档只说「按 allowance 加权」未给公式，需读
  `internal/agent/route.go` 源码才能确认与 smart 的差异——**unknown**，requires:
  一次源码阅读；BACKLOG A6 的 pace 实现以此为前置。

## 六、战略结论（回应「直接用 vs 打磨」）

- 两者正交：它做「agent 侧编排 + 多 provider」，我们做「CodeBuddy 深耕 + 账号池
  治理」。切换 = 扔掉养号/四态/治理全部差异资产，换一个我们定位外的生态。
- **最优关系是互补**：magpie 支持自定义 OpenAI 兼容端点——把 YG Gateway 的 /v1 填
  进去，它获得账号池与养号，我们零成本获得 agent 侧编排。README 可加一节「与 magpie
  配合」的做法（backlog 小项）。
- 它不完美（用户判断正确）：无健康跟踪、无实测成本、无决策审计——这三处正是打磨
  方向（其一我们昨天已落）。

## 来源

[gateway-routing.md](https://github.com/yetone/magpie/blob/main/docs/subsystems/gateway-routing.md) ·
[providers-accounts.md](https://github.com/yetone/magpie/blob/main/docs/subsystems/providers-accounts.md) ·
[仓库](https://github.com/yetone/magpie)
