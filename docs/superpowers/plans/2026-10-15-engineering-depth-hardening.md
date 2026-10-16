# 工程深度治理 Implementation Plan

> **状态（2026-10-16 收口）：Task 1–6 全部完成**，本文下方的 checkbox 全部勾选。
> 证据：提交链 `fb0290c`…`d382968`（8 个提交）+ `.harness/changes/2026-10-16-depth-tasks-4-6/summary.md`
> （含四件套/门禁/变异测试/回滚方式）。**下一步任务不在本文**，见
> `docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3（T1–T10）。
> 阅读注意：下方「基线」「Global Constraints」是 **2026-10-15 的历史快照**（当时 18 包、client.go 2185 行、
> handler.go 2051 行、gofmt 漂移 4 个），**不是当前值**——当前为 23 包、gofmt 零漂移。
>
> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 yg-gateway 从"功能宽度已足、工程深度不足"改造成"深度可度量、可守护、可持续"——**不新增任何功能**，只做结构、规范与质量门禁。

**Architecture:** 四阶段递进：①先让深度**可测量**（脚本 + 基线数据 + 门禁），②再重构**唯一的最大热点**（`chatCompletions` 871 行 → 5 个阶段函数 + 编排），③补**错误处理与覆盖率**的深度，④把规范**固化为机器检查**。每阶段独立可交付、可回退；全程用"行为等价测试"保护，不做"顺手优化"。

**Tech Stack:** Go 1.27.2、PowerShell 5.1、`go test -cover`、`gofmt`、`go vet`（本机无 gcc，-race 不可用 → 用 `-count=N` 重复执行替代）

**Spec:** 本计划即 spec。基线数据为本轮**实测**（非估计），见下方"基线"节。

## 基线（2026-10-15 实测，改进后逐项对比）

| 指标 | 当前值 | 说明 |
|---|---|---|
| 生产代码 | 28,670 行 / 197 文件 / 18 包 | 测试 24,530 行（比 0.86） |
| **>1000 行文件** | **5 个** | client.go 2185、handler.go 2051、autotask.go 1210、sse.go 1054、config.go 1014 |
| **>120 行函数** | **17 个** | 最大 `chatCompletions` **871 行**；`main()` 384；`StreamHint` 374；`CoolKind.String()` 308 |
| **包覆盖率 <70%** | **7 个** | panel **13.6%**、redisstore 16.8%、cmd/login 24.8%、logfmt 33.3%、prompt 41.3%、cmd/server 48.6%、**upstream 56.7%** |
| gofmt 漂移 | **4 个文件** | config.go / httpauth/index.go / panel/panel.go / upstream/**hint.go** |
| 静默忽略错误 | **121 处** | sse.go 25、tool_pairing.go 13、handler.go 12、payload.go 10 |
| TODO/FIXME | 10 处 | |
| 包级可变全局 | 12+ 处 | 含 autotask.go 三个可调 gap（测试用） |

**判据**：宽度维度（功能数、包数）已不弱于任何同源/竞品项目；**深度维度的短板集中在"函数与文件的体量"和"核心层覆盖率"**。

## Global Constraints

- **零新功能**：本计划不添加任何用户可见能力；所有改动都是结构/规范/门禁
- **零行为变更**：既有 21 包全绿是底线；重构必须行为等价（用等价性测试证明，不靠"看起来一样"）
- **零新依赖**：不引入 linter/框架；度量用标准库 + 现有工具
- Go 工具链 `C:\Program Files\Go\bin\go.exe`（≥1.27.2），`CGO_ENABLED=0`
- 红线继续有效：R8（协议数据不改名）、R10（错误不静默吞）、R15（替换二进制前备份）
- `scripts/check-boundaries.ps1` 退出码必须保持 0

## Review Focus

1. **重构不得夹带行为变更**：这是本计划最大风险。拆分 `chatCompletions` 时，任何"顺手修一下"都必须单独提交并单独验证；重构 commit 的 diff 应尽量只含"移动+签名调整"。
2. **等价性必须有证据**：不能只说"测试全绿"。要对**同一组请求**在重构前后比对响应特征（状态码/code/body 关键字段/日志行），像既有 `TestViewEquivalenceWithBytePaths` 那样逐位对照。
3. **闭包依赖是拆分难点**：`chatCompletions` 内有 6 个闭包（`releaseHeld`/`unbindSticky`/`fail`/`recordAttempt`/`reqHasImage`/`st.done`），共享外层变量。拆分时必须设计显式的状态载体（`chatRequest`），**不能用更多包级变量绕过**（那会把局部复杂度转成全局复杂度，是假重构）。
4. **覆盖率要防"凑数"**：补测试必须是**有牙齿的行为测试**（还原 bug 必 FAIL），不接受为凑百分比而写的"调用即断言非 nil"。
5. **门禁不许降级**：如果某阈值当前达不到，门禁应记录"当前值 + 允许不回退"（ratchet），而不是把阈值调到刚好通过。

---

### Task 1: 深度度量脚本 + 基线固化

**为什么先做这个**：没有度量，"深度"就是主观词。这个脚本让每次改动都能回答"深度是变好了还是变差了"。

**Files:**
- Create: `scripts/check-depth.ps1`
- Create: `.harness/depth-baseline.json`（基线快照，供 ratchet 对比）

**Interfaces:**
- Produces: 控制台报告 + 退出码（0 = 未回退，1 = 有指标回退，2 = 脚本自身失败）
- Consumes: `go test -cover`、`gofmt -l`、源码文件遍历

- [x] **Step 1: 实现四个度量项**

  | 度量 | 算法 | 阈值来源 |
  |---|---|---|
  | `maxFileLines` | 生产 `.go` 文件行数最大值 | 基线 + ratchet |
  | `oversizeFiles` | >1000 行文件数 | 基线 + ratchet |
  | `oversizeFuncs` | >120 行函数数 | 基线 + ratchet |
  | `minCoverage` | 各包覆盖率最低值（排除 `cmd/*` 与明确豁免项） | 基线 + ratchet |

  函数行数用「`^func ` 到下一个 `^func ` 或文件末」计算——**与 Go 语法不完全等价**（含注释与空行），但作为**趋势指标**足够，且实现简单、无依赖。这一点必须写进脚本注释，避免被当成精确值。

- [x] **Step 2: 实现 ratchet 语义**

  读 `.harness/depth-baseline.json`：
  - 若无基线文件 → 以当前值写入基线，退出 0（首次运行即固化）
  - 若有 → 逐项对比：任一指标**变差** → 输出 `REGRESSED: <指标> <基线> → <当前>` 并退出 1
  - 任一指标**变好** → 提示可否更新基线（`-Update` 才写，避免静默放水）

- [x] **Step 3: 首次运行固化基线**

  Run: `powershell -NoProfile -File scripts/check-depth.ps1`
  Expected: 生成 `.harness/depth-baseline.json`，内容与本计划"基线"节一致。

- [x] **Step 4: 验证 ratchet 有牙齿**

  临时在任一生产文件尾部加 200 行注释，重跑 → Expected: 退出码 1 且报 `REGRESSED: maxFileLines`。然后还原。

- [x] **Step 5: 提交**

  ```bash
  git add scripts/check-depth.ps1 .harness/depth-baseline.json
  git commit -m "chore: 工程深度度量脚本 + 基线固化（ratchet 门禁）"
  ```

### Task 2: 规范漂移清零

**Files:**
- Modify: `cmd/server/config.go`、`internal/httpauth/index.go`、`internal/panel/panel.go`、`internal/upstream/hint.go`

- [x] **Step 1: 逐个确认漂移性质**

  Run: `gofmt -d <file>` 看清是**纯格式**（缩进/空行）还是有语义差异。若发现语义差异 → **停下来单独讨论**（这不是本任务范围）。

- [x] **Step 2: 执行 gofmt 并复核 diff**

  ```powershell
  gofmt -w cmd/server/config.go internal/httpauth/index.go internal/panel/panel.go internal/upstream/hint.go
  git diff --stat   # 必须只是格式变化；若出现逻辑行改动，回退该文件排查
  ```

- [x] **Step 3: 全量回归**

  Run: `go build ./... && go vet ./... && go test ./... -count=1`
  Expected: 全绿（格式变化不应影响任何行为）

- [x] **Step 4: 提交**

  ```bash
  git commit -am "style: 清除 gofmt 漂移（4 文件）"
  ```

### Task 3: `chatCompletions` 拆分（本计划核心）

**为什么**：871 行、承担准入/解析/派生/改写/轮转/错误构造六件事，是全仓最重的一处。它同时是**改错风险最高**的地方——本轮 11133 分类修复时就发生在这段代码里，因为"分类"与"轮转""错误构造"三段逻辑交织在一起。

**Files:**
- Modify: `internal/server/handler.go`
- Create: `internal/server/chatflow.go`（新阶段函数）
- Create: `internal/server/chatflow_test.go`（行为等价测试）

**Interfaces（拆分的目标形状）**

```go
// chatflow.go
type chatRequest struct {          // 阶段 ①–③ 的产物载体（显式状态，替代闭包共享）
    w, r     http.ResponseWriter, *http.Request
    reqView  session.RequestView
    body     []byte
    modelStr, bareModel, upstreamModel string
    realm    string
    authKey  *httpauth.Key
    sessKey, turnKey, stickyUID string
    declaredTools map[string]bool
    chatMeta upstream.ChatMeta
    st       *chatStat
    // ... 仅列关键字段
}

// 阶段 ①：准入 + 读取 + 单次解析 + 模型/Key 校验。失败即写响应并返回 ok=false。
func (h *Handler) prepareChatRequest(w http.ResponseWriter, r *http.Request) (*chatRequest, bool)

// 阶段 ②：会话键 / 轮级键 / 粘性绑定 / 在途租约与释放闭包。
func (h *Handler) (cr *chatRequest) deriveSession()

// 阶段 ③：提示词改写 + model 重写 + 会话头族。
func (h *Handler) (cr *chatRequest) prepareOutboundBody()

// 阶段 ④：轮转主循环（415 行，最大块；再按"选号 / 出站 / 响应写回"细分）。
func (h *Handler) (cr *chatRequest) rotateAndServe() (handled bool)

// 阶段 ⑤：末端错误构造（fallback → 透传 → hint → modelBlock 覆盖）。
func (h *Handler) (cr *chatRequest) writeFinalError(lastErr error)

// 编排：主函数收到 30 行以内。
func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) { ... }
```

> 这正是原函数注释里**已有的**五段边界（804–911 / 920–1010 / 1088–1139 / 1141–1556 / 1557–1650），不是新设计的划分。

- [x] **Step 1: 先写行为等价测试（重构前）**

  在 `chatflow_test.go` 加 `TestChatCompletionsBehaviorSnapshot`：用一组**覆盖各分支**的请求打到 handler，断言响应特征。至少覆盖：

  | # | 场景 | 断言要点 |
  |---|---|---|
  | 1 | 正常非流式 200 | status + body.choices[0].message.content + 关键 header |
  | 2 | 正常流式 | status + 帧序列（含 `[DONE]`）+ header 先于首帧 |
  | 3 | 全局准入超限 | 429 + code |
  | 4 | body 非法 JSON | 400 + code（走 `viewOK=false` 路径） |
  | 5 | body 超限 | 413 |
  | 6 | Key realm/model 不允许 | 400 + code |
  | 7 | 全池不可用 | 503 + `no_healthy_account` + hint |
  | 8 | 上游 4xx 透传 | status + 上游原文 + hint |
  | 9 | **11133（本轮新修）** | 400 + `bad_params` + 上游原文（**钉住本轮修复不被重构破坏**） |
  | 10 | 模型级阻塞 | 400 + modelBlock 覆盖后的 code/msg/hint |
  | 11 | fallback 命中 | 转发结果 |
  | 12 | 带图 + 模型不支持 | hint 指向换模型 |

  断言方式：把每个场景的 `(status, code, 关键字段, hint)` 组成**规范化快照**（排除时间戳/随机 ID），与测试内期望逐字段比对。

- [x] **Step 2: 运行确认快照测试通过（重构前基线）**

  Run: `go test ./internal/server/ -run TestChatCompletionsBehaviorSnapshot -count=1 -v`
  Expected: 12 个场景全 PASS ——**这就是重构前的行为基线**。

- [x] **Step 3: 逐阶段抽取，一阶段一提交**

  每阶段：抽出函数 → `go test ./internal/server/ -run TestChatCompletionsBehaviorSnapshot -count=1` 必须仍全 PASS → 提交。**顺序必须先易后难**：
  1. 先抽阶段 ⑤（末端错误构造，最内聚、无闭包依赖）
  2. 再抽阶段 ①（准入+解析，返回结构体）
  3. 再抽阶段 ②③（上下文派生 + body 准备）
  4. 最后抽阶段 ④（轮转循环，最难——含全部闭包）

  阶段 ④ 的闭包处理原则：
  - `releaseHeld`/`unbindSticky`/`fail` → 变成 `chatRequest` 的方法（状态从"闭包捕获"变为"结构体字段"）
  - `recordAttempt`/`reqHasImage` → 变成纯函数 + 显式入参（`reqHasImage` 的惰性缓存放进 `chatRequest` 字段）
  - **禁止**把任何局部状态提升为包级变量

- [x] **Step 4: 每阶段后同一提交内确认深度指标改善**

  Run: `powershell -NoProfile -File scripts/check-depth.ps1 -Update`
  Expected: `oversizeFuncs` 递减、`maxFileLines` 可能暂时上升（新文件）——这是可接受的，因为**函数深度优先于文件数量**。

- [x] **Step 5: 全部完成后验证编排函数体量**

  Run: 手工确认 `chatCompletions` 主函数 ≤ 40 行（只做编排），且 `go test ./... -count=1` 全绿。

### Task 4: 错误处理深度审计（121 处静默忽略）

**为什么**：R10 说"错误不静默吞"，但仓内有 121 处 `_ =` / `, _ :=`。**绝大多数是合理的**（如 `w.Write` 失败无可挽回、测试清理），但必须有**明确判据**区分"合理忽略"与"真吞错误"，否则 R10 是空话。

**Files:**
- Create: `.harness/rules/ERROR-HANDLING.md`（判据）
- Modify: 审计中判定为"真吞错误"的点（预计 3–10 处）

- [x] **Step 1: 导出全部忽略点并按包分组**

  Run: 用本计划提供的 grep 命令产出清单（见基线节的统计方式），逐条贴进审计表。

- [x] **Step 2: 定判据（写进 ERROR-HANDLING.md）**

  | 类别 | 判据 | 处置 |
  |---|---|---|
  | **可忽略** | ① 写响应后无法补救（`w.Write`/`Flush`）；② 清理路径（`Close`/`Remove` 失败不影响主流程）；③ 值已被上游检查过（如已知非 nil） | 保留 `_`，但**必须**有紧邻注释说明为什么可忽略 |
  | **不可忽略** | ① 影响持久化正确性（`SaveAtomic`/`Save`）；② 影响状态机推进（pool 状态迁移）；③ 影响出站内容（body 改写/注入）；④ 影响计量（usage 记账） | 必须处理（返回/记录/降级），不得 `_` |

- [x] **Step 3: 逐条审计并修复"不可忽略"项**

  对每条判定为不可忽略的：改为显式处理（记录 WARN + 继续，或返回错误），并**加一行注释说明忽略它会怎样**。

- [x] **Step 4: 给可忽略项补注释**

  审计中确认可忽略的，若缺注释则补一句（形式：`// 忽略：<原因>，不影响 <什么>`）。这一步**不追求全覆盖**（121 处全补注释会产生噪音 diff），只补**容易被误读为疏漏**的那些（如 pool 状态迁移附近的 `_ =`）。

- [x] **Step 5: 全量回归 + 提交**

  Run: `go build ./... && go vet ./... && go test ./... -count=1`
  ```bash
  git commit -am "refactor: 错误处理审计——修正不可忽略的静默吞错，固化判据"
  ```

### Task 5: 核心包覆盖率补强（有牙齿的测试）

**目标**：`upstream` 56.7% → ≥70%；`panel` 13.6% → ≥40%（前端切片测试）；其余 <70% 的包各至少 +10pp。

**Files:**
- Modify: `internal/upstream/*_test.go`、`internal/panel/*_test.go`、`internal/logfmt/*_test.go`、`internal/prompt/*_test.go`
- Create: `internal/upstream/cache_key_test.go`（若缺）

- [x] **Step 1: 用 `-coverprofile` 定位未覆盖的关键分支**

  ```powershell
  go test ./internal/upstream/ -coverprofile=cov.out -count=1
  go tool cover -func=cov.out | Where-Object { $_ -match '0.0%' }
  ```
  挑**关键路径**（出站 body 构造、错误分类、header 构造、模型目录解析）而非易覆盖的边角。

- [x] **Step 2: 每个新测试必须有牙齿**

  每个测试在提交说明里写清："还原 <什么 bug> 会让本测试 FAIL"。参照既有 `TestViewEquivalenceWithBytePaths` 的思路——**对照式断言**优于"断言非 nil"。

- [x] **Step 3: panel 覆盖率靠前端切片测试**

  参照既有 `frontend_test.go` 的模式（提取 `app.js` 片段 + 严格 stub 执行），补关键函数的切片测试（如成本格式化、时间范围计算）。**不引入浏览器依赖**。

- [x] **Step 4: 更新覆盖率基线**

  Run: `powershell -NoProfile -File scripts/check-depth.ps1 -Update`
  Expected: `minCoverage` 提升并被记入基线（此后不许回退）。

- [x] **Step 5: 提交**

  ```bash
  git commit -am "test: 核心包覆盖率补强（upstream/panel/logfmt/prompt），全部有牙齿"
  ```

### Task 6: 规范固化为机器检查

**为什么收尾做**：规范写在文档里没人看；只有变成**退出码**才有约束力。

**Files:**
- Modify: `.harness/rules/编码规范.md`（补"体量与结构"章节）
- Create: `.harness/hooks/pre-commit`（或 Windows 等价脚本 `scripts/pre-commit.ps1`）

- [x] **Step 1: 在编码规范里补"体量与结构"硬约束**

  | 约束 | 阈值 | 理由 |
  |---|---|---|
  | 单函数行数 | ≤120（超限必须注释说明为何不可拆） | 超过则难以在脑中保持上下文，改错率高 |
  | 单文件行数 | ≤800（同上） | 同源竞品 2185/2051 行的文件全仓最难改 |
  | 单函数圈复杂度 | 建议 ≤15（无工具时以"嵌套层级 ≤4"近似） | |
  | 新增包级可变全局 | 必须注释说明并考虑替代 | 全局可变是隐藏耦合源 |
  | 新文件必须 gofmt 干净 | 零容忍 | |

- [x] **Step 2: 写 pre-commit 检查脚本**

  内容：`gofmt -l`（新改动文件零漂移）+ `go build` + `go vet` + `go test`（受影响包）+ `check-boundaries` + `check-depth`。任一项失败即拒绝提交。

  > Windows 无原生 pre-commit hook 时，提供 `scripts/pre-commit.ps1` 并写进 `.harness/rules/开发流程规范.md` 作为**提交前必跑**的一步（与既有"部署前必须备份"同级）。

- [x] **Step 3: 验证脚本有牙齿**

  故意引入一个 gofmt 漂移文件 → 跑脚本 → Expected: 退出码非 0 并指出文件。还原。

- [x] **Step 4: 提交**

  ```bash
  git commit -am "chore: 编码规范补体量约束 + 提交前检查脚本"
  ```

---

## 执行顺序与依赖

```
Task 1（度量+基线）──→ Task 2（漂移清零）──→ Task 3（chatCompletions 拆分）
                                                 ↓
                            Task 4（错误审计）──→ Task 5（覆盖率）──→ Task 6（固化门禁）
```

- Task 1 必须最先（后续所有 Task 都用它的指标验收）
- Task 3 是核心，且风险最高 → 放在度量建立之后、其他改动之前
- Task 4/5 相互独立可并行
- Task 6 最后（把前三者的成果固化成不可回退的门禁）

## 预期产出

| 交付物 | 验收 |
|---|---|
| `scripts/check-depth.ps1` + 基线 | ratchet 门禁可拦截回退（已用"加 200 行"验证） |
| gofmt 漂移清零 | `gofmt -l ./cmd ./internal` 输出为空 |
| `chatCompletions` 拆分 | 主函数 ≤40 行；12 场景行为快照测试全绿；`oversizeFuncs` 17 → ≤10 |
| 错误处理判据 | `.harness/rules/ERROR-HANDLING.md` + 不可忽略项已修 |
| 覆盖率提升 | upstream ≥70%、panel ≥40%、最低包 ≥40% |
| 规范门禁 | 提交前检查脚本可拦截漂移 |

**明确不做**：不加功能、不搬上游特性、不引入新依赖、不改协议行为。
