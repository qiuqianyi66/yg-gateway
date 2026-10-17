# YG Gateway 深读笔记（agent / harness 视角）

> 读的时点：commit `9c68947`。规模（实测，非空行口径，与 `scripts/check-depth.ps1` 同口径）：
> 生产 `93 文件 / 29,339 行`，测试 `128 文件 / 44,357 行`（测试:生产 ≈ 1.5:1），`internal/` 18 个包。
>
> 证据分级（借用本仓自己的四态纪律）：**[亲验]** = 我直接读到源码/脚本/文档并核对了行号；
> **[待确认]** = 子 agent 深读得出、我未逐条复核，或与文档不一致处。
>
> 阅读路径：**§一 结论 → §二 / §三 证据 → §四 已处理清单**。

---

## 一、最值得抽出来复用的六件东西

| # | 东西 | 为什么值得复用 | 出处 |
|---|---|---|---|
| 1 | **四态事实标注 + Unknown 一等公民** | 把"推断"和"观察"在类型层分开，让"基于噪声做硬决策"变不可能 | `internal/evidence/`、`internal/unknown/`、ADR-007 |
| 2 | **规则 → 退出码**（`pre-commit.ps1` + 两个守卫脚本） | 文档里的规范没人看，退出码没法不看 | `scripts/pre-commit.ps1`、`check-boundaries.ps1`、`check-depth.ps1` |
| 3 | **棘轮（ratchet）而非绝对阈值** | 存量债不阻塞、新增债立即拦，避免"把阈值调到刚好通过" | `check-depth.ps1:5-14`、ADR-008 |
| 4 | **Seen 计数制**：同类坑第 2 次即上规则/机器检查 | 不靠"记得住"，靠计数触发制度化 | `.harness/LESSONS.md` |
| 5 | **反向探针 / 变异测试**（负向探针证明门禁有牙齿） | 门禁最大的风险是"假绿"，必须自证不空转 | `LESSONS.md` L-2/L-6、`deploy-prod.rehearsal.ps1` |
| 6 | **部署 = 一条脚本化的命令 + 沙箱排练** | 破坏性操作的每个分支都被负向测试覆盖过 | `scripts/deploy-prod.ps1`、`deploy-prod.rehearsal.ps1` |

> 这六条即取舍结论；§二 讲"跟我相关"，§三 讲"底层机制"——同一件事只在一处详述，另一处给指针。

---

## 二、跟我（agent / harness）相关的线

### 2.1 四态认知纪律 —— 全仓最独特的设计 **[亲验]**

`internal/evidence/kind.go:20-35` 定义四态：`observation` / `derivation` / `inference` / `unknown`。
来源写明是 `morluto/rea` 的纪律，核心一句：**Missing coverage is unknown, not absence**（没覆盖 = 未知，不是"没有"）。

工程上真正落地的四个细节：

1. **零值即 unknown**（`kind.go:83-85,113-118`）
   `IsUnknown()` 判的是"不等于前三者中任何一个"，不是"等于 unknown"。于是**忘标注留在结构体里的零值自动落进最保守档**——
   把"漏标注"的默认后果从"被当成观察"翻成"未知"。
   → 对我这类 agent：置信度应当由**输出结构**要求，而不是靠自觉。

2. **samples 只要 >0 就输出，与 kind 无关**（`kind.go:93-98,107-109`）
   `{"kind":"unknown","samples":2}` 表达的是"有 2/5 个样本，还差 3 条"，跟"完全没数据"是两个状态。
   → 缺失 ≠ 零 ≠ 有：这三个语义必须能被下游分开读。

3. **标注不可信，而不是隐藏不确定**（ADR-007:63-73）
   最初实现是"EWMA 样本不足就不输出数字"，被既有契约测试 `TestEwmaStatusExposure` 抓回。
   改成"保留数值 + 标注 unknown"，理由是 rea 的 *Preserve partial facts*：藏掉会让运维失去观察手段。
   → 这条对我直接适用：**不要让不确定的值消失，要让它带着标签出现**。

4. **消解只能由观察完成，且"推断不能消解推断"**（`internal/unknown/registry.go:110-113`）
   `Resolve` 强制 `evidenceID` 非空；全仓唯一调用点 `pool` 的 `NoteModelCost`（[待确认] 由深读 agent 定位到 `state.go:214-220`），
   且它在 `tokens<=0` 时提前 return、根本不消解。
   → 纪律靠**接口可见性**保证，而不是靠运行期校验。

### 2.2 Unknown 是一等公民实体 **[亲验]**

`internal/unknown/registry.go` 是本仓少见的、把"不知道"当对象建模的实现：

| 设计 | 行号 | 用意 |
|---|---|---|
| 登记必须写 `requires`（非空），否则报错 | `:77-83` | 说不清"需要什么才能消解"的条目永远消不掉，登记它就是造噪音 → **fail-closed** |
| `revision` 乐观并发，过期必报错且**不改任何字段** | `:110-131` | 禁止盲重试：原地重试永远失败，还会掩盖"别人已经消解了" |
| ID = `sha256(question+"\x00"+requires)[:8]` | `:183-186` | 内容哈希 → 幂等去重 + **可预计算**（调用方可以拿 ID 当缓存 key，不必进锁） |
| `List` 只回未消解，`All` 含已消解 | `:146-169` | 未消解的不占注意力；已消解的留给审计 |

`\x00` 分隔符这个细节值得单列：避免 `"ab"+"c"` 与 `"a"+"bc"` 撞出同一摘要。

**它带来的可观测信号**：`/metrics` 的 `yg_gateway_unknowns_open` **持续不为零本身就是信号**（ADR-007:96-98）——
要么池子在扩，要么某些 `requires` 根本不可满足。这是"给自己配一个未关闭计数"的范式。

### 2.3 `.harness/` 治理体系：把规范变成退出码 **[亲验]**

```
.harness/
├── rules/     6 份规范（编码 15 红线 / 工程结构 / 十阶段流程 / ERROR-HANDLING / COST-CONTROL / 自检清单）
├── skills/    11 个 SKILL.md（main-dev 6 / biz-dev 2 / ops 1 / trouble-shooting 2）
├── changes/   20 份变更记录（每份 summary.md + 视级别补 review.md / PRD / ADR）
├── wiki/      glossary + cc-switch 深读
├── LESSONS.md 14 条坑（Seen 计数制）+ 3 条已知 flake
└── depth-baseline.json  棘轮基线（maxFileLines 2058 / oversizeFiles 5 / oversizeFuncs 7 / minCoverage 72.3）
```

**机制 1：棘轮而非绝对阈值**（`scripts/check-depth.ps1:5-14`）
绝对阈值在存量代码上必然一次性全红，只能选"要么大重构要么放弃门禁"，现实中会退化成"把阈值调到刚好通过"。
棘轮只要求**不许比上次更差**，`-Update` 主动抬高以冻结收益。基线入库，CI 强制。

**机制 2：白名单式依赖守卫**（`scripts/check-boundaries.ps1:7-13`）
> "新包被随手引入时，白名单会**默认拒绝**，迫使人显式声明它属于哪一层；黑名单则会默认放行，直到有人记得来更新。"

且白名单由 `-Fix` **实测生成**、不是拍脑袋写的（`check-boundaries.ps1:33-36`）。

**机制 3：Seen 计数制**（`LESSONS.md:3-12`）
同一类坑**第 2 次出现就必须升级**为规则，或（更好）变成 `scripts/` 里的机器检查。且本文件只记**会伪造结论**的坑，时间敏感 flake 单列 §二。

几个可直接搬运的坑（都是我亲验读过原文的）：

| 坑 | 形态 | 升级落点 |
|---|---|---|
| L-1 | PS 5.1 里反斜杠不是转义符 → 双引号串提前结束，整条命令一个字都没执行，而执行者以为跑过了 | `pre-commit.ps1` 检查 3 |
| L-3 | 未加引号的 `-flag=$var` 在 PS 5.1 里**不展开** → 静默生成名为 `$cov` 的垃圾文件 | `pre-commit.ps1` 检查 4 |
| L-10 | 用"替换一段文本"实现"追加一段" → 锚点选错就**吃掉内容**（连踩 4 次） | 规则：锚点选**要保留的那一行**，改完看 `git diff --stat` |
| L-11 | 测试与它依赖的生产改动不在同一提交 → **本地全绿、CI 红** | 干净 HEAD 工作树复跑（`git worktree add ... HEAD --detach`） |
| L-13 | PS 5.1 `Invoke-WebRequest` 非 2xx 时 body 已被读掉 → 503 被判成"没响应"，**误触发回滚** | 改用 `HttpWebRequest` 自己读状态码+body |

**L-3 的取舍最值钱**：机器检查的候选本来是"扫新增行里的 `-flag=$var` 写法"，但那要处理引号/换行/跨行、误报面大；
最终改成**扫产物**——"仓库根出现以 `$` 开头的文件"即失败，零误报（`$` 开头的名字在仓库根不可能有正当用途）。
→ **检查"错误的产物"比检查"错误的写法"更便宜**。

**机制 4：三个脚本共用 `0 / 1 / 2` 三态退出码**
`0`=通过，`1`=存在违规/回退，`2`=脚本自身失败（`check-depth.ps1:27`、`check-boundaries.ps1:15`、`pre-commit.ps1:25-26`）。
`check-depth.ps1:57-60` 在"找不到任何生产 `.go` 文件"时 `exit 2` 而不是 `0`。
→ **把"判不了"和"判负"分开**，否则会把环境/工具故障修成阈值放宽，或把故障读成通过。

**机制 5：门禁本身走 TDD —— 先证明旧门禁放过这个坑（RED），再加检查（GREEN），最后跑误报探针**
L-3 落地时先造出 `$out` 跑门禁得到 `exit=0`——**这就是"旧门禁一直放过它"的证据**；加检查后两条负向探针均 `exit 1` 并点名；
随后跑**误报探针**（仓库根一个正常的未跟踪 `.md` 不得被点）。CI 侧同法：放一个 gofmt 漂移文件 + 提交一个含 `_ = v` 的文件，
并报告"1 个改动文件"以证明 base 差分**确实枚举到了已提交改动**（不是空转）。
→ 没有 RED 就无法区分"新检查在起作用"和"这个坑本来就不会发生"；没有误报探针就分不清"有牙齿"和"乱报错"。

**机制 6：变异测试的三件防伪装备（且刻意不入门禁）** —— 细节见 §3.9
`GOMOD` 断言（变异前先断言 `go env GOMOD` 指向副本；cwd 落在原件会得到"5/5 全存活"的**假结果**）、
SHA256 闭环（记哈希 → 备份 → 改 → 测 → 还原 → 复核）、逐字节自证元测试。
不入门禁的理由：改生产代码 + 跑全量 + 还原超出提交前时间预算。
→ 覆盖率涨了 ≠ 行为被钉住：`prompt.Degraded` 曾是自证循环断言，`redisstore` 删掉并发上限仍全绿。

**机制 7：四处专门防"门禁假绿"**（每一处都是"看起来在跑、实际什么都没查"）：

| 假绿形态 | 修法 |
|---|---|
| CI 工作区干净 → 工作区 diff 恒空 → "检查了 0 个文件" | 同一脚本两套作用域：`-Base` 用 `git diff <ref>...HEAD`，`-AllFiles` 查全仓 gofmt |
| Linux 上反斜杠不是分隔符 → 基线路径拼不出来 → 走"无基线→初始化→exit 0" → **棘轮永远绿** | 分段 `Join-Path`；并把这个"自动初始化"分支本身记为 L-7 风险点 |
| PR `paths` 过滤器曾不含 `scripts/**` → **改门禁不需要门禁验** | 补 `scripts/**` 与 `.harness/**`；gate 抽成 `workflow_call` 给两条产物线共用（门禁复制两份必然分叉） |
| 本地 `pre-commit.ps1` 可被 `--no-verify` / 忘跑 / 并行会话直接 push 绕过 | 承认本地只是"提前发现"，**不可绕过的最终闸门交给 CI**；`build needs: [test, gate]`，gate 红就不产出任何产物 |

**机制 8：skills 的写法模板**（11 个 SKILL.md 一致）：`description` 末尾**必须带【跳过】条件**（把"什么时候不该用"前置到调度层可见处），
正文固定为 `触发条件 → 前置硬门（缺失回退）→ 自检 checkbox → 下一步（指向下一技能名）`，
并写死上下文预算（`coding-skill:62-64`「上下文 ≤4 份；已读不重复读；frontmatter 描述命中才读全文」）。
链条：`request-analysis → coding-skill → unit-test-write → unit-test-ci → expert-reviewer → deploy-verify`。

### 2.4 我这个宿主本身也被改造过 **[亲验 + 待确认]**

有意思的是：这个仓库不只是"给人用的网关"，它还记录了对**我运行所在的 dsh 客户端**的改造。

- **76 项 patch 的指令权威性清洗**（`purge_status` 现存状态：`72/74 applied`，涵盖提示词层 / 代码默认 / 引擎级审批与沙箱绕过），
  备份在案，可一键 `purge_revert`。
- **`defaultInput` 的根因链**（`.harness/changes/2026-10-16-dsh-default-input/summary.md:19-27`）：
  `@deepseek-ai/dsh-llm-pi-ai/lib/index.js:906` 的 `const DEFAULT_INPUT = ["text"]`
  把"未知"写死成"不支持文本以外"，于是图片能力在桥接层被静默砍掉。修法是在 `cordis.patch.yml` 加 provider 级
  `defaultInput: ["text","image"]`（解析优先级：条目级 > 目录 base > provider 默认，`:682`）。
  → 这也是本仓四态纪律的一个应用：**"插件没声明"被默认当成了"不支持"**，正是"未知 ≠ 否"的反面教材。

- 该变更总结里有一句很"agent 味"的更正（§三.4）：上一份交接文档说"用 reqlog 确认 `image_url` 真出站"，
  实际 reqlog **不含请求体**，方案不可行；改用**行为级证据**（模型能描述图片内容）——
  "它证明图片不只是发出去了，而是被模型**理解**了"。**证据强度分层**做得比多数人类文档好。

### 2.5 `工作准则-融入版.md`：方法论吸收的取舍样本 **[亲验]**

对一份外部最佳实践的评估结论是**融入 2 条、确认已覆盖 4 条、明确拒绝 4 组**，拒绝理由都指向"与本仓已有体系重复"
（如拒绝铺 CLAUDE.md 骨架、拒绝重复的 drift check/changelog 治理）。
→ **不堆砌**这条元纪律，比"吸收了什么"更有价值。

---

## 三、底层原理：我认为做得好的机制

> 本节除标注 **[亲验]** 的点外，均来自子 agent 深读、我未逐条复核；引用前建议按行号复看一遍。

### 3.1 选号：两阶段（top5 截断 → 定点加权抽签）**[待确认，来自深读 agent]**

- 权重 `w = 1.0 + (credits/maxCredits)×10 + idle`，`idle = min(小时数×0.5, 5.0)`，从未使用直接给满分（`pool/pick.go:483-507`、`entry.go:695-696`）。
- 第一阶段按 `(costPer1k↑, w↓, uid)` 稳定排序后**截断 5 个**；候选 >5 且有权重并列时，用 **time-seeded 独立 PCG** 先洗牌（刻意不消耗注入随机源，保住确定性测试语义）。
- 第二阶段在 `eligible`（`lastUsed` 距今 ≥ `minPickGap=100ms`）内做权重**定点放大 1e6** 的整数累加抽签。

好在两点：既避免"永远打同一个"（负载集中），又避免"随机把最差的放大"；且顺带给出 O(n log n)→O(n) 的范式（权重/成本只算一遍，不在比较器里现算）。
**硬截断的代价**：第 6 名永远选不中，所以排序依据必须含闲置补偿，否则久置低分号永久进不了短名单（`TestPickTopFiveByIdleNotCredits` 就是这条的回归护栏）。

### 3.2 防惊群用**单调逻辑序号**而不是墙钟 **[待确认]**

`lastUsed` 用 `atomic.Int64(unixnano)` 存，但 LRU 兜底比较用 `usedSeq`（`pool.pickSeq` 自增分配）。
原因写在代码里：**Windows `time.Now()` 精度约 0.5ms**，快速连续选号会让 `lastUsed` 完全相等，按墙钟比较会恒选 `candsAll[0]`。
→ "按墙钟做 LRU"是通用 bug 源（时钟精度 / 回拨 / 休眠），修法可直接搬运。

### 3.3 EWMA 回灌：有界扰动 + 冷启动中性 **[待确认]**

- `α=0.1`，`min_samples=5`，成功率基线 `0.95`；`sr>0.95` 线性加到 `+15%`，`sr<0.95` 线性减到 `−15%`；
  延迟**只罚** `> 池中位×1.5` 的号，线性升到 `−30%` 封顶，**快号不加分**。
- 综合因子 ∈ `[0.595, 1.15]` → 不可能清零权重（饿死防护）；惩罚上限远低于硬门槛，不会在熔断前抢先改判。
- **样本不足 / 无池中位 / `ewmaEnabled=false`（默认关）→ 完全不参与加权**；首个事件前懒初始化 `sr=1.0`（乐观初值）。

设计哲学一句话：**固定基线而非池相对基线**（全池劣化时相对排序不变、不随噪声漂移），
且"快号不加分"避免"快号越用越多、慢号饿死"的正反馈。

### 3.4 冷却/熔断：惩罚必须绑定**它的恢复证据** **[待确认]**

- 熔断：`fails≥3` 触发，退避 `30m × 2^n` 封顶 `6h`；到期后**不是全量放回**，而是 half-open **每 30s 至多 1 次**试探，
  成功即走 `NoteSuccess` 天然全量恢复。
- 关键位置选择：**限速放在 `pick` 而不放进 `healthy`**，因为 `healthy` 被 `/healthz` 探活与 `CountsDetailed` 共用，
  写进去会出现"面板显示不可用但实际能选中"的口径漂移 → **限速是选号策略，不是健康状态**。
- 软冷却：有上游重置墙钟 → `min(resetAt, now+2h)`；否则 `base << (streak−1)` 封顶 `2h`，
  且**已在冷却中不推进 streak、不延长 until**（"兜底探测不翻倍"）。修掉了"越重试越冷、全池被推到 2h 封顶"的真实现象。
- 6004/11102 走**模型级独立冷却表** `modelCooldowns[model]`，实现"切模型即可用"；台账不被余额刷新清除，因为**恢复证据是上游重置墙钟而非余额**。

最后这句是通用纪律：不同失败原因各有权威恢复信号，用一个综合计数器或统一时间戳就会互相覆盖。

### 3.5 `credit_floor`：护栏按"已知价格拦、未知不拦但在别处登记" **[待确认]**

两级判据（`pool/pick.go:339-366`）：
1. 有本地实测台账（6h TTL 内）→ `CostPer1k > 0` 才拦（免费放行，且**本地实测优先于目录牌价**）；
2. 无实测 → 查上游目录倍率：**空 / 非数值 → 放行**，`>0 → 拦`。

为什么必须有第 2 级：只用本地实测会让"全池都没学过的高价新模型"**恒等于放行**——
实案 `kimi-k3-1`（目录 x1.62、223 分/百万 token）两笔打穿一个 100 分的号并硬冷却到次日 04:00。
且兜底路径（全冷却时）必须用**同一条判据**，否则"floor 把健康号全拦掉 → 兜底把触底号捞回来继续接收费模型"。

### 3.6 三段式 UA 是**能力选择器**，不只是指纹 **[待确认]**

`WorkBuddy/<clientVer> <platform>/<clientVer> CLI/<cliVer>`。实测同一个 `/v3/config`：
IDE UA 下 14 条模型、CLI UA 下 22 条，**两路各有独有模型**（`client.go:1330-1347`）。
所以 UA 不是装饰字符串，是协议的一部分——`AGENTS.md:36-42` 把它上提为 R8 红线是对的。

配套的 `conversationId`（`wb2api-` 族）只生成形状、**只透传、绝不重写**；取不到 `X-Conversation-ID` 就**不发**（不伪造）。
真正的聚合主键是 `X-Conversation-Request-ID`，**在轮转循环外生成一次、循环内复用**——会话级键与轮级键分离。

### 3.7 流式：把"已受理"提前到首帧之前，超时按**阶段**设 **[待确认]**

- `StreamHint` 先设 SSE 头并立刻 `WriteHeader(200)+Flush`，把 TTFB 感知与上游首 token 解耦；之后逐帧 `writeSSE+Flush`，全程不聚合。
- 四层预算：`DialContext 10s` / `TLS 握手 10s` / `ResponseHeaderTimeout` / 流中静默侦测 + `StreamTotalTimeout`。
  单一超时无法同时覆盖"连不上 / 首包慢 / 流中卡 / 滴字节永不结束"四种形态，最后一种会**无限占住请求+账号租约+全局在途名额**。

### 3.8 缓存 key：双因子（租户隔离 + 会话） **[待确认]**

`wb2a-<uid8>-<sha256(uid|conv) 前 16 字节>`，**客户端显式值优先、绝不覆盖**。
`uid` 段是硬隔离因子：跨账号复用同一 cache key 会命中别人的前缀缓存，**既泄露对话内容又搞错用量归属**。
收益实测：同一段 8k 前缀，不带 key `credit≈0.34`，带 key 命中 7808 token / `credit≈0.02`（约 17 倍，单点样本）。

### 3.9 R8 golden：把红线钉死，并**自证比对器不空转** **[待确认]**

- 归一化可变值（`<ver>` / `<32hex>` / `<uuid>` / `<digits>`…），只钉**结构**：段数/段序/分隔符/大小写/字面量/候选序列顺序/头族是否发出（覆盖 10 个 section）。
- 两条自证：`HoldsCriticalLiterals` 断言 golden 仍含 11 个关键片段（防被清空或重写坏）；`ComparatorDetectsEveryByteEdit` 对 **5968 个字节位置**各篡改一次，断言比对器每次报差异。

→ "正确性不能用它自己来证明"——这是把文档红线机器化的样板。
牙齿用 7 条定向变异在 `%TEMP%` 副本内验证，其中 `M7`（`wb2api-night-` 前缀）在补进 golden 前**没有任何断言**——说明 golden 补的是真实盲区。
**失效点也值得学**：首版归一化清单漏了 16-hex，`X-B3-SpanId` 每次都变导致 golden 每次都不同，靠连续多轮比对才发现。**归一化清单本身就是最脆的一环。**

### 3.10 部署：每个失败分支都被负向测试覆盖 **[待确认]**

- 预检 PE（`MZ` + `PE\0\0`）把"根本不是可执行文件"挡在**停服之前**——因为把 33 字节文本当 exe 启动会弹**模态对话框**，把 launcher 卡死（L-12）。
- 替换用 `写 .staged + Move-Item -Force` 改名替换，**不直接覆盖**：覆盖要拿写权限，改名只需 `FILE_SHARE_DELETE`（L-14）。
  这条恰恰发生在"最需要回滚的时候"：坏 exe 的 launcher 占着文件，覆盖式回滚持续失败。
- 启动用 `cmd /c "... > out 2> err"`（整串再包一层引号），**不用** `Start-Process -RedirectStandard*`：
  常驻孙子进程会持有调用方 stdout 管道，把外层调用阻塞到网关退出（实测 8865ms vs 3716ms）。
- 沙箱排练四场景，其中两条是硬牙齿：**A1 非 PE 必须"零触碰"生产**（四条证据：无 `.bak`、exe 哈希未变、无 `.staged` 残留、旧 PID 未变）；
  **A2 合法 PE 但立刻退出 → 必须自动回滚**且 `rollback_log_path ≠ log_path`（回滚不许覆盖失败现场）。
- 退出码区分结局（1 预检拒 / 2 回滚 / 3 冒烟失败），且**冒烟失败不自动回滚**——多为上游账号/额度问题，来回折腾更糟。

### 3.11 安全：默认拒绝要覆盖"配置缺失"这条路径 **[待确认]**

- `httpauth`：比较双方都是 **SHA-256 摘要**再 `subtle.ConstantTimeCompare`；**缺头路径也刻意做一次比较**，保持耗时形状一致。
  多 Key 用 12 位 hex 前缀分桶，但**前缀只缩小候选集、最终始终比完整摘要**（不变量写进注释）。
- 面板鉴权与网关 API 鉴权**刻意不同口径**：面板只认管理员单 key；多 Key 已启用但管理员 key 为空 → **fail closed 拒绝匿名**。
  → "没配"不能静默升级成"全开"。
- 入站三项：body 上限（防内存 DoS）、**受信任代理**判定后才采信 XFF 且**从右往左**取第一个非受信任 IP（nginx `$proxy_add_x_forwarded_for` 是追加语义，首段仍客户端可控）、只读端点限流。
- 出站禁 h2 的**正确**写法是 `TLSNextProto: make(map[string]...)`（空 map，非 nil），
  `ForceAttemptHTTP2=false` 只对自定义 Dial 生效、默认 ALPN 仍协商出 h2。
- Windows 凭证 ACL：Go 的 `os.WriteFile(...,0o600)` **不剥离继承 ACL**，必须 `icacls /inheritance:r /grant:r`；
  且刻意**不用** `Set-Acl`（带 SACL 时需 `SeSecurityPrivilege`，非提升进程会 `PrivilegeNotHeld`）。

### 3.12 脱敏是**结构层面**的，不是过滤器 **[待确认]**

`reqlog.Event` 里**根本没有**承载 body / messages / prompt / Authorization 的字段——
"哪天忘了脱敏"这条代码路径不存在。`CacheMissUnknown` 这类存在性标记专门避免用 0 冒充"全部命中"。
UID 在日志侧只留前 8 位（`logfmt.UID8`），全量只留在 `data/state.json`。

### 3.13 提示词注入三档 **[待确认]**

`custom`（丢所有 system 再 prepend）/ `append`（只在开头连续 system 块之后插一条）/ `passthrough`，
外加降级期一律换成极简中性句。两条纪律值得抄：

- **注入器永远不能成为新的故障源**：空 body / 解析失败 / marshal 失败一律**原样返回**；
- **不制造会被下游二次改写的中间态**：`Append` 插入的网关消息角色写 `system` 而非 `developer`（归一发生在下游 `normalizeRoles`）。

---

## 四、发现的漂移与待决策（14 条）

> 14 条**全部处理完**：A 组是改字/改注释；B 组涉及行为或对外承诺，其中 B1/B4 动了代码并配了反向变异证据。
> 变更记录：`.harness/changes/2026-10-16-doc-drift-fix/`（A 组 + B2/B3 文档侧）、
> `.harness/changes/2026-10-16-unknown-reopen-and-tristate/`（B1/B4 代码侧）。

### A. 文档与注释修补（8 条）

变更记录：`.harness/changes/2026-10-16-doc-drift-fix/summary.md`（门禁全绿 `exit 0`）。

| # | 位置 | 现象 | 处理 |
|---|---|---|---|
| A1 | `AGENTS.md:77`、`pool/entry.go:129` | 写"三态"，实际是**四态** | 统一为"四态" |
| A2 | `.harness/rules/编码规范.md:158-163` | ①口径描述称用 `Measure-Object -Line`（实现是**显式过滤空白行**）；②写死了 5 个文件的行数，且实测值已过时 | **不写死数字**，改为指向 `check-depth.ps1 -Detail`。执行期间实测到 `handler.go` 1387→1389、`autotask.go` 1225→1226（并行会话在改同一工作区）——**写死即漂，这是根因不是笔误** |
| A3 | `internal/reqlog/reqlog.go:64-88` | 实测 23 个导出字段，历史变更记录里写 21 | **不改**：`changes/` 是历史留档；`reqlog.go` 包注释已声明"不写提示词/正文/Authorization"，不另加易漂的计数 |
| A4 | `pool/entry.go:273` vs 同文件 `:618` | 同文件自相矛盾：`:273` 说 `modelCooldowns` 不持久化，`:618` 说必须持久化（`persist.go:204-212,341-349` 证明持久化） | 删掉 `:273` 的"不持久化"，改为与实现一致的持久化说明 |
| A5 | `cmd/server/config.go:331` | 注释"权重 ×3"，实现是 `const expiringVirtualSlots = 2`（`pool/realm.go:12`） | 改为 ×2（slots=2） |
| A6 | `panel/autotask.go:18-19` | 包头注释说 `skill_1` 未破解、不做 `Expert_lighthouse`，后文 `:122-130` 已实现并称点亮 | 改为"已补齐"，保留仍不做的 `Expert_Philanthropy` |
| A7 | `cmd/server/main.go:423` | 注释"请求体已无网关侧上限"，实际默认 `64MiB`（`config.go:725-727`） | 改为指向 `server.max_body_bytes`（默认 64MiB，显式 0 才关闭） |
| A8 | `.harness/rules/开发流程规范.md:157` | 提到 `verify-harness.cjs`（原文带"如已建"），仓库内不存在 | 改为 `scripts/pre-commit.ps1 -Fast` + CI `gate` job |

### B. 行为与承诺（6 条）

| # | 问题 | 处理 |
|---|---|---|
| B1 | `mdMatch` 用 `!m.supports_images` 判否 → 把「缺失=未知」塌缩成「不支持」，视觉筛选会藏掉模型且不报错 | 前端改**三值**：只排除显式 `false`；档位缺失显示「档位未知」。配 `TestAppJSModelFilterUnknownCapability`（变异 M2 变红） |
| B2 | `ADR-007` 承诺的 `internal/tools/doccheck` 不存在 | 改为**明确标注未落地**；不建工具（避免新增维护面） |
| B3 | `evidence` 只服务于成本链路，`KindObservation` 未在生产代码出现 | `AGENTS.md` 增「实现口径」：未知以**字段省略**表达，消费方按三值处理、**禁止** `!field` 判否；不扩面（为"用满四态"而扩面是本末倒置） |
| B4 | `unknown` 消解后单向闩锁 → TTL 过期后 `unknowns_open` 仍读 0 | 新增 `registry.Reopen`（与 `Resolve` 对称的 CAS、`reason` 必填）+ `pruneAllLocked` 过期清理时重开；触发点在**落盘路径**，不碰选号临界区。配 `TestExpiredCostObservationReopensUnknown`（变异 M1 变红） |
| B5 | AGENTS 的依赖基线是手抄子集 | 删表，改为指向 `check-boundaries.ps1` 的 `allow` 表（唯一真相） |
| B6 | 技能链无机器校验 | **决定不做**：要动 `pre-commit.ps1` 8 处编号标签 + 2 份文档，成本 > 收益；记为已接受的边界 |

---

*本笔记为阅读产出，未修改任何源码。生成于会话 `session-ef2245cb`。*
