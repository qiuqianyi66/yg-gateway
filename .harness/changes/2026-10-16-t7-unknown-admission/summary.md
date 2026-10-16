# 变更总结 — T7：unknown 不得冒充 0（写台账/统计的准入）

> 承接 `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3 的 T7（用户决策 **D3 = 改**）。
> 需求与验收：`docs/prd/REQ-usage-unknown-admission-2026-10-16.md`。
> **P1–P4 全部实施并验证**（P3/P4 见 §五，是复核后补做的）。

## 一、改了什么

核心问题：**解析失败被迫用零值兜底时，`0` 会被当成观察值写进台账/统计**。
后果不报错：一个付费号可能被判成「免费号」被优先选号；缓存命中率被抬到 100%，
反而压掉了「缓存没生效」的告警。**未知与 0 必须在每一层可区分。**

| # | 文件 | 改动 |
|---|---|---|
| 1 | `internal/server/handler.go` `usageCreditTotal` | 三态化：`ok`（观察）/ `creditUnknown`（**有分母但 credit 不可解析 → 未知**）。旧实现 `c, _ := usage["credit"].(float64)` 的零值兜底被去掉 |
| 2 | `internal/server/chatrun.go` | 非流式：`creditUnknown` 时打 WARN（含脱敏 `logfmt.Label`）且**不调** `NoteModelCost`；miss 改用 `okM` 判定。流式：改用 `CacheTokens` 的四态返回，miss 未知时不写 0 |
| 3 | `internal/server/logging.go` | `CacheTokens()` 增第 3 个返回值 `missKnown`；`chatStats` 增 `cacheMissUnknown`；写 reqlog 时带上该标记 |
| 4 | `internal/reqlog/reqlog.go` | `Event` 增 `CacheMissUnknown bool \`json:"cache_miss_unknown,omitempty"\``（纯增量；观察到的 0 与历史行形态不变） |
| 5 | `internal/server/usage_unknown_test.go`（新） | 3 条测试 22 个子用例 |
| 6 | `internal/server/cache_stats_test.go` | 适配 `CacheTokens` 新签名（3 值 → 4 值） |

**未动 `internal/pool` 的 API**——设计偏差与理由见 PRD §三.0：`float64` 无法携带 provenance，
「未消解」本身就是 pool 侧已有的未知准入（不调 `NoteModelCost` 即不消解）。

## 二、怎么验证

### 1. 单元测试（新增，22 个子用例）

```
--- PASS: TestUsageCreditTotalUnknownIsNotZero       （8 例：字符串/null/缺失 → 未知；真 0 → 观察）
--- PASS: TestCacheTokensMissUnknownIsNotZero         （6 例：显式 miss / 推导 / 未知 / 自相矛盾）
--- PASS: TestReqlogCacheMissUnknownIsAdditive        （2 例：未知打标记且不打数字；观察 0 无标记）
--- PASS: TestStatsReaderCacheTokens                  （既有测试适配新签名后仍绿）
```

### 2. 反向变异（副本内，原件只读；先断言 `go env GOMOD` 指向副本，改前改后 SHA256 校验）

| # | 变异（把实现还原成旧行为） | 结果 | 证据 |
|---|---|---|---|
| M1 | `handler.go`：`if !okC {` → `if false && !okC {`（= 丢弃 ok，零值兜底回归） | **CAUGHT** | `usage_unknown_test.go:87: ok=true unknown=false，want ok=false unknown=true`；两条子用例红 |
| M2 | `logging.go`：`return hit, 0, false, true` → `return hit, 0, true, true`（= miss 未知当 0） | **CAUGHT** | `usage_unknown_test.go:166: ok=true missKnown=true，want ok=true missKnown=false`；两条子用例红 |

两条变异还原后哈希一致，副本已删除，原件工作区未被污染。

## 三、防过度纠正（本变更最容易做错的地方）

**真免费模型（上游明确回 `credit=0`）必须仍按观察记账**。这不是附注，是硬验收
（PRD A2）：如果为了挡未知而把所有 `credit=0` 都判成 unknown，就会丢掉「这个模型免费」这个
**真实且有用**的观测，`credit_floor` 的免费放行逻辑会失去依据。
所以判据只挂在**类型断言的 ok**（能否解析）上，不挂在数值上——`TestUsageCreditTotalUnknownIsNotZero`
的第 4 例（真 0 → 观察）就是这条的钉子。

## 四、怎么回滚

`git revert <sha>`。各改动彼此独立：

- 只回滚 `usageCreditTotal` → 恢复到旧口径（未知冒充 0）；
- 只回滚 `CacheTokens`/`reqlog` → miss 未知重新记为 0，但信用（credit）侧仍正确；
- `reqlog.CacheMissUnknown` 是纯增量字段，旧消费者不受影响，可单独保留；
- P3/P4 的回滚见 §六（P4 会把 `cmd/login` 的提示退回旧分档）。

## 五、补做：P3 与 P4（同一条纪律的两个远端）

原计划把 P3/P4 留在本轮之外，复核后判断它们就是同一条纪律（**未知不得冒充某个具体结论**）
的另外两个落点，且都能在不改行为的前提下落地（P4 本身是口径变更），于是补做。

| # | 改动 | 行为是否变化 |
|---|---|---|
| P3 | `internal/server/anthropic.go`：把 `_ = json.Unmarshal(body, &peek)` 拆成纯函数 `anthropicStreamIntent(body) (stream bool, note string)`；`"stream":"true"`（字符串）/ 非法 JSON → **未知**：行为保持非流式，但打 WARN 留痕 | **否**（分支选择一字未改——改它属行为变更，须单独 PRD + 冒烟） |
| P4 | `cmd/login/main.go`：`runPoll` 的 4xx 分档拆成纯函数 `pollFailureMessage(status, err) string`；401/403 → 「登录被上游拒绝…继续等待不会成功」；404 → 「端点不存在，查 --realm/域」；5xx/连不上 → 上游故障档；200+业务码非 0 → 等待态并带出上游 code/msg | **是（这就是 P4 的目的）**：CLI 提示分档变化 |

### P4 顺带发现的一处既有约束（已遵守，并写进代码注释）

既有测试对 4xx 有一条**负断言**：不得回显 `http_error: upstream 401` 原文（500 档则要求回显）。
因此 401/403/404 的新文案**只报状态码、不回显上游错误原文**：状态码是用户能行动的信息
（重来 / 查配置），原文只会让人误判成「上游故障」。这条约束写进 `pollFailureMessage` 的注释，
避免后来者「顺手」把原文加回去。

另：该测试原有注释已写明「401 归未完成是现有产品意图，**待确认**；若产品想让 401 走上游错误档，
应改生产代码并把本用例改成正断言」——本次决策正是那次变更，故按注释指引把两条用例**翻成正断言**，
并补 404 一条（它与 401/403 的失效原因不同）。

### 验收（实测）

```
internal/server: TestAnthropicStreamIntentUnknownIsNotFalse   （6 例：布尔/缺失/字符串/数字/非法 JSON）
cmd/login:       TestPollFailureMessageDistinguishesRejectionFromWaiting（7 例：401/403/404/500/0/200/400）
cmd/login:       TestCovLoginRunPollFatalBranches             （既有用例翻正后全绿）
```

**反向变异（副本内，原件只读，先断言 GOMOD 指向副本）**：把 `case status == 401 || 403:` 改成
`case false:`（= 回到「4xx 全归未完成」的旧档）→ 两组用例同时红（既有用例的 401/403 子用例 + 新用例），
还原后哈希一致 → **CAUGHT**。

P3 的「行为保持不变」由测试正向钉住：`stream="true"` 用例同时断言 `stream == false`（分支不变）
**且** note 非空（未知留痕）——两条同时成立才算过。

## 六、P3/P4 的回滚

`git revert <sha>`（涉及 `internal/server/anthropic.go`、`cmd/login/main.go`、
`internal/server/anthropic_stream_intent_test.go`、`cmd/login/poll_message_test.go`、
`cmd/login/coverage_test.go`）。P4 是**刻意的 CLI 口径变更**，回滚它会同时把
`TestCovLoginRunPollFatalBranches` 的两条用例退回旧断言。

## 七、遗留（已知、未做）

1. **`unknowns_open` 可能长期非零**：若上游普遍不回 `credit`，该指标会持续 >0。按 ADR-007 这是
   **信号**（要么池子在扩，要么 `requires` 设计有问题），不是故障——观测时要这样解读。
2. **面板侧未验**：unknown 的展示由并行会话在 `internal/panel/*` 推进，本变更只保证 unknown
   进入既有登记链路；**未在面板上做过端到端观感验证**（避免与并行会话冲突）。
3. **`cacheMissWarn` 的行为变化（刻意）**：miss 未知时该次不再喂入命中率统计，因此这类请求
   既不会抬高命中率、也不会触发低命中率告警——即「因为不知道，所以不下结论」。
4. **P4 的提示文案未在真机跑过**：单测覆盖了分档与文案，但没有用真实上游 401 走一遍 CLI
   （需要一个被拒的账号，属真机操作）。
