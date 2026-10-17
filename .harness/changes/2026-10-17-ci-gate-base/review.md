# 评审记录 — CI gate 差分基准（A1，L2）

- 变更：`.github/workflows/gate.yml`（base 解析）+ `.harness/rules/开发流程规范.md` §五（口径同步）
- 需求：`docs/prd/REQ-ci-gate-base-2026-10-17.md`　记录：`summary.md`（同目录）
- 评审方式：第一轮自评（六维逐条 + 证据）；第二轮由**独立复审者**（无本会话上下文）只读复审

---

## 一、结论

**通过**（🔴 无；🟡 1 项已在同一变更内清零，修复证据见 §三末）。

第一轮自评未发现阻断项；第二轮独立复审发现 **1 项 🟡**（tag 路径会因 `-ci` 演练 tag 静默退化，
且日志不实）+ 4 项 🟢。按 §四「🟡 未清零不过评审」，全部修掉后复测通过。

---

## 二、第一轮：六维自评

### 1. 正确性

| 判据 | 证据 |
|---|---|
| 六种事件形态各自取到正确基准 | `summary.md` §2.1 的 7 个分支（含 tag 的「有/无更早 tag」两支），把 `run:` 块**原样抽出**用 bash 实跑 |
| 修复真的修掉了那个洞 | `summary.md` §2.2 的牙齿探针：同一份 `pre-commit.ps1`，`base=HEAD~1` → **exit 0 且「检查了 0 个生产文件」**；`base=HEAD~2` → **exit 1 并点名违规行** |
| 无法确定基准时不静默 | 3 条回落路径全部输出 `::warning::`，且把「早期提交可能漏检」写进日志 |
| 判据留在门禁自己手里 | `workflow_call` 继承调用方 `github` 上下文 → `gate.yml` 直接读 `github.event.before`，调用方无法各自算错 |

### 2. 性能

CI 时间**不变**：`fetch-depth: 0` 早已存在，只是 `git diff` 范围变大（R10/gofmt 是逐行扫描，几万行量级仍在秒级）。
tag 分支多一次 `git describe`（本地操作，毫秒级）。

### 3. 安全

- 无新凭证、无新权限（`permissions` 未改）。
- 注入面：`${{ github.ref_name }}` / `${{ inputs.base_ref }}` 进入 shell 的地方**都加了引号**；
  `base_ref` 只用于 `git fetch` 的 refspec 且被双引号整体包裹。GitHub 的分支名不允许空格与
  shell 元字符（不含 `$()`、引号），但即使有人手工 dispatch 传怪值，最坏结果是 `git fetch` 报错
  → `set -e` 退出，不会执行到下游判据。
- 无日志脱敏问题（不接触凭证）。

### 4. 一致性

- `开发流程规范.md` §五 两处口径已同步（`go-binaries.yml` → `release.yml`；`push 用 HEAD~1` → `before` + 回落语义）。
- `gate.yml` 头部的「被发布流水线调用」段落早前已同步为 `release.yml`（本轮之前那次合并时改的）。
- 与 `docs/PLAN-open-items-2026-10-17.md` §1 A1 的描述一致；该行末尾会回填「已完成」。

### 5. 可维护性

- 41 行 bash，四种情形各有一段注释说明**为什么**（尤其 tag 那段写清了「为什么不能一律告警」）。
- 没有把判据散到调用方；将来新增调用方无需改任何东西。
- 边界明写在 `summary.md` §三：强推场景只能退化到 `HEAD~1`，要更稳需要外部状态（不在本次范围）。

### 6. 架构合规

- 未触碰任何 R 红线（R1/R2/R5/R6/R8/R9/R10/R14/R15 全无关；本变更不含 `.go`）。
- **未放宽门禁**：唯一的方向是「检查更多行」。
- 依赖方向、包结构、`go.mod` 均未变。

---

## 三、第二轮：独立复审（无本会话上下文）

独立性：复审者只有仓库与技术约束，没有本会话的推理与结论；可运行 git/go/bash 取证，被要求只读。
它自己声明了两点方法论：不采信我的 summary，**独立复现**了七分支与牙齿探针；
读 `actions/checkout@v4` 的源码（而非文档）确认 `fetch-depth: 0` 会取全部 tag。

**结论：有条件通过（🔴 无 / 🟡 1）。**

| 级别 | 位置 | 问题 | 处置 |
|---|---|---|---|
| 🟡 | `gate.yml` 的 `git describe --tags --abbrev=0` | 未排除 `-ci` **演练 tag**。`release.yml` 支持「先打 `-ci` 演练再发版」，演练 tag 通常贴在 `HEAD~1` → 基准取到它 → **窗口退化成 1 个提交（= 旧的 `HEAD~1` 行为）**，而日志仍写「全部提交都进入差分」——不实日志，正是本变更要消灭的形态 | ✅ 已修：`--match 'v[0-9]*' --exclude '*-ci'` + 文案改为「自该 tag 起」。**复现了复审给的数字**：旧命令取 `v2.1.0-ci`/窗口 **1**，新命令取 `v2.0.0`/窗口 **3** |
| 🟢 | 空 `ref` 降级为假绿（`-Base ""` → 0 个文件 + exit 0），当前无可达路径但无护栏 | 无护栏的形态：将来加分支忘写 `ref=` 就会假装查过 | ✅ 已修：重构为单一写点 + 非空断言 `::error::`/`exit 1`；**变异验证**（抹掉 7 处赋值 → exit 1，不写 ref） |
| 🟢 | `-Base "${{ … }}"` 直拼进 `run:` | GitHub 要求走 `env:`；本变更把可达取值扩到 tag 名，而 git 允许 ref 含 `$ ( ) ;`，bash 双引号内 `$()` 会执行 | ✅ 已修：`env: BASE_REF_RESOLVED` + `"$BASE_REF_RESOLVED"`（bash 语法） |
| 🟢 | 口径漂移 `工程结构.md:45`、`ADR-008:88` | 本次标题就是「同步口径」，只同步了开发流程规范 §五 | ✅ 已修：前者改指向；ADR **追加**更新段（不改写历史决策原文） |
| 🟢 | 本文件与 PRD 的证据口径不实/悬空 | 「五分支/24 行」与实不符；引用了不存在的「§四 提交前实测」 | ✅ 已修：改为 8 分支 / 48 行 / 真实章节号 |
| 🔵 | `workflow_dispatch` 与新分支首推恒回落 `HEAD~1` | 已在 PRD 3.1 明写为边界；且 `release.yml` 只对 `branches:[main]` 触发 | 无需动作 |

**复审独立确认「无问题」的项（摘）**：YAML 可解析（pyyaml）·`set -euo pipefail` × `!` × `pipefail`
无意外退出（实跑 750 次，非零退出 0）· 七分支独立复现 · **tag/branch 同名歧义不成立**
（`rev-parse` 取 tag）· `checkout@v4` 源码确认 `fetch-depth: 0` 会取全部 tag · 牙齿探针独立复现
（`HEAD~1` → 「0 个生产文件」+ exit 0；`HEAD~2` → FAIL 点名行）· `-Base` 垃圾值 → exit 2（红）·
四种合法 `-Base` 取值都能被 `rev-parse --verify "<ref>^{commit}"` 解析 · 门禁**只变严**。

**复审明确列为「无法判定」的四项**（本文件照录，不粉饰）：

1. 未在 GitHub ubuntu runner 上真跑（本地是 Windows Git bash + PS 5.1）→ 结论属**外推**；
   *本轮已用 CI run 38999922785 补上这条：日志里出现 `base = push 事件的 before（953806e…）`，
   即真实 runner 上多提交 push 走的是新逻辑*。
2. GitHub 侧是否允许含 `$()`/`"` 的 tag 名：git 侧允许，GitHub 侧**待确认**（若更严，该注入路径不可达）。
3. 「本地全量 pre-commit 跑过」当时**无记录**（悬空引用）→ 已补：两次全量 7/7 全绿。
4. 真实 tag push 的 `before` 是否恒为 40 个 0：未实测；不影响结论（代码先判 `refs/tags/`）。

---

## 四、待办（本轮不做，留给下一轮）

| # | 项 | 理由 |
|---|---|---|
| 1 | `release.yml:247` 的「上一版 tag（compare 链接用）」用的是**同一条**无 `--exclude` 的 `git describe` | 同一类问题：`-ci` 演练 tag 会让 GitHub Release 的 compare 链接指向演练点。但它是**展示**不是门禁判据，且 `REQ-ci-gate-base` §二 明确「不动 release.yml」。建议与 `release.yml` 的下一次改动合并做 |
| 2 | 强推场景只能回落 `HEAD~1` | 要更稳需要「上次成功运行的 SHA」这类外部状态，会引入首次运行的鸡生蛋问题。属另一件事，已在 PRD §3.3 与 summary §三 明写为边界 |
| 3 | `workflow_dispatch` 手工触发时 `HEAD~1` | 可接受（`notice` 而非红），但如需精确可在 dispatch 输入里加 `base_ref` 参数 |
