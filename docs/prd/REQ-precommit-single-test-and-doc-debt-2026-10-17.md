# REQ — 全量测试只跑一次 + 覆盖率口径实测 + 面板 unknown 观感 + 文档债更正

- 状态：已实施（2026-10-17）
- 日期：2026-10-17
- 分级：**L2**（改的是 `check-depth.ps1` 的失败语义 —— 属门禁行为变更）
- 来源：`docs/PLAN-open-items-2026-10-17.md` §1 A2/A3/A8、§3 文档债
- 相关纪律：R10（禁静默吞错）、L-3（PS 引号/变量展开 → 本次实测又踩一次，见 §六）、
  L-11（测试与它依赖的改动同一提交）
- **并发边界**：A1（CI gate 的 base 口径）由并行会话负责，PRD 见
  `docs/prd/REQ-ci-gate-base-2026-10-17.md`。本需求**不碰** `.github/workflows/gate.yml`
  与 `.harness/rules/开发流程规范.md`，避免同仓两会话改同一文件。

## 一、问题

### P-1 全量测试在同一个门禁里跑两遍，而且"测试红"打不红深度棘轮

`scripts/pre-commit.ps1` 全量模式：检查 6 跑 `go test ./...`，检查 7 调 `scripts/check-depth.ps1`
——后者内部**又**跑一次 `go test ./... -cover`（棘轮要覆盖率数字），实测本机多花约 40s。
更严重的是 `check-depth.ps1` 对测试失败是**宽容**的：

```powershell
if ($LASTEXITCODE -ne 0 -and -not ($coverOut | Select-String 'coverage:')) { ... exit 2 }
```

只要输出里有 `coverage:` 行就继续。于是「测试红 + 覆盖率数字正常可解析」时，深度棘轮报 **PASS**。
这条宽容是 CI 里的必要设计（`test` job 独立报测试失败），但它把「门禁能不能证明测试绿」
这件事交给了调用方——本地全量模式恰好没有调用方担保。

### P-2 覆盖率口径只有一句无法复现的声明

`.harness/changes/2026-10-16-depth-tasks-4-6/summary.md` §四.4 写：「`internal/session` 的
legacy 派生分支…其覆盖率计入 98.8% 但生产可达语句约 89%」。实测（§三 F-904）**89% 复现不出来**。
一句未验证的数字比没有数字更危险：它会被下一个会话当基线引用。

### P-3 面板 unknown 展示没有任何 JS 级测试

unknown 徽章与账号池提示行的拼接逻辑嵌在 `renderAccountsUnsafe`（约 80 行）与 overview
渲染函数里。上一轮「未知能力被塌缩成不支持」的事故就是同类：Go 侧测试全绿，问题只在浏览器里。

## 二、目标与非目标

**目标**：全量测试在一个门禁里只执行一次、失败必红；覆盖率与 unknown 展示各有**可复现**的口径；
四条「写着未做、实测已做」的文档债就地更正。

**非目标（明确不做）**：

- **不改 CI 的 job 并行结构**：`test` 与 `gate` 仍各跑一遍（取舍见 §四 D-2）。
- **不重构 `renderAccountsUnsafe` 的其余部分**：只把 unknown 的两段拼接提取成纯函数，行为零变化。
- **不删 `internal/session` 的 legacy `deriveKey`**：它是 `deriveKeyView` 的等价性对照。
- **不改历史变更记录的原文结论**：用「更正」段追加，保留可审计的原始判断。
- **不碰 gate.yml / 开发流程规范.md**（并行会话负责 A1）。

## 三、验收（Given-When-Then）

### F-902 — 一个门禁里全量测试只跑一次，且测试红必须红

- **Given** 工作区干净、代码全绿
- **When** `powershell -NoProfile -File scripts/pre-commit.ps1`（全量）
- **Then** 退出码 **0**；日志里 `ok <pkg> coverage:` 只出现**一轮**，且 check-depth 段打印
  「覆盖率来源：复用 …（未重复跑测试）」
- **Given** 某测试被改成必失败
- **When** 同上跑全量
- **Then** 退出码 **非 0**；检查 6 报 `go test ./... 有失败`；check-depth **不再**给出棘轮结论
  （直接跳过，不给"通过"的假信号）
- **Given** 直接跑 `scripts/check-depth.ps1`（无新参数 = CI gate 的调用形态）
- **Then** 行为与改动前一致：自己跑一次 `go test -cover`，解析覆盖率

### F-903 — CI 侧重复执行的处置留痕（决策，不是改动）

- **Then** `.harness/changes/2026-10-17-precommit-single-test-and-doc-debt/summary.md` 的决策表
  记录三方案与实测取舍，结论：**保留 CI 并行重复**（wall clock 优先），并说明何时该重开

### F-904 — `internal/session` 覆盖率口径以实测表述

- **Given** `go test "-coverprofile=sess.out" -count=1 ./internal/session/`
- **Then** 包覆盖 **425/430 = 98.8%**；剔除测试专用的 `deriveKey`（20 语句）后 **405/410 = 98.8%**
  → 「约 89%」不成立，按实测更正
- **代码侧**：`deriveKey` 上方注明「生产零调用、只作等价性对照」+ 覆盖率引用口径与复现命令
- **文档侧**：`2026-10-16-depth-tasks-4-6/summary.md` §四.4 追加「更正」段（原结论保留）

### F-905 — 文档债更正（写着未做、实测已做）

- **Then** 四处更正，各带复现命令：
  1. `2026-10-16-t5-sandbox` §六.2：`dynamicModelsCache` 已不存在（现为 `Handler.modelsCache` 实例字段）
  2. `2026-10-16-depth-tasks-4-6` §四.1：`TestModelsEndpoint` 的 `-count=3` 污染不复现（`ok 24.455s`）
  3. `HANDOFF.md` §五：`D:\yg-gateway-eval`（7864）没在跑，不是「当前活实例」
  4. `README.md`：Go 包数 **23**（20 包有测试 + 3 个 cmd 无测试），不是「20 个包全绿」

### F-906 — 面板 unknown 徽章与提示行有 JS 级测试

- **Given** 账号 `unknowns` 为空数组 / 字段缺失 → **Then** 无徽章、提示行无未知成本文案
- **Given** 一号 2 条 unknown、另一号 1 条 → **Then** 徽章 `2 未知成本`；`title` 含 `question`
  与「需要：」+ `requires`；提示行 `3 个 (账号,模型) 成本未知，悬停账号卡片查看明细`
- **Given** `question` 含 `<script>` 与双引号 → **Then** 输出被转义，不造成属性逃逸
- **牙齿**：去掉纯函数里的 `esc()` → 测试必须 FAIL

## 四、决策点

| # | 决策 | 选项 | 选择与理由 |
|---|---|---|---|
| D-1 | check-depth 的失败语义 | ①保持宽容 ②新增 `-CoverageStdout` 复用 + 自跑时失败即 `exit 2` | **②**。复用模式下调用方已担保绿，自跑（CI）时宽容会让「测试红」被棘轮通过掩盖 |
| D-2 | CI 里 `test`/`gate` 的重复全量测试 | ①保留并行重复 ②gate 消费 test 的覆盖率 artifact（需 `needs: test`）③测试从 test job 摘掉，gate 独占 | **①**。② 串行化后 wall clock ~2.2min → ~2.8min，只省 runner 分钟数；③ 让 PR 的测试信号只剩 gate，且 gate 同时是 build 前置，失败定位变差。**本地没有并行收益，因此本地去重（F-902）**。重开条件：CI 分钟数成为实际约束，或单次流水线 > 20min |
| D-3 | legacy `deriveKey` | ①保留并标注 ②删除 | **①**，等价性安全网 |

## 五、影响分析

| 文件 | 改动 |
|---|---|
| `scripts/check-depth.ps1` | 新增 `-CoverageStdout`；自跑时测试失败 → `exit 2` |
| `scripts/pre-commit.ps1` | 全量模式跑一次 `go test ./... -cover`，输出落 `$env:TEMP` 传给 check-depth |
| `internal/panel/app.js` | 提取 `unkBadge(unknowns)` / `unkNoteText(d)` 纯函数（行为零变化） |
| `internal/panel/frontend_test.go` | 新增 `TestAppJSUnknownBadgeAndNote` |
| `internal/session/session.go` | `deriveKey` 注释补「生产零调用 + 覆盖率口径」 |
| `README.md` / `HANDOFF.md` | 包数措辞 / 7864 实例状态 |
| `.harness/changes/*/summary.md`（2 份） | 追加「更正」段 |

## 六、本次又踩到的坑（已处理，留证防复发）

`go test "-coverprofile=$tmp"` 在 PowerShell 5.1 下若不把整串放进**双引号**，会被拆成
`-coverprofile` + `.out`/字面量 `$tmp` 两个参数 → 仓库根出现 `sess`、`$tmp` 两个字面名垃圾文件
（正是 L-3 描述的形态）。已删除，且 `pre-commit.ps1` 的检查 4 本来就会拦住它们（本轮验证有效）。
