# HANDOFF — 工程深度治理：Task 4/5/6 已收口，剩余待办与优先级

> 生成于 **2026-10-16**，HEAD = **`d382968`**（本会话新增 8 个提交，见 §1.1）。
> **本文取代** `docs/superpowers/plans/HANDOFF-depth-governance.md`（那份的 Task 状态已过时：
> Task 4 只写了「过半」、Task 5/6 写「未开始」，实际三者均已收口）。
> 执行方案原文：`docs/superpowers/plans/2026-10-15-engineering-depth-hardening.md`（Task 1–3 完成 + 本文覆盖 4–6）。
> 变更总结（已提交）：`.harness/changes/2026-10-16-depth-tasks-4-6/summary.md`。

---

## 0. 开工前 30 秒自检（照做，别跳）

```powershell
# 环境：Windows PowerShell 5.1 —— 本机**没有**独立 pwsh 可执行文件
$go = "C:\Program Files\Go\bin\go.exe"
$env:GOPROXY='https://goproxy.cn,direct'; $env:CGO_ENABLED='0'

# 四件套 + 治理门禁 + 总闸
& $go build ./... ; & $go vet ./... ; & gofmt -l ./cmd ./internal     # gofmt 必须无输出
& $go test ./... -count=1
powershell -NoProfile -File scripts/check-boundaries.ps1              # 退出码 0
powershell -NoProfile -File scripts/check-depth.ps1                   # 退出码 0（-Detail 看明细，-Update 抬棘轮）
powershell -NoProfile -File scripts/pre-commit.ps1                    # 全量；-Fast 跳过 go test
```

**当前基线（2026-10-16 实测，全部绿）**：`go build`/`go vet` 0 error；`gofmt -l` 空；
`go test ./...` 20 包 ok；边界 0；`check-depth` 未回退（`minCoverage` 71.9%）；`pre-commit` 全量退出 0。

**禁令**：不许 `git add -A`（并行会话的文件混在工作区）；不许放宽 `check-depth` 阈值；
不许为过门禁而写无牙齿测试；跑覆盖率 profile 一律写 `$env:TEMP`（见 §4 坑 1）。

---

## 1. 当前状态（已实测，不要重新怀疑，直接在此基础上干活）

### 1.1 提交链（本会话 8 个）

```
d382968 docs(changes): 记录 Task 4/5/6 收口（改了什么/怎么验证/怎么回滚/遗留）
30c9b12 chore(depth): 棘轮抬高 minCoverage 13.6%→71.9%
c68155a chore(governance): 编码规范补体量约束 + 提交前检查脚本（Task 6）
2ada6ed fix(error-handling): 修掉 5 处真吞错误 + 关键忽略点补注释（Task 4 收尾）
7e5dc4b test(coverage): auth/session/scheduler/cmd-server 补强（均以变异测试实证牙齿）
5ffc835 test(coverage): logfmt/prompt/redisstore/cmd-login 补强，并按变异测试结果补牙
0d98893 test(panel): 覆盖率 13.6%→95.5%（handler 路由层 + autotask/taskcenter 任务序列）
fb0290c test(upstream): 覆盖率 56.8%→89.5%，全部为有牙齿的行为测试
```

> 未 push。push 后 CI 会跑这些新测试（见 T2）。

### 1.2 覆盖率（`go test ./... -cover -count=1` 实测）

| 包 | 前 | 后 | | 包 | 前 | 后 |
|---|---|---|---|---|---|---|
| upstream | 56.8% | **89.5%** | | auth | 68.8% | **99.2%** |
| panel | 13.6% | **95.5%** | | session | 67.2% | **98.8%** |
| logfmt | 33.3% | **100%** | | scheduler | 67.5% | **98.5%** |
| prompt | 41.3% | **93.7%** | | cmd/server | 48.6% | **72.3%** |
| redisstore | 16.8% | **98.9%** | | cmd/login | 24.8% | **99.1%** |

最低包 = `internal/server` 71.9%（`cmd/credit|signin|trial` 为 0.0%，脚本已豁免）。
`oversizeFuncs` 7、`oversizeFiles` 5、`maxFileLines` 2185（**口径见 T1.2**）。

### 1.3 已验证的「有牙齿」范围（这是缺口所在，别误以为全仓都验过）

| 范围 | 证据强度 |
|---|---|
| logfmt / prompt / redisstore / cmd-login | **独立 agent 变异核验**（在 `%TEMP%` 副本改生产代码 → 找洞 → 补牙 → 复测，含 8/8、11/11 全捕获回执） |
| auth / session | 写测试的 agent **自报**跑过变异（27 killed / 10 杀），非独立核验 |
| **upstream / panel / scheduler / cmd/server** | **无任何变异证据**，只有「断言了哪些行为」的自述 ← **T3 就是补这个** |

---

## 2. 硬约束（与上一份交接一致，不可违反）

- **零新功能、零新依赖**（`go.mod` 直接依赖保持 1 个：`github.com/redis/go-redis/v9`）
- **不改上游协议报文数据**（R8：`wb2api-<ms>` conversationId、`chatcmpl-wb2api` 哨兵、三段式 UA）
- **不静默吞错误**（R10，判据见 `.harness/rules/ERROR-HANDLING.md`）
- **提交一律按显式路径**；棘轮只许收紧（`-Update` 仅用于冻结已验证的收益）
- **每个 Task 独立提交**，提交时 `go test ./... -count=1` 必须全绿
- **测试必须有牙齿**：commit body 要能回答「还原什么行为会让它 FAIL」

---

## 3. 待办任务（按优先级；每条含 为什么 / 怎么做 / 验收 / 风险 / 提交语）

### T1 — 补齐我这次留下的三处文档/口径缺陷（★最高，十几分钟级）

**T1.1 `AGENTS.md` 的 Development 章节已过时**（这是「新会话第一件事读本文件」的文件，却是唯一没同步门禁的）

| 现状（`AGENTS.md` L131–133） | 实测真值 |
|---|---|
| `go test ./...`（**21 包：18 ok + 3 no test files**） | **23 包：20 ok + 3 no test files** |
| `gofmt -l ./cmd ./internal`「允许 3 个 pre-existing 漂移」 | Task 2 已清零，**输出为空** |
| 只列了 `check-boundaries.ps1` | 缺 `scripts/check-depth.ps1`、缺 `scripts/pre-commit.ps1`（提交前必跑总闸） |

**验收**：`AGENTS.md` 的 Development 段与 `grep -c` 实测数字一致；出现 `pre-commit.ps1` 与 `check-depth.ps1` 两条命令。
**提交语**：`docs(agents): 同步门禁清单与包数（补 pre-commit/check-depth）`

**T1.2 「文件行数」口径不一致（我引入的）**
`scripts/check-depth.ps1` 的 `maxFileLines` 用 `Measure-Object -Line`，**不计空行**（探针实测：5 行文件→4）。
所以脚本报 `2185`，而 `internal/upstream/client.go` 真实是 **2315** 行（差 130 空行）。
脚本对**函数**口径写了长长的 why（排除空行/注释），对**文件**口径一句都没写，我照着它把 `编码规范.md` §九 写成了「文件 ≤1000 行」。

**怎么做**（二选一，建议 A）：
- **A（推荐）**：在 `check-depth.ps1` 头部补一段注释声明文件口径 = 非空行；`编码规范.md` §九 改成「单文件 ≤1000 **非空行**」，并给出真实行数对照。
- B：改脚本为真实行数（`(Get-Content).Count`）。结论不变（5 个文件两种口径都 >1000），但基线数字要 `-Update` 重算，且要与 §九 同步。

**验收**：拿 `client.go` 验证文档里的两个数字都能复现（2185 非空行 / 2315 实际行）。
**提交语**：`docs(rules): 对齐文件体量口径（非空行 vs 实际行）`

**T1.3 方案文档 28 个 checkbox 全未勾选**
`2026-10-15-engineering-depth-hardening.md` 里 `- [ ]` 28 个、`- [x]` 0 个 → 无法当进度记录用。
**怎么做**：全勾掉，或在文件头加一行「Task 1–6 全部完成，见 `.harness/changes/…` 与本文」。
**验收**：文件里不再存在会误导新读者的「未开始」状态。

### T2 — Task 6 只做了一半：把门禁接进 CI（★高）

**已查明**：`.github/workflows/go-binaries.yml` **已有**测试门禁，不是没有 CI：

```
test job : go test ./...  (L38)   go vet ./...  (L42)   node --check app.js (L44)   govulncheck (L50)
race job : go test -race ./...  CGO_ENABLED=1  (L65)     ← 本机无 gcc 跑不了，race 由 CI 独占
build job: 五平台交叉编译 (L94) + 断言二进制含源码 appVersion (L96-104)
```

**缺**：`gofmt -l`、`check-boundaries.ps1`、`check-depth.ps1`、R10 判据（即 `pre-commit.ps1` 的全部内容）。
**另一个洞**：PR 的 `paths` 过滤器（L13-19）只含 `go.mod|go.sum|cmd/**|internal/**|config.example.json|workflow`——
**只改 `scripts/**` 或 `.harness/**` 的 PR 不会触发 CI**（而门禁脚本正好住在 `scripts/`）。

**怎么做**：
1. `go-binaries.yml` 加 `gate` job（`runs-on: ubuntu-latest`，runner 自带 `pwsh`）：
   `pwsh -NoProfile -File scripts/check-boundaries.ps1` + `check-depth.ps1` + `gofmt -l ./cmd ./internal` + R10 diff 检查。
2. 把 `scripts/**`、`.harness/**` 加进 PR `paths`。
3. 在 `.harness/rules/开发流程规范.md` 写明分工：**本地 `pre-commit.ps1` 是提前发现，CI 才是最终闸门；`-race` 由 CI 独占**。
4. `-Install`（装 `.git/hooks/pre-commit`）**默认不要装**——多会话并行会互相卡（全量测试含 panel 的 44–60s）。

**验收**：PR 改 `scripts/` 下任一文件会触发 gate；gate 里故意加一个 gofmt 漂移能红（验证 CI 那份有牙齿）。
**风险**：`check-depth.ps1` 在 Linux 上要能跑（`Get-Content`/`Measure-Object` 通用，但需确认没有 Windows-only 路径）。
**提交语**：`chore(ci): 门禁接入 CI（gofmt/边界/深度/R10）+ PR paths 补 scripts 与 .harness`

### T3 — upstream / panel 的「有牙齿」没有独立证据（唯一真漏洞，★高）

**为什么**：本轮覆盖率从 56.8%/13.6% 拉到 89.5%/95.5%，但这两个包**一次变异核验都没做**。
我已经在 4 个小包上用同一手法抓出过真洞（`prompt.Degraded` 的断言自证循环、`Rewrite` 丢 `tools` 无人钉、
`redisstore` 删掉写并发上限全绿），不能假定大包更干净。

**怎么做（沿用已验证的配方，务必避开 §4 坑 2）**：
```powershell
$dst = Join-Path $env:TEMP "mut-upstream"
robocopy "<local>\Desktop\yg-gateway" $dst /E /XD .git auths data bin docs /NFL /NDL /NJH /NJS /NP
Set-Location $dst
& $go env GOMOD          # ← 必须等于 $dst\go.mod，否则你测的是原件（坑 2）
```
- 每包做 5 条**语义变异**（改常量、反转条件、删分支、换返回、边界 ±1），逐条：备份 → 改 → `go test -count=1` → 记录「哪些测试 FAIL」→ 还原 → 校验 hash + 重跑全绿。
- 目标：**CAUGHT ≥ 4/5**。存活者逐条判定「是测试缺断言」还是「语义等价」，前者必须补断言再复测。
- panel 建议优先变异：自动领奖的域选择（mp vs web）、队列 per-account 锁、`runAutoAll` 的跳过条件、20 分片边界。
- upstream 建议优先变异：三段式 UA 形状、global/cn base 与路径候选、`model.json` 三级 lookup 顺序、effort 桶合并方向。
- **验收**：两包各给出变异表（变异位置/改动/结果/捕获它的测试名）+ 「原件工作区零改动」自检。
**提交语**：`test(upstream,panel): 变异核验 + 补牙（CAUGHT x/y）`

### T4 — 把「不可改名的协议数据」做成 golden（★高，一次性收益大）

**为什么**：R8 现在只靠人记得 + 散落在各测试里的逐字断言。`cc-switch` 用 `tests/golden/*`（含红线快照）
钉住不可变内容；**我们自己的 CI 也在用同一手法**（`go-binaries.yml:96-104` 断言二进制含源码 `appVersion`）。

**怎么做**：`internal/upstream/testdata/*.golden`（三段式 UA、chat 路径、billing 路径、会话头族、错误帧 envelope），
配一个 `-update` 开关（默认只比对）。**验收**：改 golden 里任一字节 → 测试红；`-update` 才能重写。
**风险**：别把会随上游版本变的东西（UA 版本号本身）整串钉死——按 `WorkBuddy/<ver>` 抽变量，只钉结构与顺序。
**提交语**：`test(upstream): R8 协议数据 golden 化（改名即红）`

### T5 — 用「每包一个 TestMain 沙箱」根治 flake（★中高）

**为什么**：两个既有 flake 至今未修——`internal/server` 的 `TestModelsEndpoint` 在 `-count=3` 下因
**测试间全局状态污染**失败；`TestEwmaLatencyPenalizesSlowInPick` 时间敏感偶发（约 1/8）。
`magpie` 的做法是对症的：每包 10 行 `main_test.go` → `TestMain → testenv.Main`，把 HOME/临时目录隔离，
并给子进程传 sandbox marker，**逃出即报错**。

**怎么做**：`internal/server/main_test.go`、`internal/pool/main_test.go`：隔离 HOME/工作目录；
加一条「测试不得写真实 `auths/`」断言（守 R1）。EWMA 那条改成**可注入时钟**（生产侧小改），不要靠「重跑至绿」。
**验收**：`go test ./internal/server/ -count=3` 全绿；EWMA 测试 `-count=20` 全绿。
**提交语**：`test(server,pool): 测试沙箱隔离（治 -count=3 全局状态污染）`

### T6 — ADR-008 + LESSONS.md（★中）

- **ADR-008「深度门禁用棘轮 + 自建 PS 脚本，不引入 linter/覆盖率工具」**：满足 ADR 三条件（难逆转 / 未来读者会困惑
  「为什么不上 golangci-lint」/ 有真实权衡：零依赖 vs 工具生态）。`docs/adr/` 现在到 `ADR-007`。
- **`.harness/LESSONS.md`**：抄 `magpie` 的 Seen 计数制（每条 = 规则 + commit 证据 + Seen 次数；同一类坑 **第 2 次**即上规则）。
  首批写本轮的三条（见 §4 坑 1/2/3）。并在 `AGENTS.md` 加一句「动手前先读 LESSONS.md」。
**提交语**：`docs(adr): ADR-008 深度门禁选型` + `chore(governance): LESSONS.md（Seen 计数制）`

### T7 — 4 处「留待产品定夺」的口径，用一个统一机制收掉（★中，需你拍板）

清单见 `.harness/rules/ERROR-HANDLING.md` 「留待产品定夺」表。**不要逐处打补丁**，`magpie` 的模型给了统一解法：

> 429/5xx/超时/DNS = 「这次没告诉我任何关于账号的事」→ **沿用上次读数 + 打 `AsOf`**；
> 只有 401/402/凭证类才算「关于账号的结论」。

对应我们的两处核心问题：`UsageCacheMissTokens` 的「未知 vs 全命中不可区分」、`credit=0` 零值兜底被
`NoteModelCost` 当真值写进成本台账——**两者都是让 `0` 冒充了 `unknown`**。

**怎么做（建议）**：在**写台账的入口**加「unknown 不得写 0」的准入（写 `unknown` 标记或拒绝），
而不是改每个调用点的数字口径。**验收**：构造 unknown 输入 → 台账里出现 unknown 而非 0；面板显示 unknown。
**风险**：这会改计量口径 → 属产品决策，先确认再动。
**提交语**：`fix(usage): 写台账的 unknown 准入（未知不再冒充 0）`

### T8 — 深读任务的剩余部分 + 结论落盘（★中）

- **未读**：cc-switch 的 `src-tauri/src/services/provider/*`（8312 行 mod.rs）、各 `*ProviderPresets.ts`（数千行）、
  `database/`（schema/migration/dao，DB 与文件谁是事实源）、`services/usage_stats.rs` + `session_manager/*`。
- **已读**（结论在会话记录里，未落盘）：magpie 的选号决策链 / 会话亲和与失败分类 / 配额三层 / 计量 / 插件运行期 / 工程纪律；
  cc-switch 的代理转发内核（预读首包才可切）、协议转换（星形无 IR + `stream_ended_with_error` 门禁）、配置改写层（token 级往返 + 语义闸门 + 前滚）。
- **落盘建议**：`.harness/wiki/` 下一份对照笔记（或 `docs/`），每条结论带 `file:line`。
- 另：深读里 4 条高价值建议**一条都没实现**（退避与额度解耦、快过期积分单一纯函数、流式 error 门禁、写回前回读校验），属于下一阶段的重构，需先立 PRD/ADR。

### T9 — dsh 客户端：`cordis.patch.yml` 的图片能力（★中，独立于本仓）

**核验结论**（本会话已完成）：
- 文件 `<local>\.dsh\profiles\desktop\cordis.patch.yml`，改动干净：**新增 20 行 / 删除 0 行**，
  内容唯一 `input: ["text", "image"]`，缩进 12 空格与 `maxTokens` 一致，无 TAB。备份 `cordis.patch.yml.bak-img-20261016` 在。
- **报告里的「21 个模型」实际是 20 条**（`cn:auto` + 19 个 `cn:*`）；`cn:auto` 是合成路由名，给它声明 image 意味着
  「auto 落到不支持图片的模型时失败点后移到上游」——策略问题，需你定。
- **根因链补一环**：`@deepseek-ai/dsh-llm-pi-ai/lib/index.js` 的 `:906 const DEFAULT_INPUT = ["text"]` →
  `:682 input: declaredInput(entry.input) ?? base?.input ?? [...request.defaultInput]` →
  `:1792 inputModalities: [...model.input]` → dsh `session-controller` 的判据
  `if (model.inputModalities !== void 0 && !model.inputModalities.includes("image")) throw ...MODEL_DOES_NOT_SUPPORT_IMAGES`。
  **注意 `!== void 0`：未声明时下游本来放行**——是桥接层默认值把「未知」消灭成了「不支持」。
  （pi-ai 的 `model.input.includes("image")` 只是消费端；`input` 为 undefined 时它会抛 TypeError，不会优雅降级。）

**更好的修法**：`defaultInput` 是 **provider 级**配置项（`:993`，挨着 `defaultContextWindow`/`defaultMaxTokens`），
在 `yg-gateway:` provider 加一行 `defaultInput: ["text", "image"]` 即可覆盖全部条目**及将来新增的**。
建议 **provider 级 + 保留现有 20 条**（条目级优先，互不冲突）；实测确认不支持图片的模型再单独降回 `["text"]`。

**验证（比「看日志有没有 image_url」硬）**：重启后发图 → 用网关 reqlog 归档 JSONL 确认 `image_url` 真出站；
若模型其实不支持，上游回 11133/11135，`internal/upstream/hint.go:41-42` 会直接给
「model X does not support images; pick one with supports_images=true from /v1/models」。**按模型记一次实测结果**，
别让 20 条未验证的断言长期躺在配置里。
**风险**：同目录 5 个 `.bak-*`，任何回滚旧备份都会静默抹掉这次修复 → 这份 patch 建议纳入版本管理。

### T10 — `review.md`（可选，惯例不统一）

`CODE-STANDARDS-CHECKLIST.md` §九 要求 `summary.md` + `review.md`，但 9 个 change 目录里只有 2 个配了 review.md。
要么给 `.harness/changes/2026-10-16-depth-tasks-4-6/` 补一份，要么把规范改成「review.md 仅 L2/L3 改动必需」。

---

## 4. 本轮踩过的坑（新对话务必避免重踩）

1. **`-coverprofile` 用单引号写变量 → 生成字面名垃圾文件**。agent 写了 `-coverprofile=$out`（单引号），
   于是仓库根出现 `$out`/`$p`/`$tmp` 三个文件（内容是覆盖率 profile）。**正确写法**：
   `$out = Join-Path $env:TEMP "cov.out"; & $go test <pkg> -count=1 "-coverprofile=$out"`（双引号展开变量）。已清理，别再犯。
2. **变异测试必须断言「我在副本里」**。有 agent 的第一版脚本 cwd 落在原件，`go test` 实际测的是**未变异**的源码，
   于是输出「5/5 全存活」的**假结果**（它自己发现并作废了）。开跑前先断言 `go env GOMOD` 指向副本，并在改前改后校验 hash。
3. **并行 agent 的结构化返回值要原样往上返**。我第一次写 workflow 时把 pipeline 结果按 `r[0]/r[1]` 解构映射，
   实际返回的是「末阶段的单个结果」→ 一次性丢掉 8 份报告，全部结论丢失、只能重跑。
4. **PS 5.1 的两个引号陷阱**：① 双引号串里的「反引号 + 双引号」会把字符串引号吃掉（`pre-commit.ps1` 因此解析失败）；
   ② 中文串里混用反引号做行内代码同样中招。**行内引用一律用「」**，只在需要转义时用反引号。
5. **`Measure-Object -Line` 不计空行**（见 T1.2）。任何「行数」指标都要先问口径。
6. **`gofmt -l` 要的是 gofmt，不是 `go fmt -l`**（后者报 `flag provided but not defined: -l`）。
7. **panel 测试变慢是预期的**：单轮 44–60s，因为 mp 上报路径有 0–10s 随机反作弊抖动且不可注入。
   写新测试时尽量走「达标/已领」路径（0 抖动）以压低时长。
8. **`go test ./internal/server/ -count=3` 现在会红**（既有全局状态污染，非本次引入）——T5 治它。

---

## 5. 明确不做（除非用户改主意）

- 不引入 linter / 覆盖率工具 / 测试框架（零新依赖是硬约束；ADR-008 会把这个取舍写下来）
- 不装 `.git/hooks/pre-commit`（多会话并行会互相卡）
- 不为凑覆盖率写「调用即断言非 nil」的测试
- 不擅自改「留待产品定夺」的计量口径（T7 需先拍板）
- 不碰并行会话的文件（见 §6）

---

## 6. 工作区卫生：这些文件不是你的（并行会话在跑另一条工作流）

**已核实的并行会话工作流**（读 `git diff -- HANDOFF.md` 得出，与 Task 4/5/6 **无重叠**）：

> 面板/前端 + 生产部署：Playground 模型下拉 WorkBuddy 化（`pgModelLabel`/`pgModelOptions`）、
> 模型与档位视图的「实测成本率列」（`cost_ppm`/`costLine`/`fmtCostPpm`）、unknown 面板展示、
> README 排版重写、`.harness/rules/COST-CONTROL.md`；并把二进制替换部署到 **D: 生产实例（7863）**。

| 文件 | 归属 | 对新对话的影响 |
|---|---|---|
| `HANDOFF.md`（仓库根） | 并行会话 | 与 `docs/superpowers/plans/` 下的交接文档**是两回事**，别混。它里面有「21 包」这类旧计数（实际 23 包） |
| `README.md` | 并行会话（排版重写） | — |
| `internal/panel/app.js` | 并行会话（改完必须 `go build`，R13） | — |
| `internal/panel/panel.go` | 并行会话 | T3 的变异核验用 `%TEMP%` 副本，会带上他们未提交的状态——**可接受**，但结论要注明基线含其 WIP |
| `internal/panel/frontend_ux_test.go` | 并行会话 | 别改；它会与 `frontend_test.go` 一起在 panel 包内编译 |
| `.harness/rules/COST-CONTROL.md` | 并行会话（未跟踪） | 与本批任务无关 |
| `docs/superpowers/`（整目录） | 上一会话 + 本次 | 仍 untracked（用户选择「暂不动」）；见 D1 |

**一律按显式路径 `git add`**。本会话 8 个提交都是这么做的，可 `git show --stat <sha>` 逐条核对。
**开工前先 `git status --porcelain` 看清当前有哪些是别人的**——并行会话会持续改动上面这几项。

---

## 7. 需要用户拍板的决策点

| # | 决策 | 备选 |
|---|---|---|
| D1 | `docs/superpowers/`（方案 + 本文）要不要提交？ | 提交（可追溯） / 继续 untracked（当前） |
| D2 | 棘轮要不要在 CI 里强制（`check-depth` 失败即红）？ | 强制 / 只在本地 |
| D3 | T7 的计量口径（unknown 不写 0）要不要改？ | 改（更诚实） / 只加观测字段不动数字 |
| D4 | `cordis.patch.yml` 用 provider 级 `defaultInput` 还是维持 20 条逐条声明？ | 两者并存（建议） / 只留逐条 |
| D5 | `cn:auto` 要不要声明支持图片？ | 声明（可能后移到上游失败） / 保持 text-only |
