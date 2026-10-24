# 评审记录 — depth-ratchet-restore（handler.go 拆分 + cmd/credit 覆盖补足）

- 日期：2026-10-24
- 对象：`a00edc4`（handler 拆分 + cmd/credit 覆盖 2.9%→86.1%）及关联提交 `e0eec54`（gofmt 对齐）
- **独立复审人：独立 agent 会话**（与原实现者无共同记忆，未参与实现）
- 依据：`.harness/rules/CODE-STANDARDS-CHECKLIST.md` §九（L2 需独立复审）+ `.harness/LESSONS.md` **L-16**
  （自评审 PASS ≠ 通过；独立复审另起一人）。本文件由复审人直接落笔，非实现者转述。

## 结论

**PASS**（0 🔴 / 0 🟡 / 2 🟢 / 2 🔵）

两条 🟢 均为 summary.md 的数字措辞不精确（不影响门禁结果与回滚可行性），无需改代码；
详见 §三。

---

## 一、复审方法

1. 读 `.harness/changes/2026-10-17-necessity-stage-and-parity/review.md` 学格式（独立完成）。
2. `git show a00edc4` 逐文件读 diff；`git show e0eec54` 全量读 diff。
3. 机械比对（Python 大括号配对提取函数体做精确字符串等价判断），不靠肉眼。
4. 机器实跑：check-depth 全量、两包测试+覆盖率、全仓 `go test ./...`、vet、gofmt、
   cmd/credit 运行时冒烟。

## 二、逐项复审结果

### （a）handler.go → handler_fallback.go 拆分：纯机械搬移 ✅

| 检查 | 方法 | 结果 |
|---|---|---|
| 函数体逐字节一致 | 大括号配对提取 `fallbackForward`/`forwardToOne`，比对 `a00edc4^:internal/server/handler.go` 与 `a00edc4:internal/server/handler_fallback.go` | **两者精确相等**（含全部注释，零改动） |
| import 删除是否正确 | 扫描拆分后 handler.go 残留 `bytes.`/`io.` 引用 | 仅 2 处**注释**（flushWriter 文档）；代码零引用，删 `bytes`/`io` 正确；其余 8 个 import 逐一验证仍被使用 |
| 调用点完整 | grep 全仓 | `handler.go:723` 仍调 `h.fallbackForward(...)`；`forwardToOne` 仅被 fallbackForward 调用；`newFlushWriter` 留在 handler.go:920、被 handler_fallback.go:93 跨文件引用（同包，合法） |
| 行数对账 | `grep -cv '^\s*$'` | handler.go 非空行 **1014 → 902**（summary 称 ~900 ✓）；handler_fallback.go 128 行 |
| 新增内容 | diff | 仅文件头 4 行拆分原因注释 + import 块（time 单独归组，gofmt 合法）——文档性新增，非逻辑改动 |
| 红线 | diff 全读 | R8 未触碰（无 `wb2api`/UA 指纹字符串改动）；无新增 goroutine（R9）；无新增 `_ =` 吞错（R10，既有 `_, _ = w.Write(respBody)` 原样搬移） |

### （b）cmd/credit 测试是否真测行为：真测，86.1% 不注水 ✅

| 检查 | 方法 | 结果 |
|---|---|---|
| 逃逸手段 | grep `t.Skip` | **0 命中**；`mustWrite` 仅定义一次（main_test.go:273），无重声明 |
| happy path 之外 | 读 6 个测试体 | 错误分支**逐一实测**：http 500 / code≠0 / 坏 JSON / client.Do 连接失败 / glob 元字符退出码 1 / 缺 token `no accessToken` / 损坏文件 Error 行 |
| 断言是否对齐真实逻辑 | 对照 main.go:198-210 聚合代码 | 吻合：remain=250（50+200）、**TotalDosage 下限** size=1000（>360）、**used 派生**=750、Authorization 头、按文件名排序调 fetch（a 先于 b）、JSON 形状 |
| 覆盖率独立复现 | `go test ./cmd/credit -cover -count=1` | `ok ... coverage: **86.1%** of statements`——与 summary 声称完全一致 |
| 未覆盖的 ~14% 是什么 | 结构判断 | 真实网络 wrapper `fetchUserResource` / `main()` / 少量 marshal 错误分支——不可单测或无行为分支，不是靠跳过错例凑出来的 |

### （c）棘轮基线文件与实测一致性：一致 ✅（含 2 条 🟢 措辞偏差）

| 检查 | 结果 |
|---|---|
| 本变更是否碰基线 | `git show --stat a00edc4`：仅 5 个文件，**无** `.harness/depth-baseline.json`——summary「不碰基线文件」属实。基线上次由 `248e316`（2026-10-22，先于本变更）收紧至 maxFileLines 1089 / minCoverage 73.3 |
| 独立实跑 check-depth | `export YG_GO_EXE=<go1.27.3 toolchain>` + `powershell -NoProfile -File scripts/check-depth.ps1` → **exit 0**：oversizeFiles **2** ✓ / minCoverage **73.6%**（≥73.3 基线）✓ / maxFileLines **1088** / maxFrontendLines 4378 ✓ / oversizeFrontendFiles 2 ✓；脚本自报「✅ 未回退，且变好：maxFileLines 1089 → 1088」 |

### （d）commit message 与实际 diff 是否相符：相符 ✅

- `a00edc4` message「handler.go 拆分 fallback 至新文件 + cmd/credit 覆盖 2.9%→86.1%
  （check-depth 未回退）」——diff 恰为这 5 个文件，无一额外；86.1% 经独立复跑复现。
- `e0eec54` message「gofmt 对齐 toolchain 1.27.3，零逻辑改动」——全量读 diff 确认：
  仅 `internal/pool/state.go` Status 结构体字面量的**列对齐空白**变化（19+/19-），零语义改动。
  注意：该提交与本变更主题（拆分/覆盖率）无关，属顺手卫生提交（message 引用的漂移源是
  `eec8be0`），summary.md 未提及它——见 🔵-2。

## 三、问题清单

| 级别 | 位置 | 问题 | 处置建议 |
|---|---|---|---|
| 🟢-1 | summary.md §二 | 「maxFileLines 1088 **与基线一致**」不准：基线是 **1089**（248e316 收紧值），实测 1088 是**优于基线 1 行**（check-depth 脚本自己打印「1089 → 1088 变好」）。门禁结论不受影响 | 下次顺手改措辞为「1088 ≤ 基线 1089」 |
| 🟢-2 | summary.md §二 | 「minCoverage **73.5%**」，本轮独立实测 **73.6%**。两者均 ≥73.3 基线，0.1pp 差异疑为后续提交（2a877eb 等）所致；不构成回退 | 记录备查即可 |
| 🔵-1 | handler_fallback.go import 块 | `time` 单独归组（与 stdlib 主组之间隔空行）——gofmt 合法、vet 干净，仅风格上略突兀 | 不要求改 |
| 🔵-2 | e0eec54 / summary.md | 关联的 gofmt 提交在变更目录内**零记载**（summary 未列 e0eec54）；其 message 自述准确、diff 零逻辑，风险为零 | 后续顺手提交建议在 summary 附一行提及，便于追溯 |

## 四、机器验证记录（复审人独立实跑，2026-10-24）

```
工具链：C:\Users\yanha\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.27.3.windows-amd64\bin\go.exe
        （go version go1.27.3 windows/amd64）

powershell -NoProfile -File scripts/check-depth.ps1   → exit 0（六指标全过，见 §二(c)）
go test ./cmd/credit/ ./internal/server/ -count=1 -cover
        → ok yg-gateway/cmd/credit    0.692s  coverage: 86.1% of statements
        → ok yg-gateway/internal/server 7.968s coverage: 77.3% of statements
go test ./... -count=1                                → exit 0（仅 cmd/signin、cmd/trial no test files，符合预期 21 ok + 2）
go vet ./cmd/credit/ ./internal/server/               → 干净
gofmt -l ./cmd/credit ./internal/server               → 无输出
go run ./cmd/credit（冒烟）                            → exit 0，输出合法 JSON（空 auths 场景）
```

## 五、未覆盖（未覆盖 = 未知，不是没有）

1. **pre-commit 10/10 全量总闸**：summary「残留」第一条自记「本改动提交后重跑」。本轮复审
   复跑了其中 5 项（test/vet/gofmt/boundaries 间接经由 check-depth/深度棘轮），但**未整跑**
   `scripts/pre-commit.ps1` 全量总闸——该项仍挂账，须由实现者在后续提交时闭环。
2. **summary 所述验证环境**（`GOTOOLCHAIN=go1.27.2` + 临时 modfile）无法事后复现当时现场；
   本轮全部验证用的是 go1.27.3 直跑，两者结论一致（86.1% 可复现），原现场差异不构成风险，
   但严格意义上属 unknown。
3. **check-depth 的覆盖率口径**：73.6% 为本轮实测；不同提交间 minCoverage 有 ±0.1pp 漂移
   （见 🟢-2），棘轮余量仅 0.3pp（73.3 基线），后续任一包覆盖率微降即可能复红——这是
   棘轮设计使然，非本变更缺陷，但下一个动 cmd/credit 或低覆盖包的人应知悉。
