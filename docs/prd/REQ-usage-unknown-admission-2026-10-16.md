# REQ — 计量口径：unknown 不得冒充 0（写台账入口的准入）

- 状态：已拍板（用户决策 D3：**改**——「写台账入口加 unknown 准入」，而不是只加观测字段）
- 日期：2026-10-16
- 来源：`docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3 T7 与 §7 D3；
  `.harness/rules/ERROR-HANDLING.md` §「留待产品定夺」表（4 行）
- 相关纪律：`AGENTS.md`「事实语义纪律（三态分离）」与「Unknown 消解纪律」；
  `docs/adr/ADR-007-evidence-and-unknown.md`

> **实施状态（2026-10-16）**：**P1–P4 全部已实施并验证**（P1/P2 见
> `.harness/changes/2026-10-16-t7-unknown-admission/summary.md` §一–§三，P3/P4 见 §五，每项都有反向变异证据）。
> 实施中有一处设计偏差，见 §三.0。

## 一、问题（为什么必须改）

本仓的养号决策直接消费成本台账与缓存统计。当**解析失败**被迫用零值兜底时，
`0` 会被当成**观察值**写进台账，而它真正的语义是 **unknown**。后果不是报错，而是**无声地把噪声当事实**：

| # | 位置 | 现状 | 后果 |
|---|---|---|---|
| P1 | `internal/server/handler.go:988-989` `c, _ := usage["credit"].(float64)` + `t, _ := usage["total_tokens"].(float64)` | 类型断言失败 → 零值兜底；`credit=0` 与 `tokens>0` 一起进 `NoteModelCost` | 记为 `per1k=0` 的**观测样本** → 成本 tier 被判成「免费/限免」，一个付费号可能因此被当成免费号选号（与 ADR-007 的 kimi-k3-1 实案同族） |
| P2 | `internal/server/chatrun.go:441`（及同族流式路径）`miss, _ := upstream.UsageCacheMissTokens(usage)` | 上游没给 miss 时记 `0` | `0` = 「全部命中缓存」与「miss 未知」不可区分；`UsageCacheMissTokens` **本来已经返回 `(float64, bool)`**，是调用方丢掉了 `bool`。ERROR-HANDLING 明确要求**流式/非流式两处同改**，只改一处会造成口径分叉 |
| P3 | `internal/server/anthropic.go` 的 `json.Unmarshal(body, &peek)` | `"stream":"true"`（字符串）类型错被吞 | 静默走非流式分支——「不知道」被当成「不是流式」 |
| P4 | `cmd/login/main.go` `runPoll` 状态码分档 | 4xx 全归「登录未完成」 | 真 401/403 时用户按到天亮也不会成功，且看不到上游状态码 |

P1–P3 是同一类错误：**把「不知道」编码成某个具体值**。P4 是「把不同的失败归成一类」。

## 二、目标与非目标

**目标**：任何**未经观察**的计量输入，都不得以具体数值（尤其 `0`）进入台账或日志；
必须能以结构化方式表达为 unknown，并且**可被人看到**（日志/指标/unknown 登记）。

**非目标（明确不做）**：

- **不改已有数字的算法**：EWMA、tier 归一、分桶插值分位数一律不动。本变更只挡「零值冒充观察」。
- **不改 panel 前端**：面板 unknown 展示由并行会话在推进（同期在改 `internal/panel/*`），
  本变更只保证 unknown 进入既有登记链路（`internal/unknown/`），面板侧不需要为本需求额外改动。
- **不动 R8**（上游协议报文）与出站报文形状。
- **不引入依赖**（`go.mod` 直接依赖仍只有 redis）。

## 三、设计（写台账入口的准入，而不是逐处打补丁）

### 0. 实施偏差（如实记录）

原计划在 `internal/pool` 的 `NoteModelCost` **入口**加准入（例如新增 `NoteModelCostUnknown`）。
实施时判定**不需要动 pool 的 API**，理由是：

- 受伤的唯一入口是**解析点**：`usage["credit"].(float64)` 零值兜底。`float64` 本身无法携带
  provenance，所以「一个 `credit=0` 是观察还是未知」在 pool 入口**原理上不可判定**——
  加参数只是把同一个判断挪个地方，还要改所有调用点与公开 API。
- 「不消解」本身就是 pool 侧已有的准入语义：`TrackCostUnknown` 在请求路径登记，
  `NoteModelCost` 才消解。所以只要**不调用** `NoteModelCost`，该 (uid,model) 的成本未知就保持未消解
  （`/metrics` 的 `unknowns_open` 持续非零 = 可见信号）。
- 因此实施为：**在解析点把三态判出来**（观察 / 无分母 / 未知），只在「观察」时调 `NoteModelCost`，
  「未知」时打 WARN 且不消解。这与「写台账入口的准入」是同一件事，只是把准入放在了
  唯一能判定的位置。**`NoteModelCost` 的语义同时被收紧并写进注释**：它只接受观察值。

### 1. 术语固定：`NoteModelCost(uid, model, credit, tokens)` 的语义收紧为「**这是一次观察**」。
   调用方只有在**确实解析到上游数值**时才可调用它。
2. **新增 unknown 入口**（`internal/pool`）：`NoteModelCostUnknown(uid, model, reason string)`：
   - **不写**成本样本（不产生 `per1k=0` 的假观测）；
   - 以幂等方式登记到 `internal/unknown/`（`requires` = 「一次带 credit 的成功调用」），
     并递增一个指标（Unknown 是既有的一等公民实体，`pool → unknown` 已在边界白名单内）；
   - 记一条 WARN（含脱敏 `uid` 前 8 位与 `model`，守 R2）。
3. **调用侧三处改判据**：
   - `handler.go`：`c, okC := usage["credit"].(float64)`；`t, okT := ...`。
     `okC && okT` → 走 `NoteModelCost`（观察）；否则 → `NoteModelCostUnknown`（unknown）。
   - `chatrun.go` 及其同族路径：`miss, ok := upstream.UsageCacheMissTokens(usage)`；
     `ok=false` 时落 **unknown 标记**（reqlog 侧不带数字，见第 4 条），而不是 `0`。
   - `anthropic.go`：`stream` 字段非 bool → 记 unknown/WARN，**行为保持当前分支**（不趁机改路由）。
4. **reqlog 的可观测表达**：`cache_miss_tokens` 为 unknown 时，**省略数字**并补 `cache_miss_known: false`
   （字段增加是纯增量；已有消费者读缺失字段会得到「没有」，而不是「0」）。
5. **P4（`cmd/login`）本轮只登记不改**：它改的是 CLI 提示文案与状态分档，不是计量口径，
   混进来会让本 PRD 的验收标准变形。另立小 PRD（或在下一轮 T7 变体里）处理。

## 四、验收（Given-When-Then）

| # | Given | When | Then |
|---|---|---|---|
| A1 | 上游末帧 `usage.credit` 为字符串 `"0.1"`（类型错）而 `total_tokens` 正常 | 该请求结束 | 台账**不新增**该 (uid,model) 的成本样本（`count` 不变）；`unknowns_open` +1；日志出现带脱敏 uid 的 WARN |
| A2 | 上游末帧 `usage.credit` 为数值 `0`（真实免费模型） | 该请求结束 | 仍按**观察**记录（tier 0 样本可写）——free 模型不得被误判为 unknown（防过度纠正） |
| A3 | 上游没给 miss 字段且 prompt 不可用（`UsageCacheMissTokens`/`CacheTokens` 返回 miss 未知） | 请求结束 | reqlog 行**无** `cache_miss_tokens` 数字、含 `cache_miss_unknown: true`；该次**不进入**命中率统计（不喂 `noteCacheTokens`） |
| A4 | 上游显式给了 `prompt_cache_miss_tokens: 0` | 请求结束 | 按**观察**处理（`missKnown=true`、进入命中率统计）；reqlog 行**不出现** `cache_miss_unknown`（0 因 `omitempty` 也不出现数字，历史行形态不变） |
| A5 | 同上（A3/A4）在**流式**路径 | 流式请求结束 | 与非流式口径一致（两处同改，无分叉） |
| A6 | `anthropic` 请求体 `"stream":"true"`（字符串） | 请求处理 | 仍走当前分支；日志出现 unknown/WARN（不静默） |
| A7 | 上述每条改动 | 单测 | 每条新断言都能回答「还原什么行为会让它 FAIL」；`go test ./... -count=1` 全绿 |

## 五、风险与回滚

| 风险 | 说明 | 处理 |
|---|---|---|
| 过度纠正 | 把真实的 `credit=0`（免费模型）也挡成 unknown，反而丢掉有效的免费观测 | A2 作为硬验收；`NoteModelCost` 的**观察**语义不变 |
| 调用点遗漏 | 成本台账可能在**多于一处**被写（流式/非流式/带图探测） | 先枚举全部 `NoteModelCost` 调用点（当前实测：`chatrun.go:402`、`:463`），逐处判定 |
| unknown 泛滥 | 若上游普遍不返回 credit，`unknowns_open` 会持续非零——按 ADR-007 这是**信号**而不是故障 | 指标解释写进 summary；若长期不降，说明 `requires` 设计有问题 |
| 回滚 | 三处判据改动彼此独立 | `git revert <sha>`；reqlog 新字段为纯增量，旧消费者不受影响 |

## 六、实施边界（防与并行工作冲突）

- 只改：`internal/server/handler.go`、`internal/server/chatrun.go`、`internal/server/logging.go`（如涉及）、
  `internal/server/anthropic.go`、`internal/pool/`（新增入口）、`internal/reqlog/`（新字段）、以及对应测试。
- **不碰** `internal/panel/**`（并行会话在改）、`README.md`、`HANDOFF.md`（并行会话在改）。
- 依赖方向：新增 `pool → unknown`？**已存在**（白名单里 `pool → auth, evidence, logfmt, unknown`），
  改动后必须仍 `check-boundaries.ps1` 退出 0。

## 七、P3/P4 的处置（复核后补做）

| # | 项 | 状态 | 说明 |
|---|---|---|---|
| P3 | `internal/server/anthropic.go` 的 `"stream":"true"`（字符串）类型错被吞 | **已实施** | 只改可观测性、**不改分支行为**（改分支是行为变更，须单独 PRD + 冒烟）：抽出纯函数 `anthropicStreamIntent` 返回 `(stream, note)`，未知时打 WARN |
| P4 | `cmd/login/main.go` 的 4xx 全归「登录未完成」 | **已实施** | 抽出纯函数 `pollFailureMessage`：401/403 归「被拒」、404 归「端点/realm 不匹配」、5xx/连不上归上游故障、200+业务码非 0 保留等待态并带出上游 code/msg。这是刻意的 **CLI 口径变更**；既有测试的分档注释已预告该变更方向，本次按指引把断言翻正 |

> 两者起初被判为「不属于计量口径」而留在本轮之外（当时的判断是：P3 改分支属行为变更、
> P4 是 CLI 文案）。复核后修正为：P3 **可以不改行为**、P4 与 P1/P2 同属「未知不得冒充某个具体结论」，
> 且既有测试的注释已为 P4 预留了翻正路径，故一并实施。
> 记录与反向变异证据见 `.harness/changes/2026-10-16-t7-unknown-admission/summary.md` §五–§六。
