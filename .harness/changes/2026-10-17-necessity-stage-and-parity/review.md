# 评审记录 — 阶段 0 必要性入规范 + 前端体量入棘轮 + 沙箱副本机器核对

- 日期：2026-10-17
- 对象：`.harness/changes/2026-10-17-necessity-stage-and-parity/summary.md` 所列文件
- **轮次：3 轮**（第 1 轮自评 PASS → 第 2 轮独立复审判 **FAIL** → 第 3 轮修复后复跑）

## 当前结论（以最后一轮为准）

**PASS**（第 3 轮；3 条 🟡 已全部闭环，见 §三）

> ⚠️ **本文件按轮次分段，不覆盖历史**：第 1 轮我给的 PASS **被第 2 轮独立复议推翻**，
> 那条记录保留在 §二（它就是教训本体），不许删。
> 规则来源：`开发流程规范.md` §四「L2 及以上必须有一次独立只读复审；自评审不能作为唯一凭据」，
> 以及 `.harness/LESSONS.md` **L-16**。

---

## 一、第 1 轮：自评审（六维）— 结论 PASS（**已被推翻**）

| 维度 | 当时的结论 | 依据 |
|---|---|---|
| 正确性 | 通过 | 三种输入路径手测；沙箱比对用 `runtime.Caller`（第一版用相对路径直接红，因为 `TestMain` 已 chdir） |
| 门禁退化 | 通过 | 新检查是加法；`exit 2` 也算失败（fail-closed）；`-Base` 透传 |
| 红线 | 通过 | 无 `SilentlyContinue`、只读文件、不写仓库根 |
| 一致性 | 通过 | 正则 ↔ 脚本 ↔ REQ 三处措辞一致 |
| 可维护性 | 通过 | 注释写清「为什么不塞进 `maxFileLines`」 |
| 架构合规 | 通过 | 无新包/新依赖/方向变化 |

**这轮为什么不够**：它检查的是"我有没有按自己的设计做到"，而三条真问题都藏在
「我的设计在别的环境里还成不成立」——本机的 `.gitconfig`（`quotePath=false`）恰好把缺陷盖住了。

## 二、第 2 轮：独立只读复审（子代理）— 结论 **FAIL**（0 🔴 / 3 🟡 / 5 🟢 / 6 🔵）

### 三条 🟡（必须修，全部已修）

| # | 问题 | 复审证据 |
|---|---|---|
| 🟡-1 | 脚本 4 处 git 调用从不判 `$LASTEXITCODE`，`2>$null` 丢 stderr → `-Base` 写错时 exit 1，被 pre-commit 误归因为「缺必要性段」 | `-Base 'HEAD:docs/prd'` 实测 |
| 🟡-2 | `core.quotePath` 默认 true → 中文名 REQ 输出成 `"…\344\270\255…"`，正则不匹配 → **CI 静默跳过**；本机绿来自用户级 gitconfig 巧合 | 临时 `quotePath=true` 复现：修复前 exit 0（静默放过） |
| 🟡-3 | 前端棘轮只扫 `*.js`，`index.html`（1755 行）漏在门外；且自设的「任一前端文件 >1000 行」重评条件落地当天已失效 | `index.html` 实测 1755 非空行 |

### 复审通过的部分（记录备查）

棘轮方向正确（缺字段 → `[int]$null=0` → 一律收紧）；`-Update` 会写入新字段；
`TestSandboxFilesInSync` 实跑 PASS（归一化 255 vs 252，丢 TestMain 3 行后逐行相等）；
`runtime.Caller` 在 Linux `go test ./...` 下成立；8 处编号全对；add-后-删盘 → exit 1（从 index 读，正确）；
bogus base → 2；非 REQ/非 md → 0；场景 S2–S6 全部符合设计。

### 文档数字复核不一致（已更正）

存量 REQ「7 份」实为 **11** 份（12 份里只有本轮新增那份有该段）；备份「41 份 / 421.7 MB」与
PLAN 的「42 份 / 431.5 MB」是**两种口径**（仅 `.bak` = 42 份 / 421.7 MB；加 `yg-gateway.exe.tmp`
= 43 份 / 431.5 MB），已按口径写法更正。

## 三、第 3 轮：修复后复核（本机实跑）

| 修法 | 复核命令与结果 |
|---|---|
| git 调用收敛到 `Invoke-Git`（`2>&1` + 判退出码 + 失败 `exit 2`） | `-Base 'HEAD:docs/prd'` → **exit 2**，打印「这是脚本自身失败，不是没有新增文件」 |
| 三处调用加 `-c core.quotePath=false` | 临时 `core.quotePath=true` + 中文名缺段 REQ → **exit 1**（修复前 0）；探针已删、config 已还原 |
| 前端扫描面扩到 js/html/css + 新增 `oversizeFrontendFiles`（基线 2） | `check-depth.ps1` → `最大前端文件行数 4378 (app.js)` / `>1000 行前端文件数 2`，`exit 0`；app.js 加一行 → `REGRESSED: 4378 → 4379` |
| `-Update` 保留 `_comment`（从文件读，不从 `$baseline.metrics` 取） | 手写带说明的 baseline → 真跑 `-Update` → `_comment` 原文保留 |
| 正则放宽到 `^#{1,6}` + 跳过 ``` 围栏；`Get-Content -Encoding UTF8` | 缺段探针 → exit 1；有段 → exit 0 |
| 措辞/清单/数字 | 「阶段 0 + 十阶段」统一；pre-commit 头部补第 6 条约束；`_comment` 口径写清 |

**门禁**：`pre-commit.ps1` 全量 8 项 —— gofmt 干净 / R10 无新增静默忽略 / PS 地形无新增反斜杠双引号 /
仓库根无垃圾文件 / build+vet 通过 / 20 包测试绿（`-cover` 复用）/ 边界通过 / 深度棘轮未回退 / 阶段 0 通过。

## 四、问题清单（当前状态）

| 级别 | 位置 | 问题 | 状态 |
|---|---|---|---|
| 🟡 | `check-req-necessity.ps1` | git 失败无判据 / quotePath 静默跳过 | **已修**（§三） |
| 🟡 | `check-depth.ps1` 度量 5 | 漏 `index.html` | **已修**（扫描面 + 计数指标） |
| 🟢 | `sandbox_parity_test.go` | 归一化丢弃注释行，注释分叉不会被发现 | 刻意（注释含互指说明，行为由代码行决定） |
| 🟢 | `check-req-necessity.ps1` | 只校验"有没有这一段"，不管内容质量 | 刻意（D-1） |
| 🔵 | `check-depth.ps1` 度量 5 | 只扫 `internal`（度量 1 扫 `internal, cmd`） | `cmd` 无前端产物；已在注释写明这是设计而非疏漏 |
| 🔵 | `review` 流程本身 | 自评审曾单独作为凭据 | 已升级 L-16 + 规范 §四 🔴 条款 |

## 五、未覆盖（未覆盖 = 未知，不是没有）

1. **CI 上首次真实运行**：本轮只在本机模拟了 `-Base origin/main` 与 `quotePath=true`，未在
   GitHub Actions runner 上跑过。首个 push 的 CI 结果即验证。
2. **PS 5.1 + 遗留 ANSI 代码页（ACP≠65001）**：加了 `-Encoding UTF8` 但未在 ACP=936 的机器上实测 → **待确认**。
3. **浏览器级 E2E** 仍缺（与本变更无关，是 D-5「不拆 app.js」的重评条件）。
