# 变更记录 — 夜猫子间隔改为可注入（N4）+ 拆出 promo.go 给棘轮腾空间

- 日期：2026-10-16
- 计划：`docs/superpowers/plans/HANDOFF-2026-10-16-ci-closed.md` §3 N4
- 前置：T4（`wb2api-night-*` golden 覆盖）——那份把夜猫子上报形态钉住了，代价是要真跑 `RunNightChats`
- 级别：L2（改生产代码的可注入点 + 一处纯搬家；不改对外行为、不改依赖方向、不新增依赖）

## 一、问题

`RunNightChats` 每成功一次就 `time.Sleep(4 * time.Second)`，且**不可注入**。T4 的 golden
覆盖了 `wb2api-night-*`（此前无人断言），代价是每次 `go test ./...` 都要真付这段等待：

| 位置 | 代价 |
|---|---|
| `TestUpstreamHTTPRunNightChatsSuccess`（HTTP 形状用例） | 4.00s |
| `TestR8ProtocolGolden`（golden 生成器会真跑一次夜猫子对话） | 4.00s |
| **合计** | `internal/upstream` 单轮 **11.05s**（改前基线；本包本应 ~3s） |

这个 sleep 又**不能直接删**：连续发对话会被上游当成脚本行为，4s 是实测可用的节奏。
删它是拿风控观感换测试速度——测试要的是「不用真等」，所以修法必须是**可注入**。

## 二、改了什么

### 2.1 生产代码

| 文件 | 改动 |
|---|---|
| `internal/upstream/blackcat.go` | `4 * time.Second` 字面量 → 常量 `nightChatGap`；新增 `SetNightChatSleep(fn func(time.Duration))`（仅供测试注入，生产代码不应调用）与 `sleepNightChat()`；`RunNightChats` 改调 `sleepNightChat()` |
| `internal/upstream/client.go` | 新增字段 `nightChatSleep func(time.Duration)`（nil = 生产默认 `time.Sleep(nightChatGap)`）；**并拆走 promo 块**（见 §2.3） |
| `internal/upstream/promo.go` | **新增**：从 `client.go` 逐字搬来的模型促销块（`v3ModelPromotion` + `promoZone`/`promoClock`/`promoActive`/`applyModelPromotions`，136 行） |

**为什么用「注入等待实现」而不是「注入一个 gap 时长」**：注入的函数**会收到生产默认时长**，
于是测试既能跳过真实等待、又能断言「默认值没被改小」——一条用例同时钉住两件事。
若只注入时长，测试就只能断言「我传了多少」，默认值本身没人守着。

**等价性**：`RunNightChats` 的等待次数与位置**逐位不变**（含最后一次成功后也等——本变更**故意**
不顺手省掉它，那是另一件事，不该夹在测试提速里做）；成功/失败/中断路径的返回语义不变。

### 2.2 测试代码

- 新增 `internal/upstream/blackcat_test.go`：
  - `TestNightChatsGapIsInjectable`：注入记录型等待 → 断言**等待 2 次、每次恰好 4s**，
    且 2 次对话总耗时 < 1s（若注入形同虚设需 ≥8s）。两条断言互为约束，防单边假绿。
  - `TestNightChatSleepNotPreInjectedInProductionConstruction`：`New()` 与字面量构造的
    `Client` 都不得预置等待实现 —— 防「为了跑得快在 New() 里塞空实现」，那会让 4s 节奏
    在生产里静默消失。
- 两处付 4s 的既有用例改为注入空等待（`coverage_sequences_test.go` 的
  `TestUpstreamHTTPRunNightChatsSuccess`、`r8_golden_test.go` 的 golden 生成器），
  并各自注明「生产默认间隔由 blackcat_test.go 单独钉住，因此这里跳过等待不会让节奏失去覆盖」。

### 2.3 顺带的纯搬家：`promo.go`（这条是被门禁逼出来的，如实记录）

加了 `nightChatSleep` 字段后，门禁报：

```
❌ 深度指标回退（1 项）：
   REGRESSED: maxFileLines 2185 → 2190
严禁通过放宽阈值或 -Update 掩盖回退。
```

- **判据口径**：`check-depth.ps1` 的 `maxFileLines` **不含 `*_test.go`**，所以「加个测试文件」不算，
  但 `client.go`（最大生产文件，非空行 2185）**多 5 行就顶破**。任何生产文件加行都会撞上它。
- **反应**：按脚本给出的修复方向（「拆分超长函数/文件」）把 `client.go` 里一块**内聚**逻辑
  整块搬出去 —— 模型促销时段（`v3ModelPromotion` 类型 + 时段判定 + 挂载），其调用面只有
  `fetchV3Models` 一处，无状态、无跨包依赖。
- **验证「纯搬家」**：用已提交版本做基准逐行比对（`git show HEAD:... | cmd 重定向` 取字节级原文，
  避免 PowerShell 重定向重编码导致行错位——第一次复验就是这么错的）：
  `✅ 搬家正文与 HEAD 版本逐行完全一致（136 行）`。
  这里**不用** `Set-Content`/字符串拼装写文件，而是程序化抽取原行区间后直接拼接，避免手抄。
- **由此收紧棘轮**（`-Update`，只抬高不放松）：`maxFileLines 2185 → 2058`，
  `minCoverage 71.9% → 72.3%`。跑 `-Update` 前先确认**当前无回退**（脚本明说它会掩盖回退）。

## 三、怎么验证的

### 3.1 RED → GREEN

```
RED   go vet ./internal/upstream/
      vet.exe: internal\upstream\blackcat_test.go:37:4: c.SetNightChatSleep undefined
      (type *Client has no field or method SetNightChatSleep)

GREEN go test ./internal/upstream/ -run TestNightChat -count=1 -v
      --- PASS: TestNightChatsGapIsInjectable (0.00s)
      --- PASS: TestNightChatSleepNotPreInjectedInProductionConstruction (0.00s)
```

### 3.2 耗时（本任务的目的）

| | `go test ./internal/upstream/ -count=1` |
|---|---|
| 改前 | **11.052s**（`TestUpstreamHTTPRunNightChatsSuccess` 独占 4.00s） |
| 改后 | **2.876s**（wall 12.2s → 4s） |

优于计划里写的目标（「单轮回到 ~7s」）。

### 3.3 golden 仍有牙齿（反向变异）

不能因为「跳过了等待」就让 T4 的覆盖变松。反向变异 = 把 `blackcat.go` 里的
`"wb2api-night-%d-%d"` 改成 `"wb2api-NIGHT-%d-%d"`：

```
--- FAIL: TestR8ProtocolGolden (0.00s)
    r8_golden_test.go:327: R8 golden 与当前代码不一致：第 152 行起有差异，共 1 行不同
          golden:   RunNightChats 上报 conversationId         = wb2api-night-<digits>-0
          当前  :   RunNightChats 上报 conversationId         = wb2api-NIGHT-<digits>-0
```

变异**逐字还原**后立刻回绿（`ok yg-gateway/internal/upstream 0.058s`）。
注意：抓住它的是**比对器**（`TestR8ProtocolGolden`），不是
`TestR8GoldenHoldsCriticalLiterals`——后者读的是磁盘上那份 golden，
所以在「代码改名」这个方向上它**没有**牙齿（它防的是「golden 被重写成空」）。

### 3.4 其它

| 项 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` | ✅ exit 0 / 无输出 |
| `gofmt -l ./cmd ./internal` | ✅ 无输出（搬运后出现过一次「import 后缺空行」，用 `gofmt -w` 修掉） |
| `go test ./... -count=1` | ✅ 20 包 ok + 3 包无测试文件 |
| `check-depth.ps1` | ✅ 未回退，且已收紧（见 §2.3） |
| `check-boundaries.ps1` | ✅ 通过（同包搬家，不新增跨包引用） |
| `-race` | 未跑（本机无 gcc，由 CI 独占）。`nightChatSleep` 是**装配期写入、运行期只读**的字段，与 `HTTP`/`ChatBaseCN` 等同族字段同一约定 |

## 四、怎么回滚

```powershell
git revert <本变更的提交>
```

生产侧无动作（间隔时长未变，只是取值的来源从字面量变成常量，且多了一个注入点）。
若只想回滚搬家部分：把 `promo.go` 的内容搬回 `client.go`（`git show <本提交>^:internal/upstream/client.go`
是搬家前的原文），同时把 `depth-baseline.json` 的 `maxFileLines` 撤回 2185——**但那时
`nightChatSleep` 字段会再次顶破棘轮**，所以这两件事实际上绑在一起。

## 五、执行中作出的裁决

- **等待次数保持「含最后一次」**：省掉末次等待能再省 4s（生产上每次夜猫子任务少 4s），
  但那是行为变更、不是测试提速。本变更不夹带。**代价（若判断错）**：夜猫子任务每次多等 4s，
  直到有人单独提出这件事（已记为本轮观察，建议进 N6 的候选清单）。
- **`promo.go` 的搬家用脚本程序化抽取而非手抄**：手抄 136 行必出错，且错了看不出来
  （编译还能过）。**代价（若判断错）**：脚本的边界假设（行号 + 首末行内容双重校验）
  若被后续编辑破坏，会直接 throw 而不是静默错搬。
- **顺手收紧棘轮（`-Update`）**：拆完就该把上限落到新实际值，否则下次谁往 `client.go` 加
  100 行都不会被拦。**代价（若判断错）**：上限变严，未来想在 `client.go` 里加行的人
  被迫先拆分——这正是棘轮想要的。
