# 评审记录 — precommit 单次测试 + 覆盖率口径 + 面板 unknown 观感

- 日期：2026-10-17
- 对象：`.harness/changes/2026-10-17-precommit-single-test-and-doc-debt/summary.md` 所列 10 个文件
- 轮次：第 1 轮
- 方法：六维逐项 + 实证命令（不是读一遍代码就下结论）

## 结论

**PASS**（无 🔴 / 🟡；🟢 2 条、🔵 3 条已记录）

## 一、六维结论

| 维度 | 结论 | 依据 |
|---|---|---|
| 正确性 | 通过 | ①`-cover` 输出文件由调用方在**同一脚本作用域**生成并传给检查 7，`$coverFile` 可见；②文件名带 `$PID`（并发跑两次不会互踩），跑完 `Remove-Item` 清理；③CI 调用形态实测等价（见 §二.1）；④JS 纯函数与原内联逻辑逐项对照一致（见 §二.2） |
| 门禁退化风险 | 通过（这是本次最关键的一维） | ①测试红时**不**把覆盖率文件喂给棘轮（`$coverFile = ''`），且 check-depth 自跑模式失败即 `exit 2`；牙齿探针证明最终 `exit=1` 且 check-depth 不给棘轮结论；②`-Fast`（CI gate 的形态）不受影响：不生成文件 → check-depth 自跑一次，与改动前逐字等价；③复用只发生在「同一门禁、同一次运行、同一份测试结果」内，不存在跨次/跨目录复用 |
| 红线 | 通过 | R10：没有新增静默吞错；反之新增的两处「不静默」：`-CoverageStdout` 文件不存在 → `exit 2`，测试红 → `exit 2`。无写入落到仓库根或 `auths/`（覆盖率文件落 `$env:TEMP`）。L-3：README/脚本改动中无「反斜杠+双引号」新增行（检查 3 实测通过） |
| 一致性 | 通过（含一条自纠） | 4 条文档更正都给了可复现命令，且都是**穷尽**型证据（`git grep ... -- '*.go'` 全仓、`go test -count=3` 实跑、`Get-Process` 实查）。**自我修正 1 处**：初稿把「约 89%」照抄进新注释，实测复现不出（425/430 与 405/410 都是 98.8%），已改为实测口径 |
| 可维护性 | 通过 | 纯函数注释写 why（为什么抽出来、为什么不能用真假判断替代 `length`）；新增参数注释写清两种模式的责任划分 |
| 架构合规 | 通过 | 只动 `scripts/`、`internal/panel/`（前端 + 测试）、`internal/session` 注释、文档；无新包、无新依赖、无依赖方向变化（`check-boundaries.ps1` 实测通过） |

## 二、关键实证

### 2.1 CI 调用形态未被改变（最容易砸的一处）

```
$ powershell -NoProfile -File scripts/pre-commit.ps1 -Fast -AllFiles -Base HEAD~1
  [SKIP] -Fast 已跳过测试 …（此时 check-depth 会自跑一次带覆盖率的）
  [OK]   依赖方向检查通过
  [OK]   深度棘轮未回退
exit=0
```

即：CI 的 `gate` job 仍是一条自跑的、不依赖任何外部产物的门禁。`-CoverageStdout` 只是给
**本地全量模式**用的旁路。

### 2.2 JS 纯函数行为等价（逐项对照）

| 输入 | 改动前（内联） | 改动后（`unkBadge` / `unkNoteText`） |
|---|---|---|
| `unknowns` 缺失 / `[]` | `unkTag = ''` | `''`（早返回） |
| 2 条 unknown | `'<span class="tag warn" title="…">2 未知成本</span> '` | 同一字符串（含尾部空格） |
| `question` 含 `<script>` / 引号 | `esc()` 转义 | `esc()` 转义（测试断言 `&lt;script&gt;`、`&quot;`） |
| `accounts` 含 `null` 元素 | **抛 TypeError** → 整页不刷新 | 返回 `''`（新增守卫，测试 M3 覆盖） |

最后一行是本次唯一的行为变更：从「抛错」变成「不显示」。方向是**更安全**（旁支路径抛错会
连带整页停止刷新），且只对畸形输入生效。

### 2.3 反向变异（证明测试有牙齿）

| 变异 | 结果 |
|---|---|
| 去掉 `esc()` | 红 |
| 空态不早返回 | 红 |
| 去掉 `null` 守卫 | 红 |

## 三、问题清单

| 级别 | 位置 | 问题 | 处置 |
|---|---|---|---|
| 🟢 | `scripts/pre-commit.ps1` 检查 7 | 复用模式下若 `$coverFile` 存在但**为空文件**（理论上只可能因写入失败），check-depth 会算出 `minCoverage=0` 并报棘轮回退 —— 方向是 fail-closed（红），不是假绿 | 记录即可。真出现时日志会显示「覆盖率来源：复用 …」+ `0.0%`，可诊断 |
| 🟢 | `internal/panel/frontend_ux_test.go` | `TestAppJSUnknownsBadge` 仍是源码文本级断言，函数改名就会红（脆但便宜） | 保留；行为级覆盖由 `TestAppJSUnknownBadgeAndNote` 承担 |
| 🔵 | `scripts/check-depth.ps1` | 「覆盖率来源」只在指标段打印一行，失败时才有完整上下文的回显 | 不阻塞；失败路径已打印 go test 的 FAIL 行 |
| 🔵 | `docs/PLAN-open-items-2026-10-17.md` §9.4 | 本文件由并行会话同日多次改写，我只**追加**了一段，没有动它的既有结论 | 已在段首标明「同日另一条工作线的收尾」 |
| 🔵 | `.harness/changes/*/summary.md` 的更正段 | 用「引用块 + 日期」形式追加，原文结论保留（可审计），但同一文件里会同时存在新旧两种说法 | 刻意如此：原结论是历史判断，改掉就丢了「当时为什么这么写」 |

## 四、未覆盖（按 L-15：未覆盖 = 未知，不是没有）

1. **浏览器真机的 unknown「有值态」观感未验**：需要一条真实未消解 unknown（面板上的徽章、
   悬停 title 的实际排版）。本轮只到 JS 渲染层（函数输出已钉）+ 空态（`/metrics` 的
   `unknowns_open 0` 佐证）。归 B1（沙箱 + 假上游）。
2. **CI 侧 `test`/`gate` 重复执行的去重未做**：这是 D-2 的**决策**，不是遗漏。
3. **`-race` 未跑**：本机无 gcc（CI 独占），本次未触及并发路径。

---

# 第 2 轮：独立只读复审（子代理，2026-10-17）

> 方法：把 diff 交给一个**只读**的子代理（禁改文件），按同样六维独立复核，重点自查
> 「门禁有没有退化」。结论 **PASS**，但抓出 4 条我第 1 轮自评**没看到**的问题。

## 结论

**PASS**（无 🔴 / 🟡；🟢 4 条、🔵 3 条）。它独立复现了我给的全部数字
（425/430 = 98.84%、`deriveKey` 20/20、剔除后 405/410 = 98.78%、`go list` = 23/20、
`server -count=3` → `ok 25.194s`、`dynamicModelsCache` 零命中、7864 拒连且只有 PID 40912），
并单跑了全量 `pre-commit`（exit 0、单轮测试、`-cover` 输出被复用）。

关键自查项：「测试红仍 PASS」路径不存在（全量红 → `$coverFile=''` → check-depth 自跑再红 → 双红）；
`-Fast`（CI gate 形态，`gate.yml:98`）与改动前逐字等价；空覆盖文件走 `exit 1` **fail-closed**。

## 它发现、我已修的问题（同轮修完，未留到下一轮）

| 级别 | 问题（它的实证） | 处置 |
|---|---|---|
| 🟢-1 | **测试红时临时文件泄漏**：`%TEMP%\yg-precommit-cover-23832.txt`（11:44:09，含 4 行 FAIL）残留；绿路径那份已正确删除。根因：清理判据用了 `$coverFile -ne ''`，而红路径把 `$coverFile` 置空 | 引入**独立**变量 `$coverTmpPath`（红绿都删）；文件名 `$PID` → **GUID**（PID 会复用）。验证：红路径跑完 `%TEMP%` 残留 **0**（修复前 1） |
| 🟢-2 | `Remove-Item -ErrorAction SilentlyContinue` 是本 diff 里**唯一新增的静默吞错**且无注释（正是 -1 没暴露的原因） | 去掉 `SilentlyContinue`，存在性用 `Test-Path` 判；注释写明「删不掉就是真问题，要看得见（R10）」 |
| 🟢-3 | `session.go` 的复现命令 `"-coverprofile=sess.out"` 会把文件落到**仓库根**，且 `pre-commit` 检查 4 的判据 `^\.?(out\|p\|tmp\|cov\|cover\|profile)$` **不匹配 `sess.out`** → 拦不住；与 depth-tasks 里用 `$env:TEMP` 的写法自相矛盾 | 改成 `"-coverprofile=$env:TEMP\sess.out"`，并把「为什么必须落 TEMP」写进注释 |
| 🟢-4 | 注释「本机实测多约 40s」与我自己的 A/B 数字（第二遍 63s / 总差 66.4s）矛盾——正是本需求 P-2 批评的那类未验证数字 | 改为「第二遍就是 63s（A/B 总差 66.4s）」并指向 summary §2.1 |
| 🔵-1 | `check-depth.ps1` 头部「用法 / 退出码」未同步新参数与新语义 | 补 `-CoverageStdout` 用法；退出码 2 的含义写明含「自跑测试红」 |
| 🔵-2 | 复用模式是「数据来源可能错位」的唯一残缝（概率极低） | 加一行防御：复用内容含 `^(FAIL\|--- FAIL)` → `exit 2`。验证：喂一份含 FAIL 的探针文件 → `exit=2` 且打印失败行 |
| 🔵-3 | depth-tasks §四.1 的更正段把「根因是包级 `dynamicModelsCache`」写成观察，实为推断 | 按四态改为 **inference**，并补 provenance：`TestModelsCacheNotSharedAcrossHandlers`（`internal/server/handler_test.go`）钉住「每个 Handler 一份」；明确「没有变异复现证据」 |

## 它提出但我判定不改的

| 项 | 我的判据 |
|---|---|
| `frontend_ux_test.go` 的源码文本级断言（它归为 🟢 类） | 保留：行为级覆盖由 `TestAppJSUnknownBadgeAndNote` 承担，两者互补；文本级那条便宜且能抓「函数被删/改名」 |

## 它无法验证的两项（我补齐了 1 项，另 1 项仍是未知）

1. **改动前 128.4s 的 A/B 基线**：它禁改文件所以没复核 → 我在第 1 轮已实跑
   （`git checkout -- scripts/` 后全量 128.4s vs 新脚本 62.0s），命令与输出见 summary §2.1。
2. **浏览器真机 unknown 有值态观感**：仍未验（同 §四.1），归 B1。
