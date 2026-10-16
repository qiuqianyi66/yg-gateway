# 变更总结 — T3 变异核验（upstream 4/5、panel 5/5）+ 补牙

> 承接 `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3 的 T3：这是交接文档点名的
> **唯一真漏洞**——`upstream` 89.5% / `panel` 95.5% 的覆盖率此前**没有任何独立证据**证明「有牙齿」。
>
> 核验方式：在 `%TEMP%` 的**一次性副本**里改生产代码（每包 5 条语义变异），看包内测试是否变红。
> 原件全程只读。配方与踩坑规避见 HANDOFF §3 T3 与 `.harness/LESSONS.md` L-2。

## 一、结论（先看这个）

| 包 | CAUGHT | 结论 |
|---|---|---|
| `internal/upstream` | **4/5** | 有牙齿；唯一存活者 M2 是**真实断言缺口**，已补牙并复验（见 §三） |
| `internal/panel` | **5/5** | 有牙齿，无存活者、无缺口 |

## 二、变异表

### upstream（基线 `ok yg-gateway/internal/upstream`，单轮约 7s）

| # | 位置 | 变异（改了什么行为） | 结果 | 捕获它的测试 |
|---|---|---|---|---|
| M1 | `headers.go` `defaultWorkBuddyUAFor` | 三段式 UA 去掉 `CLI/` 段标记（`" CLI/"` → `" "`） | **CAUGHT** | `TestUpstreamParseDefaultWorkBuddyUA`、`TestUserAgentDefaultWorkBuddyShape`、`TestUserAgentClientVersionOverride`、`TestUserAgentDefaultEmptyKeepsClientUA/chat`、`TestDesktopUAForGlobalAccount` |
| M2 | `client.go` `billingMeterPaths` | global 分支候选顺序反转（`[无/v2, 有/v2]` → `[有/v2, 无/v2]`） | **SURVIVED** → 已补牙（§三） | 变异态：0 条既有测试捕获 |
| M3 | `model_catalog.go` `ContextWindowListingV4` | 交换第 2/3 级 lookup：让运行时可写的 `model.json` 压过编译期静态种子表 | **CAUGHT** | `TestUpstreamParseContextWindowListingV4/静态种子表压过_model.json（第_2_级_>_第_3_级）` |
| M4 | `payload.go` `normalizeReasoningEffort` | 降级桶边界 `idx <= reqIdx` → `idx < reqIdx`（请求档落到相邻更低档） | **CAUGHT** | `TestPrepareBodyOptWithEfforts/supported_effort_passes_through_unchanged`、`TestInjectThinkingDefaultEffort` |
| M5 | `sse.go` `writeFrame` | 错误帧 envelope 识别字段名 `"error"` → `"err"`（原生 error 帧不再原样透传） | **CAUGHT** | `TestStreamHintErrorFrameObserver` |

### panel（基线 `ok yg-gateway/internal/panel 40.5s`；含 0–10s 随机抖动，属预期）

| # | 位置 | 变异 | 结果 | 捕获它的测试 |
|---|---|---|---|---|
| M1 | `autotask.go` `runAutoAll` | 自动领奖的 mp/web 域选反（`isMPTaskCode` 取反） | **CAUGHT** | `TestPanelAutoTaskRunAutoAllAutoClaimsWeb`、`...AutoClaimsMP` |
| M2 | `panel.go` `tryLockAccount`/`unlockAccount` | 3 处锁键 `p.taskLocks[uid]` → `p.taskLocks["__global__"]`（跨账号共用一把锁） | **CAUGHT** | `TestTaskAccountLockDifferentAccountsIndependent`、`TestPanelAutoTaskRunQueueItemsTwoAccounts` |
| M3 | `autotask.go` `runAutoAll` | 跳过判定取反（达标/已领的反而不跳过） | **CAUGHT** | 8 条（含 `RunAutoAllSkipsTerminalAndAbsent`、`ClaimMessages/*`） |
| M4 | `tasks.go` `taskAcceptAll` | 分片边界 `batch = 20` → `19` | **CAUGHT** | `TestPanelHandlersTaskAcceptAll`（断言 `[20,5]` 分片） |
| M5 | `taskcenter.go` `runGrowthQueued` | 两条终态早返回的判据互换（「无此任务」↔「已完成（已领取）」） | **CAUGHT** | `TestPanelAutoTaskRunGrowthQueuedBranches/*` |

## 三、唯一的缺口与补牙（M2）

**缺口**：`upstream` 包内 3 处涉及 `/billing/meter` 的测试**只断言 `/v2/...` 后缀**，即只覆盖 CN 单路径形态；
global realm 的候选序列、以及「无 /v2 优先、404 才回落」这条顺序语义**完全没有断言**。
所以把 global 的候选顺序反转，全包 5 条既有测试一起绿——而这在生产里意味着国际版账号每次都先打一个必然 404 的 CN 路径。

**补牙**：新增 `internal/upstream/meter_paths_pin_test.go`

- `TestBillingMeterPathsRealmOrderPin`：钉住 6 个行为——global 的 get-user-resource / daily-checkin 候选序列与顺序、
  CN 的单元素 `/v2` 现状（零回归）、以及 `GlobalEnabled=false` 逃生门下 global 账号回落 CN 单路径。
- `TestBillingMeterPathsCoverMeterFamily`：钉住「只有 `/billing/meter/*` 族走候选机制」的边界
  （`reportPath` 必须仍在 `/v2/` 单路径上），防止将来把候选机制扩散到 report/growth 端点。

**独立复验（本会话实测，不是自述）**：把整个仓库拷到 `%TEMP%\mut-pin-upstream`，先断言
`go env GOMOD` 指向副本，再施加 M2 变异：

```
pin-only : --- FAIL: TestBillingMeterPathsRealmOrderPin
           meter_paths_pin_test.go:93: 候选[0] = "/v2/billing/meter/get-user-resource"，
           want "/billing/meter/get-user-resource"（顺序反转会让国际版账号先打必然 404 的 CN 路径）
整包     : 唯一 FAIL 就是这条新测试（其余全绿）→ 还原 > hash 一致 > 整包回到 ok 7.0s
```

即：**M2 由「0 条测试捕获」变为「被精确点名」**。副本已删除，原件 `internal/upstream/` 只多了这一个测试文件。

## 四、核验过程的自证（防止「假全存活」）

- **goMOD 断言**：两个包都在跑测试前断言 `go env GOMOD` == `$dst\go.mod`（见 LESSONS L-2：
  cwd 落在原件会得到「5/5 全存活」的假结果）。两次核验均通过。
- **哈希闭环**：每条变异都走「记 SHA256 → 备份 → 改 → 测试 → 还原 → 复核 SHA256」；
  两包各 5 条 `restoredHashOk` 全为 true，还原后与原件逐文件比对 DIFF=0 / MISSING=0。
- **噪声排除**：10 条变异的失败信息全部与被改行为语义直接对应（UA 段形状、域、分片、文案、锁互斥），
  无「负载导致的超时/时间敏感 flake」形态，因此未触发「重跑一次」条款。
- **原件未被污染**：两包核验后 `git status --porcelain` 与开工时一致（并行会话的 `panel/*.go`、
  `README.md`、`HANDOFF.md` 改动未触碰）。
- **副本保真度的一处偏差（如实记录）**：`upstream` 副本里 `.harness/LESSONS.md` 缺失——它的落盘时间与本轮
  robocopy 几乎同时（本会话刚创建）。该文件不参与任何 Go 测试，对结论无影响。
- **panel 副本含并行会话的未提交 WIP**（`panel.go`/`app.js` 等）：结论的基线是「当时的工作区快照」，
  而非某个提交——这一点必须在引用 panel 的 5/5 时说明。

## 五、口径说明（避免误读）

- 变异核验**不在提交门禁内**（改生产代码 + 跑全量 + 还原，超出提交前时间预算），
  它是**一次性核验手段**：结论是「这些行为已被钉住」，不是「以后自动守住」。
- schema 里的 `classification` 只对**存活者**有意义（gap/equivalent/unreachable/unknown）。
  本轮 9 条被捕获的变异在该字段无适用值，报表里填了 `unknown` 并注明 N/A，别读成「原因不明」。
- 未单独做的子方向：`headers.go` 的「会话头族去必需头」（5 条配额已用满）。建议后续补测位置：
  `injectConversationHeaders` 里自称「必发」的 `X-Conversation-Request-ID`。

## 六、怎么回滚

`git revert <sha>`：删除 `internal/upstream/meter_paths_pin_test.go` 即可（纯增量测试，无生产代码改动）。
回滚后 M2 类顺序变异重新变成「零测试捕获」的静默缺口——**不建议**，除非该候选机制本身被移除。

## 七、遗留（已知、未做）

1. `upstream` 的 M2 只钉了**函数级候选序列**；「实际出站首个请求 URL」未用 httptest 钉（会引入网络时序依赖）。
   若要更强的端到端钉法，位置：global 账号 + `BillingBaseGlobal` 指向 httptest server，断言首请求 `RawPath`。
2. `panel` 的变异核验只做了 5 条（配额），`autotask` 的 20 分片/`taskcenter` 的其它终态分支未逐条覆盖。
3. 本轮的 10 条变异**不代表**这两个包已被穷尽核验；只代表「这 10 个行为已被钉住」。
