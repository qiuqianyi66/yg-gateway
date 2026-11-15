# 独立复审记录 — 2026-11-15-refactor-depth-batches

> 本仓纪律（LESSONS L-16）：自评审 PASS ≠ 通过，须独立复审。本文件记录**独立复审者**
> （隔离上下文的子 agent，只读，禁止改文件）对 `fc54879..HEAD` 的核对结论与我的处置。

## 一、复审设置

- 范围：B0 度量修正 / B1 `normalize()` 拆分 / B2 `StreamHint` → `streamRun`，外加 REQ 与 summary。
- 基线：`git show fc54879:<path>`（未被改写，可直接复现）。
- 方法：机器比对两轮（多重集 + 有序子序列）+ 13 条词法探针 + 全仓新旧对拍 + 终点人工抽检 10 个函数。
- 复审者实跑：`go vet ./...` exit 0；`go test ./internal/upstream ./cmd/server -count=1` 两包 ok。

**前置提醒（复审者提出、我确认）**：本轮提交号被重写过一次（`e653185/bf30fef/946dc59`
→ `674b185/a3f6367/c5fd618`），任何按旧 hash 引用的复核都会解析失败。树内容逐字节相同
（`git diff --stat <旧> <新>` 为空），故结论不受影响；引用请用新 hash 或 `fc54879` 基线。

## 二、结论

**A（config 拆分）、B（SSE 状态所有权）、C（词法器）均未发现行为漂移。**

- A：旧 `normalize()` 体 148 条代码行在「仅旧」侧 **0 条**（无丢失语句）；唯一顺序违例
  `CreditFloor` 一位，已证**不可观测**（`RpmPerAccount`/`ConcurrencyPerAccount` 从 pool 段中段
  移入 `normalizeUpstreamLimits`，但两者是纯钳制、无 error 返回、只写 `c.Upstream.*`，与 pool 段
  写入字段零交集）；11 个 error 产生点的先后逐条对齐无变化。
- B：15 项状态全部成字段、非零初值未丢（`maxToolIdx: -1`、`toolCallSeen: map[int]bool{}`）；
  指针接收者 + `go vet`（含 copylocks）exit 0 → 无值拷贝误捕；锁口径四点（`writeMu` 仅心跳启用、
  心跳内联写不调 `writeThrough`、`[DONE]` 走 `writeThrough`、`defer` 与提前返回的相对位置）
  与旧版逐条一致；`validFrames` 5 个计数点逐点对齐。
- C：13 条词法探针（`"}{"`、反引号原生串、`'{'`、`"a\"b{"`、行尾 `// }`、结构体 tag、
  行内 `/* } */`、`'\u007b'`、行尾除号、跨行块注释、跨行原生串）全部符合 Go 词法；
  短路跳过安全（被跳过的行不可能改变 `$inRaw/$inBlock`）；全仓 126 个函数计数变化**全部是缩小
  方向**，无一个放大；4 个超限 + 6 个缩水函数的终点人工核对正确。

## 三、findings 与处置（3 条 low，无 blocker/high/medium）

| # | 复审发现 | 判定 | 处置 |
|---|---|---|---|
| 1 | `scripts/check-depth.ps1` 无函数体声明的兜底**不成立**：bodyless func 后跟带花括号的 `type` 时，那个 `{` 会把 `$seenOpen` 顶真、深度回 0，`$e` 被钉在 type 的闭合行 —— 幽灵吸收原样复活 | **成立**（我原注释「深度永远回不到 0」是错的）。现网零触发（`//go:linkname` 命中 0，5 处多行签名都有函数体），latent | **已修**：扫描前加拦截——`$seenOpen` 仍为假时遇到下一个顶层声明（`^(func \|type \|const \|var )`）即 `$e = $i-1` 并跳出。合成探针验证：bodyless func 后接 `type Ghost struct{...}`，正文收在声明前一行（旧实现会吞到 `}`）；真实仓库指标不变（`oversizeFuncs` 仍 4、exit 0） |
| 2 | `internal/upstream/stream.go` 空流兜底帧的注释写「走 `hintFn=nil` 的直写路径」，与实现相反（实际经 `writeRaw` → 咨询 `s.hintFn`） | **成立**。是 `fc54879` 原文件的逐字搬运，**非本次漂移**，但注释描述的机制不存在 | **已修注释**（不改行为）：写明「本包没有 hintFn=nil 的写路径」、真实行为是 hintFn 对该自造 payload 判不出 ErrKind 而返回空串（推断）、以及将来若要**保证**不带 hint 必须换写路径 |
| 3 | `summary.md` 三处对拍数字与实测不符 | **成立**（我抄错）：新口径 `CoolKind.String()` 实测 **9**（非 8）；`processLine` 收在 **229** 行 / **21** 代码行（非 228/8，228 是 0-based 索引误当行号）；「吞掉文件其后全部内容」过重——被 `nextStop` 封顶，实测只多吞 1 行 | **已改**为实测值，并写明「多吞到下一个顶层函数之前（多吞 1 行）」 |

## 四、复审提出但不改的项

- **`B0` 提交信息里的「真身 8 行」**：应为 gate 口径 **9 行**。提交信息不改写（历史以本文件为准）。
- **复审者建议的兜底做法**「bodyless func 时不进深度循环」与我采用的做法等价（我选「进循环但拦截
  下一个顶层声明」，因为它同时覆盖「签名后紧跟 //go:linkname 注释行」的形态），不重复实现。

## 五、本轮未覆盖（unknown，不是「没问题」）

- **`-race` 未跑**：本机无 gcc（CI 独占）。B2 只搬移锁的所有权、未新增并发路径，因此无竞态回归
  测试，证据是静态等价 + 既有测试全绿。
- **空流兜底帧的 hint 判定未独立断言**：复审者明确标为 inference；仓库没有「空流帧无
  gateway_hint」的测试（`sse_test.go` 只断言 message/type）。要消解需补一条断言。
- **`Get-CodeOnly` 未做词法器级正确性证明**：13 条探针 + 全仓对拍是样本证据，不是全称证明。
- **`pre-commit.ps1` 的 R10 检查在「`-Base` + 工作区脏」时会假红**（本轮实测）：行号取自
  `git diff <base>...HEAD`，解释注释却从**工作区文件**读取；工作区比 HEAD 多几行时行号错位，
  于是「注释明明在正上方」也会被判未论证。提交后（工作区干净）即恢复。修法：`-Base` 模式下
  注释也应从 `git show <base>:<path>`/HEAD 的对应版本读，或直接在工作区脏时拒绝运行。
