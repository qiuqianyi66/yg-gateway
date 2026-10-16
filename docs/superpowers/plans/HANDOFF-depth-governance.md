# HANDOFF — 工程深度治理（Task 3 完成，Task 4 过半，Task 5/6 未开始）

> 生成于 2026-10-15。当前 HEAD = `e210511`。
> 本文件是**交接文档**；执行方案本身在 `docs/superpowers/plans/2026-10-15-engineering-depth-hardening.md`
> （注意：该目录**未提交**，仍是 untracked，但文件在工作区里可直接读）。

---

## 一、任务状态总览

| Task | 状态 | 提交 |
|---|---|---|
| Task 1 度量脚本 + 基线固化 | ✅ 完成 | `1fac012`，算法修正 `80431d0` |
| Task 2 gofmt 漂移清零 | ✅ 完成 | `d80bd70` |
| 插入：防会话死锁修复（toolCallNameEmpty） | ✅ 完成 | `68fb053` |
| **Task 3 chatCompletions 拆分为 5 阶段** | ✅ **完成** | 见下 9 个提交 |
| **Task 4 错误处理审计** | ⚠️ **前半完成** | `e210511` |
| **Task 5 覆盖率补强** | ❌ **未开始**（仅测了基线） | — |
| **Task 6 规范固化为机器检查** | ❌ **未开始** | — |

### Task 3 的完整提交链

```
f4afe55  test: chatCompletions 行为等价快照（7 场景）  ← 先建保护，再动刀
3d64fa6  refactor: 阶段⑤ 末端错误构造 → pure 函数
844d4f3  refactor: 阶段① 准入后读取+解析+校验
d45fd50  refactor: 阶段② 键派生 与 阶段③ 出站准备
ee0cd92  refactor: 5 个闭包方法化为 chatRun（阶段④ 前置）
bf4d688  refactor: 阶段④ 轮转主循环（415 行）
1fc7eb2  refactor: 抽出 serveStream / serveNonStream
f746d8f  refactor: 抽出 selectAccount（三态控制流）
6fb5bd2  refactor: 抽出 handleErrorPath（四态控制流）
80431d0  fix(scripts): check-depth 只统计代码行
```

**成果数字**：

| 指标 | 前 | 后 |
|---|---|---|
| `chatCompletions` | 847 行 | **128 行**（纯编排） |
| `handler.go` | 2051 行 | ~1344 行 |
| `oversizeFuncs` | 17（旧口径）/ 8 | **7**（棘轮已抬高） |

新增文件：`internal/server/chatflow.go`（阶段①②③⑤函数）、`internal/server/chatrun.go`（运行期状态载体）。

拆完后的 server 包函数体量：
```
rotateAndServe   34 行     selectAccount      64 行
handleErrorPath 108 行     serveStream        83 行
serveNonStream   49 行
```

---

## 二、Task 4 的实际进度（重要：不是"完成"）

**已做**：
- Step 1 导出忽略点：实测 **123 处**（方案里写的 121 是估计值）
  分布：类型断言 6 / 写响应与清理 ~60 / JSON 兜底 ~20 / 其他 ~37
  按包：upstream 75 / server 23 / panel 18 / login 5 / pool 4 / 其余少量
- Step 2 判据已写入 `.harness/rules/ERROR-HANDLING.md`（已提交）
- Step 3 修复了 **1 处真吞错误**：`internal/panel/autotask.go` 专家召唤链的
  `fail` 计数累加后从未使用，靠 `_ = fail` 压制编译器检查 → 失败对运维不可见。
  已改为 WARN 日志（不改返回值格式）。

**未做**：
- Step 3 剩**其余"不可忽略"候选的逐条判定**（我只深查了 3 处代表性样本，
  其余 ~34 处"其他"类还没逐条过）
- Step 4 给"容易被误读为疏漏"的 `_ =` 补注释（pool 状态迁移附近等）
- 判据接入 pre-commit（那是 Task 6 的一部分）

**判据摘要**（细节见 ERROR-HANDLING.md）：
- 可忽略：① 写响应后无法补救 ② 清理路径 ③ **错误会在紧邻下一步被检出** ④ 值已被上游检查过
- 不可忽略：① 影响持久化 ② 影响状态机推进 ③ 影响出站内容 ④ 影响计量
- 灰色处置：先问"忽略它，失败会以什么形式被看见？"答不上来就按不可忽略处理
- **`_ = <标识符>` 形态是异味**（与 `_ = f()` 性质不同，后者正当）

---

## 三、Task 5 覆盖率：目标与基线（2026-10-15 实测）

| 包 | 基线 | 目标 |
|---|---|---|
| **upstream** | **56.8%** | **≥70%**（+13.2pp） |
| **panel** | **13.6%** | **≥40%**（+26.4pp） |
| logfmt | 33.3% | +10pp |
| redisstore | 16.8% | +10pp |
| prompt | 41.3% | +10pp |
| cmd/login | 24.8% | +10pp |
| cmd/server | 48.6% | +10pp |
| auth | 68.8% | +10pp |
| session | 67.2% | +10pp |
| scheduler | 67.5% | +10pp |
| cmd/{credit,signin,trial} | 0.0% | 豁免（check-depth 已豁免 0.0% 包） |

**已定位的 upstream 零覆盖纯函数**（最易入手，建议从这里开始）：

```
blackcat.go:20   InNightWindow
blackcat.go:27   BlackcatNeed
client.go:209    SoftRateResetLoc
client.go:381    isAllDigits
client.go:397    parseRetryNumber
client.go:745    globalChatBase
client.go:753    globalBillingBase
client.go:47     String            (20.0%)
```
其余 blackcat.go 的 `RunNightChats` / `ClaimGift` / `ClaimCompensation` /
`HeatmapYesterdayMissed` / `UseMakeupCard` 需要 HTTP 桩，成本较高。

测量命令：
```powershell
& $go test ./internal/upstream/ -count=1 -coverprofile=cov.out
& $go tool cover -func=cov.out | Select-String '0\.0%'
```

---

## 四、Task 6（未开始）

把规范固化为机器检查。方案原文要点：把 `.harness/rules/编码规范.md` 的
body-size/structure 约束 + Task 4 的 `_ =` 判据接入 `scripts/pre-commit.ps1`。

**注意**：`.harness/rules/CODE-STANDARDS-CHECKLIST.md` 已存在（已提交），先读它，
避免重复建设。

---

## 五、环境与命令（新会话必读）

```powershell
$go = "C:\Program Files\Go\bin\go.exe"
$env:GOPROXY='https://goproxy.cn,direct'; $env:CGO_ENABLED='0'
```

| 事项 | 说明 |
|---|---|
| **`pwsh` 不存在** | 只有 Windows PowerShell 5.1 → 用 `powershell -NoProfile -File x.ps1` |
| 无 gcc | `-race` 不可用；并发正确性靠设计 + 单测（需要时用 `-count=3` 代替） |
| PS 5.1 引号陷阱 | 原生参数里的 `'` / `,` 会被吃掉 → 复杂模板写进文件传（如 `go list -f`） |
| 门禁四件套 | `go build ./...` / `go vet ./...` / `gofmt -l ./internal ./cmd` / `go test ./... -count=1` |
| 边界 | `powershell -NoProfile -File scripts/check-boundaries.ps1`（退出码 0 才过） |
| 深度棘轮 | `powershell -NoProfile -File scripts/check-depth.ps1`（`-Detail` 看明细，`-Update` 抬高棘轮） |
| 行为快照 | `go test ./internal/server/ -run TestChatCompletionsBehaviorSnapshot -count=1` |

---

## 六、本轮踩过的 6 个坑（新会话务必避免重踩）

1. **`return` 作用域变化**：把循环体搬出主函数时，`return` 从"退出整个请求处理"
   变成"只退出该方法"，控制流会继续往下走 → 对**已写好的响应**再构造一次错误。
   症状极隐蔽：body 正常（已 flush），但 Content-Type 被覆盖成 application/json。
   （快照测试若只断言 body 会漏掉——断言响应头才是关键）

2. **`switch` 里的 `break` 只跳出 switch，不跳出 for**。把循环控制流搬进 switch
   时 break 的目标变了 → 退化成"继续下一轮"，导致轮转翻倍 + 泄漏在途名额。
   **用 `if` 链承接 outcome 码**。

3. **"解构-回写"手法对只读状态安全，对会被改写的状态会造出第二份真相**。
   `body`/`degradedApplied` 被降级路径改写时，局部副本改了但 run 字段没变
   → 下一轮出站与 fallbackForward 都看不到新值。**活状态只存一份**。

4. **`break`/`continue` 的 tab 缩进不统一**（实测有 3/4/5 三种）。机械替换的正则
   要匹配任意缩进，否则漏改 → 编译报 "break is not in a loop"。
   可靠写法：`^(\t+)(break|continue)(\s*//.*)?$`

5. **gofmt 漂移**：改完立即 `gofmt -w` 自己改的文件。

6. **`_ = <标识符>` 是异味**（与 `_ = f()` 不同）——详见第二节。

---

## 七、已知既有问题（与本次治理无关，别误判为自己引入）

- **`go test ./internal/server/ -count=3` 会让 `TestModelsEndpoint` 失败**：
  测试间全局状态污染。已用 `git stash` 对照验证**改动前后行为完全一致**。
  单独跑该测试通过。留待 Task 5 一并处理。
- **`TestEwmaLatencyPenalizesSlowInPick` 偶发失败**（约 1/8）：时间敏感
  （`setLastUsed(time.Now())` + 毫秒边界），单独跑通过。

---

## 八、工作区卫生（重要）

**其他并行会话的未提交改动，绝对不要 `git add -A`，也不要覆盖**：

```
 M HANDOFF.md
 M README.md
 M internal/panel/app.js
 M internal/panel/panel.go
?? .harness/rules/COST-CONTROL.md
```

本会话的产物一律**按显式路径** `git add`。

**另注**：`docs/superpowers/` 整个目录仍是 untracked（含正在执行的方案文档）。
如果希望方案可追溯，应单独提交——但注意它可能是别的会话创建的，先确认归属。

---

## 九、下一步建议顺序

1. **Task 5 先行**（收益最明确）：从第四节列出的 upstream 零覆盖**纯函数**入手，
   先补 `blackcat.go` / `client.go` 那几个（易测、无 HTTP 依赖）。
   然后 panel 13.6% → 40%（需要研究它的测试形态，方案说"前端切片测试"）。
2. **Task 4 收尾**：把"其他 ~37 处"逐条过一遍判据，补关键位置注释。
3. **Task 6**：读 `.harness/rules/CODE-STANDARDS-CHECKLIST.md` 后，把判据接入
   `scripts/pre-commit.ps1`。
4. 每步都要：改完跑四件套门禁 + `check-depth` 不回退，**独立提交**。

---

## 十、本方案的硬约束（不可违反）

- **零新功能、零新依赖**
- **重构必须行为等价**，由 `TestChatCompletionsBehaviorSnapshot`（7 场景）保护
- **不把闭包局部状态提升为包级变量**（那是假重构）
- **每阶段独立提交**，且每条提交时 `go test ./... -count=1` 必须全绿
- **棘轮门禁不许放水**：`check-depth.ps1` 的阈值只能收紧不能放宽；
  若因口径修正需要重算基线，必须给出"真阳性保留 + 假阳性消除"的证据
