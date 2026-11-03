# REQ-yg-gateway-code-reuse-2026-11-02 — 代码复用体系（internal/pkg 原语层 + 提示词模板库）

- 状态：已落地（阶段 4-6）
- 来源：交接摘要「yg-gateway 代码复用体系建设」
- 流程级别：L2（新增架构层 + 依赖白名单变更）→ 全流程，阶段 6 需独立复审

## 必要性

> 阶段 0 四问（`.harness/rules/开发流程规范.md` §零）。交接摘要只是上一轮的假设，
> 下面每条都回到仓库原始出处复核过；**摘要里 4 个候选包实测只有 2 个站得住**。

**问 1：要解决的真实问题是什么？**（引观测，非"清单上写着"）

| 观测 | 出处（实跑 `Select-String` 全仓统计） |
|---|---|
| `sleepCtx` 同函数两份拷贝，且代码注释自认是"被迫复制" | `internal/server/backoff.go:63`、`internal/scheduler/scheduler.go:456`；backoff.go 注释原文：「该函数未导出且 scheduler 不宜被 server 反向依赖，按任务书「等价物」口径在消费侧实现」 |
| 日期布局字面量 `"2006-01-02"` 在 10 个包出现 15 次；`"2006-01-02 15:04:05"` 出现 4 次（含 2 处裸字面量） | `bench.go:219`、`blackcat.go:146`、`cooldown.go:58`、`reqlog.go:429/455/628/631/671/674`、`trends.go:49`、`snapshot.go:52`、`streak.go:113`、`travel.go:41`、`state.go:670`；`billing.go:172`、`errors.go:266`、`main.go:149/150` |
| 不安全类型断言 `x, _ := m["k"].(T)` 全仓 25+ 处，**已造成生产语义错误** | `internal/server/helpers.go:71` 注释原文：旧实现 `c, _ := usage["credit"].(float64)` 用零值兜底，把「未知」当「免费」写进成本台账 → 付费号被判成免费号优先选号 |
| 嵌套 `x, ok := m["k"].(map[string]any)` 30+ 处，逐处手写 | `responses_out.go`(6)、`sse.go`(6)、`payload.go`(3)、`errors.go`(2)、`logging.go`(2) 等 |

**问 2：不做会怎样？**

- `sleepCtx` 双份：改动（如加 jitter、改停止语义）要同步两处，漏一处即"两个包等待语义不同"，
  且不报错——属 AGENTS.md「不报错但会伪造结论」那一类。
- 布局字面量散落：改日期口径（如统一本地时区/CST）需逐个翻 15 处，漏一处即面板与台账对不上。
- 不安全断言：**已经发生过一次**（credit 未知被当 0），危害是选号决策被污染，且不报错。

**问 3：现状是否已变？**

- 已有叶子包先例：`internal/logfmt`（脱敏/截断）、`internal/httpauth`（常量时间比较）、
  `internal/evidence`、`internal/unknown` 都是「零 internal 依赖、被各层引用」的形态，
  `internal/pkg/` 沿用同一形态，不引入新范式。
- 交接摘要称「internal 下 20 个包」：**实测 21 个**（`alerting/auth/bench/evidence/growth/httpauth/
  livecfg/logfmt/metrics/opsagent/panel/pool/prompt/redisstore/reqlog/scheduler/server/session/
  unknown/upstream/usage`）。摘要的 `internal/prompt` 也未提及——它已存在且是对话提示词拼装，
  与本次「提示词模板库」不是一回事，不冲突。
- 摘要称「4 个工具包全做」：实测 **`httpx` 与 `errx` 无重复证据**（见下"不做"结论）。

**问 4：有没有更便宜的等效手段？**

- 只改注释/加 lint：改不动 `sleepCtx` 的两份实现（依赖方向硬约束），也拦不住新的不安全断言。
- 扩 `logfmt` 装时间/JSON：语义污染（logfmt = 日志脱敏），且会让 logfmt 从叶子变成"杂物间"。
- 结论：**建两个语义单一的叶子包**是最便宜的有效手段。

### 阶段 0 结论（四选一）

| 候选 | 结论 | 依据 |
|---|---|---|
| `internal/pkg/timex` | **做** | `sleepCtx` 双份 + 布局字面量 19 处，均为实测重复 |
| `internal/pkg/jsonsafe` | **做** | 25+ 不安全断言，且已有一次生产语义事故记录在案 |
| `internal/pkg/httpx` | **不做**（重评条件见下） | 无重复证据：Retry-After 解析已单源（`upstream/errors.go:394 ParseRetryAfter`）；退避/抖动已单源（`server/backoff.go jitterDur/backoffAfter`，注释明写「不在 handler 与 upstream 各写一份」）；5 处 `&http.Client{}` 的 Timeout/Jar/CheckRedirect **本来就该不同**（5s 告警 / 30s 设备授权 / 上游加固 transport / 20s 计费），抽象只增加间接层。重评条件：出现**第二个**包需要同一套重试+超时策略时再建 |
| `internal/pkg/errx` | **不做**（重评条件见下） | 错误词汇已被领域独占：`upstream.ErrKind` + `Classify/ClassifyError/inferEvidenceLevel`（errors.go:73/524/673/714）+ evidence 四态。再建通用 errx 会形成**第二套错误词汇**，与 AGENTS.md「四态分离」冲突；现有 7 个 `IsXxx(err)` 是领域哨兵（`IsFrameTooLarge`/`IsAlreadyCheckin`/`IsBuddyTaskIncomplete`），不是通用谓词。重评条件：出现跨领域复用的**错误码分类**需求（非哨兵判断）时再议 |

## 范围

1. `internal/pkg/timex`：布局常量 + 本地日/时刻格式化 + `SleepCtx`。
2. `internal/pkg/jsonsafe`：`map[string]any` 安全取值，**缺失/类型不符一律 `ok=false`，绝不返回零值冒充**。
3. 迁移真实调用点（每包至少 2 个真实消费者，杜绝"建了没人用"的空抽象）。
4. `.harness/prompts/` 4 个模板，口径必须贴本仓（6 维评审 / 十阶段 / 四态纪律），不写通用套话。

## 非目标

- 不重构 `internal/upstream/transport.go` 的连接构造（禁 HTTP/2 等红线，改动收益为零风险非零）。
- 不一次性迁移全部 25+ 不安全断言（expand-contract：先建原语 + 高价值点，其余按改动就近迁移）。
- **测试文件保留日期布局字面量**（如 `Format("2006-01-02")`）：测试**故意**独立于实现，
  不引用 `timex`，这样布局常量被误改时测试会红而不是跟着一起错。生产代码才是"字面量只留一处"
  的适用范围（独立复审 🔵-3 的口径声明）。
- 前端 `app.js` 组件化：工作量大且需改 go:embed 构建流程，单独排期（本 PRD 不含）。

## 验收（Given-When-Then）

- **F1** Given 任意包需要可取消等待，When 调用 `timex.SleepCtx`，Then ctx 取消返回 false 且
  立即返回；d≤0 时立即返回 ctx.Err()==nil。
- **F2** Given `map[string]any` 缺 key 或类型不符，When 用 `jsonsafe` 取值，Then 返回 `ok=false`，
  **不返回零值**（"缺失 ≠ 零值"）。
- **F3** Given credit 是字符串 `"0.1"`（上游真实形态），When `usageCreditTotal` 解析，
  Then `creditUnknown=true`（保持既有三态语义，不得退化为 credit=0）。
- **F4** Given 新增 `internal/pkg/*` 包，When 跑 `check-boundaries.ps1`，Then 退出码 0（白名单含 `pkg` 层）。
- **F5** Given 全部改动，When 跑 `scripts/pre-commit.ps1`，Then 退出码 0；深度棘轮未回退。

## 红线自查

- R8（上游协议数据不改名）：未触碰 conversationId / 哨兵 / UA 指纹。
- 依赖方向：`pkg` 为纯叶子（零 internal 依赖），只被允许"向内"引用；白名单显式声明。
- 单二进制零依赖：**未新增任何 go.mod 依赖**（纯标准库）。
- R10：新增代码无 `_ =` 吞错；迁移处语义等价。

---

## 第二轮追加（2026-11-02 续，用户要求"整个项目都做复用"）

### A. R8 协议常量单源化 —— 做（真重复 + 红线）

机械扫描"跨包重复 6 行块"115 组，人工核对后确认唯一真重复落在 R8 数据上：
`CLI/2.63.2 CodeBuddy/2.63.2` 有 **3 份**字面量（`upstream/models_fetch.go`、`panel/login.go`、
`cmd/login/main.go`），两个 Origin 与两个登录基址各 2 份，两个计费基址各 2 份。
→ 集中到 `internal/upstream/protocol.go`，其余改为别名（**值一字未动**），
并新增 `TestProtocolConstantsR8` 把字面值钉死——R8 首次有机器检查。

### B. `jsonsafe` 全量迁移 62 处 —— **不做**（结论修正）

原计划"25+ 处不安全断言逐步迁移"。逐处读完全部 62 处（含 3 处注释）后修正结论：
绝大多数是 `x, _ := m["k"].(T)` **紧跟** `!= ""` / `!= nil` 守卫的形态（`payload.go:104/184/217`、
`sse.go` 16 处、`tool_pairing.go` 13 处…），**语义上已经等价于 ok 检查**——零值不会流进决策。
机械替换 = 触碰最热的 SSE 解析路径 + 零语义收益 + 大回归面。
→ 口径改为：**新增代码一律用 jsonsafe；存量按"改到哪迁哪"**（expand-contract）。
判据写进 `.harness/snippets/jsonsafe-取值.md`，避免下一轮再按计数机械迁移。

### C. `internal/pkg/httpx` —— **仍不做**（重评后维持）

复查后发现唯一字节级相同的只有 `writeJSON`（`panel/panel.go` 与 `server/handler.go` 各 6 行）。
但两处的**错误体形状本来就不同**（面板 `{"ok":false,"error":...}` 内部约定 vs
`server` 的 OpenAI 兼容 `{"error":{message,type,code}}`），合并这两个才是错的。
`upstream.ExtractClientIP` 与 `server.extractClientIP` 名字相近但**故意不同**（安全边界），
同样不合并。重评条件写进 `.harness/snippets/json-响应写出.md`。

### D. 前端组件库 —— 计划已定，分步执行

`app.js` 4317 行拆分的前置约束已实测：测试里 29 处硬读 `"app.js"`、≥6 处按
`src.indexOf('function x')` 切片取源码（函数换文件即静默错测）。故执行顺序必须是
**先改测试地基 → 再试点拆纯函数 → 再拆核心工具 → 再按视图拆**。
详见 `.harness/changes/2026-11-02-code-reuse-toolkit/frontend-split-plan.md`。
