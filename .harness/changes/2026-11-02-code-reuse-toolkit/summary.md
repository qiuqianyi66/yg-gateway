# 代码复用体系（internal/pkg 原语层 + 提示词模板库）

- 日期：2026-11-02
- 流程级别：L2（新增架构层 + 依赖白名单变更）
- PRD：`docs/prd/REQ-yg-gateway-code-reuse-2026-11-02.md`
- 来源：交接摘要「yg-gateway 代码复用体系建设」

## 工作区边界（先看这条，否则会误读 diff）

本工作区**同时装了两个 feature**，本记录只覆盖前者：

1. **本记录**：`internal/pkg/{timex,jsonsafe}` + 10 包迁移 + `check-boundaries` 白名单 +
   `AGENTS.md` 数字 + `.harness/prompts/`，外加两处为解棘轮回退的机械拆分
   （`cmd/server/config.go`、`chatrun.go:handleTransportError`）与复审驱动的红线修复。
2. **上一轮（未提交）**：`2026-11-02-ban-risk-reduction` —— per-account proxy、
   `X-Session-ID` 改进程随机、`MinRequestInterval`、`isKnownModel`/11102 白名单、
   回归脚本改造、未跟踪的 `scripts/yg-gateway-watch-hidden.vbs`、`go.mod` 新增 `golang.org/x/net`。

后果与建议：①`summary` 里"单提交可整体 revert"只对第 1 组成立；②`deploy-prod.ps1 -Build`
遇脏树会拒绝（AGENTS.md 部署纪律），两个 feature 需**拆成两个提交**才能用提交号追溯地部署；
③我不代为拆分第 2 组（L-21：不碰外部落入工作树的改动），但复审发现的两条红线/语义问题
已在第 2 组代码里修掉并单独标注。


## 结论先说：交接摘要里的 4 个包，实测只有 2 个站得住

| 候选 | 结论 | 证据 |
|---|---|---|
| `internal/pkg/timex` | **建了** | `sleepCtx` 在 server/scheduler 各一份（backoff.go 注释自认"消费侧实现"）；`"2006-01-02"` 字面量散落 10 包 15 处 |
| `internal/pkg/jsonsafe` | **建了** | 25+ 处 `x, _ := m["k"].(T)`；已发生过一次生产语义事故（credit 未知被当免费） |
| `internal/pkg/httpx` | **不做** | Retry-After 已单源（`upstream/errors.go:394`）；退避/抖动已单源（`server/backoff.go`）；5 处 `&http.Client{}` 的 Timeout/Jar/重定向**本来就该不同** |
| `internal/pkg/errx` | **不做** | 错误词汇已被 `upstream.ErrKind` + `Classify/ClassifyError/inferEvidenceLevel` 独占；再建一套与四态纪律冲突 |

两个"不做"的重评条件写在 PRD 阶段 0 结论表里。摘要另有 2 处数字漂移（`internal` 实测 21 包非 20；
`internal/prompt` 已存在），已在 PRD「问 3」记录。

## 改了什么

### 1. 新增 `internal/pkg/timex`（纯叶子）

- `SleepCtx`：消掉 `server/backoff.go` 与 `scheduler/scheduler.go` 的两份等价实现。
- `DateLayout` / `DateTimeLayout` / `HourLayout` + `Date/DateTime/Hour/HourStart`。
- 迁移消费方（10 个包）：`server`(SleepCtx)、`scheduler`(SleepCtx+Date×3)、`usage`(Hour/HourStart/Date)、
  `reqlog`(Date×7)、`pool`(Date×2)、`growth`(Date)、`bench`(Date)、`upstream`(两个布局常量改为引用)、
  `cmd/credit`(DateTime×2)、`cmd/harnessrun`(Date×2)。
- 迁移的测试：scheduler 里两条 `sleepCtx` 行为用例**随实现搬到** `timex_test.go`（并补了"等待中途取消"
  一条），不留孤儿测试。

### 2. 新增 `internal/pkg/jsonsafe`（纯叶子）

- `Map/Slice/String/Bool/Float/Int`，全部返回 `(值, ok)`：**缺失或类型不符一律 `ok=false`，不返回零值**。
- `Float` **严格**只认 `float64`——这是刻意的：`usageCreditTotal` 依赖"字符串数字 → 未知"这一行为，
  放宽会把"上游给了怪形态"从 unknown 变成已知值，直接改成本台账的三态语义。
- `Int` 接受 float64/float32/int/int64/json.Number，**逐 case 与** `logging.go` 原来的 `read()` 闭包一致。
- 迁移消费方：`server/helpers.go`（`usageCreditTotal`，事故点）、`server/logging.go`（
  `usageDeltaFromResponse` 去掉 20 行闭包、`completionTokens`）。

### 3. `.harness/prompts/` 提示词模板库

`README.md`（索引）+ `feat-dev.md` / `code-review.md` / `debug-triage.md` / `perf-optimize.md`。
口径全部贴本仓：阶段 0 四问、L0-L3 分诊、6 维评审 + 🔴🟡🟢🔵 分级、独立复审纪律（L-16）、
四态标注、`check-depth`/`check-context-budget` 棘轮、"测试要有牙齿"。不写通用套话。

### 4. 白名单与治理数字同步

- `scripts/check-boundaries.ps1`：新增 `pkg` 层（纯叶子）+ 8 个消费方条目，每条写了"为什么这一层需要它"。
- `AGENTS.md`：源包条目 `20 → 23`；`go test` 包数 `25 包(22 ok + 3 no test) → 30 包(25 ok + 5 no test)`。
  两处均为**实跑计数**，非估算。

### 5. 附带修复（**非本任务引入**，为解棘轮回退）

`check-depth.ps1` 报 `oversizeFiles 2→3`、`oversizeFuncs 7→8`。在 HEAD 的临时 worktree 复算
（`git worktree add --detach %TEMP%\yg-head-probe HEAD`）得到 HEAD = 2/7，故回退由**上一轮未提交改动**
引入（`cmd/server/config.go` 涨到 1038 行、`chatrun.go:handleErrorPath` 涨到 121 行代码）。按纪律
"严禁用 -Update 掩盖回退"，直接修因：

- `cmd/server/config.go`：`normalizePrompt` / `validateScheduleHours` / `checkHourRange` 整块搬到
  `cmd/server/config_prompt.go`（同包、零行为变化）→ 1038 → ~980 行。
- `internal/server/chatrun.go`：传输层错误两分支抽成 `handleTransportError`（注释与副作用原样保留）
  → 121 → ~91 行代码。

## 6. 第二轮：把「复用」做到全项目（2026-11-02 续）

用户要求"整个项目都做代码复用"。按证据做了三件事，**否掉了两件**：

### 6.1 R8 协议常量单源化（真重复，且压在红线上）

机械扫描"跨包重复 6 行代码块"共 115 组，人工核对后确认两组是真重复，其余是**同包并行实现**
（如 `session/ids.go` 与 `session/session.go` 的字节路径/视图路径，注释写明"逐位一致"）
或**故意不同**（`upstream.ExtractClientIP` 无条件取首段 vs `server.extractClientIP` 按受信任代理判定）。

真重复的一处是 R8 数据：

| 常量 | 原先几份 | 现在 |
|---|---|---|
| `CLI/2.63.2 CodeBuddy/2.63.2` | **3 份**（`upstream/models_fetch.go`、`panel/login.go`、`cmd/login/main.go`） | 1 份（`upstream/protocol.go`）+ 别名 |
| `https://www.codebuddy.cn` / `www.workbuddy.ai`（Origin/Referer） | 2 份 + upstream 内部 1 份 | 1 份 |
| `https://copilot.tencent.com` / `www.workbuddy.ai`（登录基址） | 2 份 | 1 份 |
| 计费域基址 | 2 份（`cmd/credit` 与 `upstream/client.go` 缺省值） | 1 份 |

**值一字未动**（R8 要求的是"不改名"，不是"不许重构"），并新增 `TestProtocolConstantsR8`
把字面值钉死——R8 此前只有人读评审，现在有机器检查：改 UA/域名/Origin 立刻红。

### 6.2 代码块库 `.harness/snippets/`

4 块 + 索引：jsonsafe 取值 / timex 等待与日期 / R8 协议常量 / JSON 响应写出。
每块都带**判据与反例**，尤其 `jsonsafe-取值.md` 写明"什么时候 `, _ :=` 是合法的"——
防止下一轮有人照着"25+ 处不安全断言"再去机械全量迁移。

### 6.3 前端组件库：地基 + 试点已落地，按计划继续

`app.js` 4317 行是拆分对象，实测出真成本：测试里有 **26 处**硬读 `"app.js"`、
**28 处**按 `src.indexOf('function xxx')` 切片取源码（函数换文件后 `indexOf` 返回 -1 →
**静默错测**）。所以先改测试地基，再拆代码：

- **第 1 步（完成）**：新增 `frontend_bundle_test.go` 三个 helper（`scriptFiles` 解析
  index.html 的加载顺序 / `scriptBundle` 拼接 / `scriptBundlePath` 供 node harness），
  替换 26 处硬读与 4 处 `os.ReadFile("app.js")`，语法校验改成逐文件。
  **副作用收益**：顶层冒烟现在执行全部 7 个脚本（此前只跑 app.js），首次就抓到 DOM 桩
  缺 `MutationObserver`——已补三类 Observer 惰性桩。
- **第 2 步（完成）**：拆出 `app_qr.js`（QR 编码器，纯函数零 DOM，144 非空行），
  `app.js` 4317 → **4184** 非空行。配套 embed/handler/路由/script 标签 + 三条牙齿测试
  （路由级服务、嵌入非空且 app.js 不许留副本、**金样数字**钉住 21×21/236 等输出）。

- **第 3 步（完成）**：拆出 `app_core.js`（18 个纯函数：`esc/ago/dur/parseAPITime/fmtLocalDateTime/
  dtLocalValue/dtLocalParse/formatTokenCount/formatLatency/formatRate/fmtTok/fmtMs/fmtRate/
  trimFixed/fmtCredit/fmtCreditRatio/fmtModelRate/csvCell`，129 非空行），**排在 app.js 之前加载**。
  `app.js` 4184 → **4068** 非空行（累计 4317 → 4068，−249）。
  三条新测试：`TestAppCoreLoadsBeforeApp`（顺序钉死——排反了面板白屏，Go 侧看不见）、
  `TestAppCoreScriptServing`（走完整路由）、`TestAppCoreHasNoDuplicateInApp`（工具函数只许一份 +
  纯函数文件禁止出现 `document./fetch(/localStorage.`）。

  **第 3 步暴露的真问题（已修，记在这里避免下一轮重踩）**：切片类测试用
  `src.indexOf('function A')`…`src.indexOf('function B')` 取源码执行。A、B 分处两个文件后，
  切片会**跨文件**把 app.js 的顶层语句（`let theme = localStorage.getItem(...)`）一起卷进来，
  在裸 node 上下文里直接 `ReferenceError`——症状看起来像"面板代码坏了"，实际是取源方式坏了
  （5 条测试同时红）。修法：新增 `funcsOnlyBundlePath`，给切片类测试喂"只含顶层函数声明"的
  源码，切片跨文件也不会带进顶层副作用语句。

- **第 4 步（完成，7/7 视图）**：依次拆出 `app_views_models`(97) / `app_views_packages`(374) /
  `app_views_keys`(127) / `app_views_usage`(493) / `app_views_accounts`(374) / `app_views_config`(98) /
  `app_views_tasks`(276) / `app_views_logs`(687)。**`app.js` 4317 → 1622 非空行（−62%）**。

  三条规则型测试全程守着：`TestComponentLoadOrder`（`app_core.js` 与所有 `app_views_*.js` 必须在
  app.js 之前，**新增组件自动被覆盖**）、`TestViewsComponentEmbedded`（表驱动，函数在组件里且 app.js
  不许留副本）、`TestViewsComponentsAreFunctionOnly`（**白名单判据**：视图组件只许有声明）。

  **第 4 步的三个坑（都已修 + 已机器化）**：
  1. 视图区块里混着**顶层事件绑定**（`$('x').onclick`/`addEventListener`/顶层 `for` 循环）——
     必须留在 app.js，否则 `$` 还在 TDZ 时执行 → 白屏。
  2. 切片类 harness 跨文件后连**数据常量**（`PK_DEFAULT_DETAIL_LIMIT`/`PK_ACCOUNT_COLORS`/
     `PK_DAY_MS`/`CFG_MAP`/`CLEARABLE_CFG`）都拿不到 → 新增 `funcsOnlyWithConstsPath`（常量前置），
     并把两条按位置切片的 harness 改成**直接执行整份源码**。
  3. 最初的"黑名单"式规则测试（禁 `document.`/`$(` 等关键字）漏掉了顶层 `for` 循环与顶层
     `bindRetry($(...))` 调用——已改成**白名单**（只允许 `function/const/let/var/注释`），
     这才是"视图组件只放声明"的真正判据。

剩余：第 5 步收紧棘轮（待 `cmd/server` 覆盖率补回后再 `-Update`，否则会连带放宽 minCoverage）。

### 6.4 这一轮**否掉**的两件事（都有实测证据）

| 候选 | 结论 | 证据 |
|---|---|---|
| `jsonsafe` 全量迁移 62 处 | **不做** | 逐处读过：绝大多数是 `x, _ := m["k"].(T)` 紧跟 `!= ""`/`!= nil` 守卫，**已经等价于 ok 检查**（如 `payload.go:104/184/217`、`sse.go` 16 处）。机械替换 = 触碰最热 SSE 路径、零语义收益、巨大回归面 |
| `internal/pkg/httpx` | **仍不做** | 唯一字节级相同的是 `writeJSON`（panel/server 各 6 行），但两处**错误体形状本来就不同**（面板内部约定 vs OpenAI wire 契约），合并反而错。重评条件写进 `.harness/snippets/json-响应写出.md` |

## 7. 独立复审驱动的修复（复审记录见 `review.md`）
复审（子代理，只读）抓到的、已在本轮修掉的：

| 级别 | 问题 | 修法 |
|---|---|---|
| 🔴 R1/R2 | `upstream/transport.go:111/128/136` 把**含 `user:pass@` 的完整 proxyURL** 打进 WARN 日志 | 新增 `redactProxyURL`（`url.User=nil`；解析失败且含 `@` 时整体隐去）+ 三处改用它 + `TestRedactProxyURL` |
| 🟡 语义 | `chatrun.go` 用 `!isKnownModel(...)` 当否定判据：模型目录为空（冷启动/拉取失败）= unknown，却被当成"模型不存在"，把真实模型直接 400（换号本可成功），与函数自己注释的意图相反 | 新增 `modelCatalogVerdict` 三值（known / absent / catalogEmpty）+ 调用点改为 `!known && !catalogEmpty` 才 fail-fast + `TestModelCatalogVerdictTriState` |
| 🟢 | `rotateBackoff` 外层重复 `d<=0` 守卫（把刚消掉的复制写回来） | 删守卫，直接 `timex.SleepCtx(ctx, backoffAfter(i))` |
| 🟢 | `archiveDay` 用 `len(timex.DateLayout)` 当文件名日期宽度（布局改非定宽会静默切错） | 改 `const archiveDateLen = 10`，注释说明是文件名契约而非布局长度 |
| 🔵 | 局部变量 `auth` 遮蔽 `internal/auth` 包名 | 改名 `socksAuth` |

复审报的 `gofmt` 一条**经复核驳回**：它是基于我 `gofmt -w` 之前的树下的结论，当前
`gofmt -l ./cmd ./internal` 无输出、`pre-commit` 检查 1（45 个目标）通过。
——复审结论同样是二手证据，必须回到当前树复核（L-22）。

## 怎么验证的（原始输出）

```
go build ./...                                  → 无输出（成功）
go vet ./...                                    → 无输出
gofmt -l ./cmd ./internal                       → 无输出
go test ./...                                   → ok=25  no_test_files=5  fail=0  total=30
check-boundaries.ps1                            → ✅ 边界检查通过；EXIT=0
check-boundaries.ps1 -Fix                       → pkg 消费方实测：bench/growth/pool/reqlog/scheduler/server/upstream/usage/cmd
check-depth.ps1                                 → >1000 行文件 2 / >120 行函数 7（回到基线）；EXIT=0
pre-commit.ps1                                  → 11/11 全过，EXIT=0
```

门禁第一次跑挂了 2 项，都已修（**记录在此，避免下一轮当成"一次就绿"**）：

1. `gofmt 漂移 1 个文件` → `cmd/server/config.go`（拆函数后留下空行），`gofmt -w` 修好。
2. `阶段 0 必要性段缺失` → 报的是**上一轮的**
   `docs/prd/REQ-yg-gateway-ban-risk-reduction-2026-11-02.md`（不是本次新增）。
   已按四问补 `## 必要性` 段：事实取自该文档自己的「触发原因」与当日观测，
   未观测项（上游风控是否仍按 IP/会话关联）标 **unknown + 重评条件**，未编造动机。

复审修复后门禁又抓到 1 项（R10），也记下来：

3. `internal/server/models_handler.go` 的 `known, _ := h.modelCatalogVerdict(...)` 被判
   「静默忽略」——它丢的其实不是 error，但规则就是规则。**没有加豁免注释绕过**，
   而是把两返回值改成三值枚举 `modelCatalogVerdict`（`modelCatalogUnknown/Absent/Known`）：
   `isKnownModel` 变成 `== modelCatalogKnown`，调用方变成 `== modelCatalogAbsent`，
   结构上不再需要丢弃任何返回值。

`check-depth.ps1` 提示两项指标变好（`maxFileLines 1089→1070`、`maxFrontendLines 4378→4317`），
但**没有**执行 `-Update`：`-Update` 会把 `minCoverage 73.3 → 72.9` 一并写进基线，
那等于用棘轮掩盖覆盖率回退（`cmd/server` 由上一轮新增代码拉低，本任务未触碰该包的覆盖率）。

## 未完成 / 需要下一轮的事

| 项 | 状态 | 说明 |
|---|---|---|
| R8 协议常量单源化 + 金样测试 | **已完成（第二轮）** | 5 类常量、最多 3 份副本 → 1 份；`TestProtocolConstantsR8` 钉死字面值 |
| `.harness/snippets/` 代码块库 | **已完成（第二轮）** | 4 块 + 索引，含"什么时候**不**该用"的判据 |
| `jsonsafe` 全量迁移（62 处） | **结论修正：不做** | 逐处读过：绝大多数紧跟守卫，已等价于 ok 检查；改口径为"新增必用、存量改到哪迁哪" |
| 前端组件库（`app.js` 4317 行 → 组件） | **计划已定，待执行** | 前置约束已测绘（29 处测试硬读 `app.js`、≥6 处 `indexOf` 切片）；执行顺序见 [frontend-split-plan.md](frontend-split-plan.md) |
| `cmd/server` 覆盖率 73.3% → 72.9% | **已完成（2026-11-03）** | 补 9 条归一分支 → **74.0%**；棘轮同步收紧，见下节 |
| 11102 三值判定**调用分支**的端到端测试 | **待补** | 三值契约有测试（有牙齿），但「目录为空 + 11102 → 仍换号」这条路径没覆盖 |
| 代理 fail-open 直连（配错时反而用共享 IP 出去） | **待 owner 决策** | 改 fail-closed 会改可用性语义；建议至少让 `/status` 能看出「代理未生效」 |
| `chatClientFor` 重复 `chatHTTP()` / 每次 11102 复制两次快照 / `sessionID` 每请求新 ID | **未修（属 ban-risk feature）** | 已在 `review.md` 登记 |
| `internal/pkg/httpx`、`errx` | **不做** | `httpx` 重评后仍不做（`writeJSON` 错误体形状不同）；重评条件见 snippets |

## 怎么回滚

纯新增 + 机械迁移，单提交可整体 revert：

```powershell
git revert <commit>            # 或 git checkout -- <改动文件>
```

回滚后需同步：`scripts/check-boundaries.ps1` 的白名单（`pkg` 条目留着无害——未消费的层不会误判）、
`AGENTS.md` 的两处数字。数据/凭证/配置**零影响**（未动 wire 形状、未动 `auths/`、未动 `config.json`）。

---

## 第三轮（2026-11-03）：覆盖率回补 + 深度棘轮收紧

### 为什么必须先补测再收紧

`scripts/check-depth.ps1` 的 `minCoverage` 取的是**各包最小值**（`Measure-Object -Minimum`，
0% 的 no-test 包被豁免），带 `0.5` 容差；`-Update` 写的是 `$current` **全量**。
所以 72.9 对基线 73.3 只是「擦边未判回退」（< 72.8 才拦），`-Update` 会**原样把 72.9 写进基线**
——棘轮被悄悄放松。正确顺序：先把覆盖率补回基线以上，再 `-Update`。

### 补了什么

新增 `cmd/server/ratelimit_config_test.go`（5 组用例，覆盖此前只有「正常值」路径的分支）：

| 分支 | 位置 | 为什么值得钉 |
|---|---|---|
| `min_request_interval` / `_global` 非法时长报错 | `config.go:905` / `:914` | 不报错则非法值被 `ParseDuration` 静默吞成 0 = 特性悄悄关闭（R10） |
| `min_request_interval` / `_global` 负值钳 0 | `:908` / `:917` | 配置层**出口口径**：导出字段不得带负值（不是唯一防线，见下） |
| `failure_window` 负值钳 0 | `:898` | 同上；负值语义上等于关闭 |
| `degrade_cooldown` / `_max` 空串回落默认 | `:806-811` | 空串无法 `ParseDuration`，不回落就 fail fast——「留空」从「用默认」变「启动崩溃」 |
| `Load` 的第二个 `normalize` 错误出口 | `:624-626` | env 覆盖发生在第一次归一之后，必须再归一一次 |
| `WriteDefault` 父目录创建失败 | `:673-674` | 父级被普通文件占住时必须报错，否则故障延迟到下次重启 |

覆盖率：`cmd/server` **72.9% → 74.0%**。

### 顺带清掉一处死代码

`config.go` 里 `degrade_cooldown` / `degrade_cooldown_max` 的空值回落写了**两遍**（原 `806-811`
与 `942-947`）。第二份恒不执行：

- 全仓对这两个字段的赋值只有 `Default():555`、`806-807`、`942-943` 三处；
- `806` 归一后字段不可能是 `""`，`806 → 942` 之间是直线（只有错误 `return`，无跳转）。

已删除后一份（行为等价），并在原处留注释指向唯一实现。附带效果：这 2 条不可达语句不再永久
压着覆盖率分母。

### 棘轮收紧结果（`-Update`）

```
+ maxFileLines        1089 → 1070
+ maxFrontendLines    4378 → 1842   （现最大前端文件是 index.html，不是 app.js）
+ minCoverage         73.3 → 74.0
```

三项**均为变好**，`oversizeFiles 2` / `oversizeFuncs 7` / `oversizeFrontendFiles 2` 不变，
**无一放松**。

### 诚实边界（别把下面几条读成「已覆盖」）

1. 仍有 **3 条不可测分支**：`crypto/rand.Read` 失败（`:661`）、`json.MarshalIndent` 失败
   （`:669`）、`f.Write` 失败（`:683`）。三者要测必须给 `WriteDefault` 注入 seam
   （随机源 / 序列化器 / 写入器），本轮**没做**——它们是 unknown，不是「已覆盖」。
2. 负值钳位的「牙齿」是**弱**的，测试注释已按实测改写：`pool.minIntervalBlocked` 有
   `interval <= 0 → return false`（`pick.go:99`），`failureWindowBlocked` 首位是
   `p.failureWindow > 0`（`pick.go:89`），`Pool.SetMinRequestInterval` 也自钳一次。
   所以删掉配置层钳位**不会**把选号挂死——钉的是出口口径，属纵深防御。
3. `minCoverage 74.0` 恰好等于当前实测值，`cmd/server` 是全仓新底线：**给它加生产代码而不加测试
   会直接撞门禁**（容差 0.5）。这是棘轮本意，但下一轮动 `cmd/server` 时会先看到这条。