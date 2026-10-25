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

## 五、Unknown（诚实登记）→ **2026-10-25 二轮源码研读已消解**

读了 `internal/routes/strategies.go`（Smart/Pace/LeastUsed/Weighted 全部实现）+
`internal/gateway/routing.go`（Seat 组装），三个此前的 unknown/拍脑袋点全部有实锤：

### ① pace 公式（源码级）

**`pace = 周剩余份额 ÷ 距 reset 的小时数`**（"what it has left of its week per hour
until that renews, the most first"）。它统一了 smart 和 least-used 各自的失败模式
（源码注释原文）：least-used 会把「80% 剩余但 1 小时后 reset」排在新号前（浪费）；
smart 会把「3% 剩余 1 小时到期」排在「90% 剩余 2 小时到期」前（打光快没的）。
pace = **每小时的流失风险**。分带细节：先按 tier 分层，层内 Learns 优先，再按 pace
的**十分之一带宽**分组——注释里专门论证了为什么要「先排序后画带」：非传递性
（三个 pace 各差 1/12 会循环成环）。带内按衰减 token（见③）。
对照 YG：我们的快过期×3 虚拟实例 ≈ smart 思想；pace 是更平滑的统一度量，但依赖
「reset 时刻已知」——CodeBuddy 的到期是批次制（creditsEarliestExpiry），可推导。

### ② 90% 带的真实现：三档字典序分层，不是乘系数

```go
const LowShare = 90; UsedShare = provider.SpentShare
func tier(share) { >=UsedShare→2; >=LowShare→1; else 0 }
return append(append(fine, low...), spent...)  // 字典序拼接
```

带内（tier 1）按 Used 升序；近耗尽（tier 2）里 **Credits 多者优先**（spentBefore：
还能靠 credits 续命的先上）。**没有权重系数**——「软」体现在「仍在候选序列里，
只是排到 fine 之后（failover 才轮到）」。另一处精妙：Smart 层内按 `Soon`（各窗口
reset 时刻）比较时**截断到小时**（"to the hour, so a few minutes don't reorder"）。

### ③ 半衰期实锤：token 记账维度，非权重乘子

```go
const usageHalfLife = time.Hour
// tokenUse.now: u.n * math.Exp2(-Δt.Seconds()/usageHalfLife.Seconds())
// served: routed.used[rest].now(now) + float64(tokens)
```

作用在**该账号累计服务的 token 数**上（base-2 指数衰减），是 LeastUsed/Pace 带内的
**次级排序键**，不参与任何权重乘法。翻译到 YG 的加权抽签体系：等价物是给每账号记
「衰减 token 账」（RecordTokenUsage 已有累计值，缺时间戳衰减），选号时作为乘性因子
`w *= exp2(-recentTokens/scale)` 或带内次级降权——**不是**替换线性 idle 补偿（我此前
REQ 里的写法不准确，已修正）。

### 顺带佐证

`internal/gateway/count_tokens_test.go` 存在——count_tokens 是它网关层原生实现，
BACKLOG A1 的做法得到验证；`redact.go` 也在 gateway 层（隐私脱敏位置佐证不抄理由）。

## 七、实跑观察（2026-10-25，clone → nogui 构建 → serve 实测）

> 方法：`git clone --depth 1` → `CGO_ENABLED=0 go build -tags nogui`（71.5MB 单
> 二进制，本地工具链一次过）→ `magpie serve` headless 启动 + CLI 探测。以下全部
> 是**跑起来才知道**的增量（文档没写或写得不像）。隐私注：它自动发现的本机
> 订阅账号信息（邮箱等）不入库，只记行为。

1. **端点面 12 类 vs 我们 3 个**：根路径直接回清单——chat/completions、**responses**、
   messages、systemone、images/generations、images/edits、videos、embeddings、
   rerank、audio/transcriptions、audio/translations、v1beta/models/*。对 CodeBuddy
   场景多数用不上，但 **/v1/responses 是 Codex 客户端的真实缺口**（BACKLOG 已有）。
2. **/v1/models 富模型卡且无鉴权可读**：context_length/modalities/pricing/
   native_endpoints/display_name 全量元数据。我们的精简目录+三值语义是另一个
   极端（诚实但贫瘠）——可在不破坏三值语义下补观测字段（记 BACKLOG 候选）。
3. **账号自动发现（discovery 默认 on）**：读本机各 agent 的登录态直接变 provider
   （实测它发现了我本机 Codex 订阅，标记「3 of 3 models · fetched just now」）。
   这是它「零配置体验」的核心。**不抄**：与 R1（凭证不出 auths/）冲突，记为定位差异。
4. **usage.jsonl 真实 schema**：`{t, agent, provider, host, providerAccount, model,
   in/out/cache_read/cache_write, ms, status, ep, kind, source}`——**ep（端点）+
   kind（调用种类）+ source（probe/user）** 三个维度我们没有，连 model_list 探测
   都记账。可借鉴：reqlog 加 kind 维度（S 级，记 BACKLOG）。
5. **quota-history.json 真实 schema**：按 `provider|account` 键 → 窗口数组
   `{at, left, start, resetsAt}`，保留 30 天。比我们 creditsExpiring 单值丰富
   （历史可画趋势）——「周期边界检测」的基础设施形态实证。
6. **WorkBuddy 养号深度 = 仅签到**：`magpie accounts checkin` 只做每日签到循环
   （cli/accounts_cli.go:712）；全仓 grep「旅行/夜猫/成长任务/连登」零命中。
   **我们的五类养号（17 任务）是碾压级差异**——README 差异化叙事成立。
7. **运维成熟度**：backup/restore（加密整机备份）、webdav/s3/github 三通道同步、
   daemon 服务化、`magpie heap/cpu` 自诊断 profile 子命令。我们没有整机备份/
   自诊断（记 BACKLOG 候选：heap/cpu profile 端点，S 级）。
8. **分发对比**：nogui 71.5MB vs 我们 ~10MB（它内嵌 Bun 插件运行时）——「单二进制
   零依赖」差异的真身。

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
