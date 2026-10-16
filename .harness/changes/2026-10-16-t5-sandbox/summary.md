# 变更总结 — T5 测试沙箱隔离（治 -count=3 全局状态污染 + EWMA 时间敏感），2026-10-16

> 任务书：`docs/superpowers/plans/HANDOFF-2026-10-16-depth-next.md` §3 T5 + §4 坑 8。
> 承接：`.harness/changes/2026-10-16-depth-tasks-4-6/summary.md` 「四、遗留」第 1 条。
> 约束不变：零新功能、**零新依赖**（`go.mod` 直接依赖仍只有 `github.com/redis/go-redis/v9`）、
> 不改上游协议报文数据（R8）、不静默吞错误（R10）、goroutine 有回收路径（R9）、
> 不改 `internal/upstream/`、`internal/panel/`、`cmd/`、`.github/`、`scripts/`。

## 一、两个 flake 的**修复前**证据（先复现，再动手；不是「重跑直到绿」）

### A｜`go test ./internal/server/ -count=3` → `TestModelsEndpoint` 红

```
$ go test ./internal/server/ -count=3
--- FAIL: TestModelsEndpoint (0.00s)
    handler_test.go:862: models count=0
    handler_test.go:871: cn:glm-5.2 missing
FAIL	yg-gateway/internal/server	23.135s
```

用 `-count=3 -v` 定位轮次：**第 1 轮 PASS（0.32s）、第 2/3 轮 FAIL**。
第 1 轮那 0.32s 是**真实上游网络调用**（日志里 enterprise 端点 401、v3/config 端点成功），
即该用例当时把「/v1/models 的组装逻辑」押在了两件与本仓无关的事上：

1. 运行机器能否访问上游（离线/上游抖动即红）；
2. 包级全局 `dynamicModelsCache` 里有没有**同包其它用例**留下的负缓存。

第 2 轮红的机制（读代码可完全解释）：`hardening_test.go` 的 `TestReadOnlyRateLimit` 等
用例的假上游返回空目录 → `fetchDynamicModels` 走失败分支 → 写 `lastFail = now`；
**负缓存 5min > 整个测试进程时长**，于是下一轮所有 `/v1/models` 用例都被短路成空列表。
（`-count=1` 之所以绿，只是因为「第 1 轮」永远碰不到别人留下的负缓存。）

**关键结论（与 T5 原文的假设不同）**：A 是**进程内**的包级全局状态污染，
`TestMain` 沙箱（HOME/cwd/TEMP）只在进程启动/退出各跑一次，**隔离不了它**。

### B｜`TestEwmaLatencyPenalizesSlowInPick` 时间敏感

```
$ go test ./internal/pool/ -count=20 -run TestEwmaLatencyPenalizesSlowInPick
--- FAIL: TestEwmaLatencyPenalizesSlowInPick (0.00s)
    ewma_test.go:261: iter 2: 慢号被罚后 r=0 应选快号, got &{... UID:mid ...}
FAIL	yg-gateway/internal/pool	0.027s
```

机制：三号 credits 相同 → 权重只差「闲置补偿」；旧版用 `time.Now()` 给三号写 lastUsed，
靠 setup 时**自然产生的微秒级先后**打破 fast/mid 并列。Windows 的 `time.Now()` 精度约 1ms：
- 三个 lastUsed 通常落在同一毫秒 → 权重**完全相等** → 由 pick 的稳定兜底（UID 字典序
  `fast` < `mid` < `slow`）决定顺序 → 恰好绿；
- setup 一旦跨越毫秒边界（约 1/8 概率），pick 又把中选号 fast 的 lastUsed 刷新到 `now`
  → 第 2 轮起 mid 的闲置补偿反超 fast → `r=0` 选中 mid → FAIL（实测 `-count=20` 必现）。

## 二、改了什么

| 文件 | 改动 |
|---|---|
| `internal/pool/pool.go` | 新增 `nowFn` 字段 + `Pool.now()` + `Pool.SetClock(fn)`（测试注入时钟；生产默认 `time.Now()`）。注释写明 why（权重含闲置补偿/延迟中位数两项随墙钟漂移）与「只给 pick 系用」的边界 |
| `internal/pool/pick.go` | `pick` / `pickWeighted` 的时刻取 `p.now()`；`pickEarliestExpiryLocked` 的写回用调用方传入的 `now`（与上方 expiry 判定同一时刻）。**零行为变化**（`nowFn==nil` 时逐位等于 `time.Now()`） |
| `internal/pool/ewma_test.go` | `TestEwmaLatencyPenalizesSlowInPick` 改为**注入冻结时钟**：fast/mid lastUsed = base（闲置补偿恒 0）、slow 刻意久置 1h（未罚权重 11.5 反超 11，从而把「被罚后垫底」锁死为延迟惩罚的功劳）；新增 4 处断言（中位数=1000、未罚权重前提、罚后权重对比、写回 lastUsed 必须等于注入时钟） |
| `internal/pool/main_test.go`（新） | 每包测试沙箱：`TestMain → runTestSandbox`，HOME/USERPROFILE/TMPDIR/TMP/TEMP/工作目录 → 本次独占 `$env:TEMP` 子目录，退出清理；两条不变量（R1 守卫 + 逃出即报错）+ 负向探针开关 |
| `internal/server/main_test.go`（新） | 同一实现（逐字副本，除包名/前缀），本包 TestMain 在 `handler_test.go` 里调它 |
| `internal/server/handler_test.go` | ① `TestMain` 改为「关聊天日志 + `runTestSandbox`」；② 新增 `resetDynamicModelsCache()`，5 处 models 用例统一走它；③ `TestModelsEndpoint` 改为「重置缓存 + 假上游固定目录（6 模型 + glm-5.2 全字段）」，去掉活网络依赖，并按契约断言字段透出/空值省略 |
| `.harness/LESSONS.md` | L-3 升级为 **Seen 2**（未加引号的 `-flag=$var` 在 PS 5.1 下同样不展开 → 仓库根出现 `$cov`/`$abs` 字面名 profile，已清理，附探针证据）；§二 flake 表两行改为「已修」 |
| 本文件 | 变更记录 |

**为什么实现成两份 main_test.go 而不是 `internal/testenv`**：零新依赖是硬约束；新增包会让
`go list ./...` 多一层 → `scripts/check-boundaries.ps1` 的白名单以「新层未显式声明」拒绝
（该脚本不在本任务范围），且门禁口径「23 包」会漂移。两份文件除**包名 / 沙箱目录名前缀 /
入口日志标签 / 探针注释里的包路径 / 头部注释** 4 处文本（外加 server 版**少一个 `TestMain`**
——它住在 `handler_test.go`，那里还要关掉聊天表格日志）之外逐字相同；生成/比对脚本
`sync-main-test.ps1` 放在 `%TEMP%`（不入库，见「遗留」第 3 条）。

## 三、怎么验证的

### 3.1 修复后的绿（全部实跑）

| 命令 | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0（无输出） |
| `gofmt -l ./cmd ./internal` | 无输出 |
| `go test ./internal/server/ -count=3` | `ok yg-gateway/internal/server 23.4s` |
| `go test ./internal/pool/ -count=20` | `ok yg-gateway/internal/pool 2.6s`（含 EWMA 那条） |
| `go test ./... -count=1` | **20 包 ok + 3 no test files，0 FAIL**（53.1s） |
| `powershell -NoProfile -File scripts/check-boundaries.ps1` | exit 0 |
| `powershell -NoProfile -File scripts/check-depth.ps1` | exit 0；`最低包覆盖率 71.9%`（= 基线，未回退，未 `-Update`） |
| `powershell -NoProfile -File scripts/pre-commit.ps1`（全量） | exit 0（gofmt/R10/build+vet/test/边界/深度 5 项全 OK） |

### 3.2 R1 守卫有牙齿（负向证据，可复现）

`TestMain` 退出前比对**真实** `auths/`（仓库根）的指纹（每项相对路径+大小+mtime 纳秒；
「从无到有」「从有到无」同样算变化）。负向探针：

```
$ $env:YG_TEST_AUTHS_ESCAPE_PROBE='1'; go test ./internal/pool/ -run TestEwmaLatencyPenalizesSlowInPick -count=1
[sandbox] pool: 沙箱=C:\Users\...\Temp\yg-pool-test-420615662（HOME/cwd/TEMP 已隔离，auths/ 守卫已装载）
[sandbox] ❌ R1 守卫命中：目录 C:\Users\yanha\Desktop\yg-gateway\auths 在本次运行中被新建（此前不存在）
[sandbox] 测试不得写真实凭证目录（R1）：把写入目标改到沙箱（...）或 t.TempDir()。
[sandbox] （本次为负向探针的**预期**命中：守卫有牙齿的证据）
FAIL	yg-gateway/internal/pool	0.033s     ← exit=1
$ Test-Path .\auths → False               ← 探针残留已被守卫清干净
```

探针关闭时同一命令 exit 0。`internal/server` 同样验证通过（exit=1 + 同上消息）。
**即「某个测试真去写 auths/」会让整个测试进程非 0 退出**，而不是只打一条日志。

### 3.3 变异实证（在 `%TEMP%\mut-t5` 一次性副本里做，L-2 纪律：先断言 `go env GOMOD` 指向副本，改前改后校验 SHA256）

| 变异 | 位置 | 结果 |
|---|---|---|
| 删延迟惩罚（`if false {` 替换 `ewmaLatencyMs > median*slowFactor` 判定） | `pool/pick.go` | **CAUGHT**（`--- FAIL: TestEwmaLatencyPenalizesSlowInPick`） |
| 删 `cn:` 前缀（`"cn:" + mi.ID` → `mi.ID`） | `server/handler.go` | **CAUGHT**（`--- FAIL: TestModelsEndpoint`） |
| 绕过注入时钟（`p.now()` → `time.Now()`） | `pool/pick.go` | **CAUGHT**（写回 lastUsed ≠ 冻结时刻） |

三次变异还原后复跑：`ok`（两包），证明副本里除变异外无差异。

### 3.4 沙箱隔离生效的机器证据

`runTestSandbox` 在**入口**断言 cwd/HOME/TEMP 已在沙箱内（不成立即非 0 退出），
入口日志打印沙箱绝对路径；**出口**再次断言（测试若 chdir 出去或覆盖 HOME 即报错）。
沙箱退出即 `RemoveAll`（本机跑完 `%TEMP%` 无 `yg-pool-test-*`/`yg-server-test-*` 残留）。

## 四、EWMA 时钟注入为什么是「行为等价」

- 生产唯一的时钟入口仍是 `time.Now()`：`Pool.now()` 的实现是
  `if p.nowFn != nil { return p.nowFn() }; return time.Now()`，而 `nowFn` 只能由
  `SetClock` 赋值，生产代码路径**没有任何调用点**（与既有 `SetRandomSource` 同纪律：
  「仅供测试注入，生产代码不应调用」）。
- 改动只把 `pick.go` 里两处 `time.Now()` 换成 `p.now()`（第三处改为复用调用方已取的
  `now`，属同一瞬间，语义更一致）；其它文件里的 `time.Now()` 一个都没动
  （它们只做「时长/过期」判定，注入它们只扩大漂移面、零收益——注释里写明了这条边界）。
- 因此 `nowFn == nil` 时，三处取值与改动前**逐位相同**；`go test ./...` 20 包全绿、
  `check-depth` 覆盖率不变，也印证没有行为漂移。
- 反向证据（变异 3）：把 `p.now()` 改回 `time.Now()` 后 EWMA 用例立刻 FAIL →
  证明「注入的时钟真的被 pick 用上了」，而不是摆设。

## 五、怎么回滚

| 层 | 回滚 |
|---|---|
| 本任务全部 | `git revert <本提交>`（一个提交含全部改动，纯增量 + 两处测试重写） |
| 只要沙箱/R1 守卫 | 删 `internal/pool/main_test.go`、`internal/server/main_test.go`；把 `internal/server/handler_test.go` 的 `TestMain` 改回 `os.Exit(m.Run())` |
| 只要时钟注入 | `git revert` 后重放测试部分即可；或单独把 `pick.go` 的 `p.now()` 改回 `time.Now()`（`Pool.SetClock/now()` 留着无副作用） |
| 只要 A 的修 | 把 `TestModelsEndpoint` 改回 `upstream.New()`——**不建议**：那会把活网络与全局负缓存两个 flake 源一起带回来 |

## 六、遗留与不确定项（如实记录）

1. **T5 标题的因果说法要修正**：A 的根因是**进程内**包级全局（`dynamicModelsCache`），
   TestMain 沙箱**治不了**它；本轮「沙箱」与「A 的修」是两件独立的事，各自有独立验收。
   若后续有新包出现同类污染，只有两条路：把全局搬进 Handler（结构性），或每个用例显式重置。
2. **`dynamicModelsCache` 仍是包级全局**：本轮只做到「依赖它的用例必须首行重置」，
   新增用例忘了调 `resetDynamicModelsCache()` 仍会复现 A 类污染。**候选**：把缓存搬进
   `Handler` 字段——属生产结构改动，需单独评估（会影响 `cachedModelsSnapshot()` 的调用方）。
3. **`internal/server` 的 TestMain 与沙箱在两处**（`handler_test.go` 调 `main_test.go` 的
   `runTestSandbox`），两份沙箱文件是**手工同步**的副本（生成脚本在 `%TEMP%`，未入库）。
   两份文件一致性目前靠「逐行 diff 恰好 4 行差异」的人工核对。
4. **R1 守卫是事后检测，不是写保护**：它在进程退出时报错，不能阻止中途写入。
   真要做到「物理不可写」需要 auth 层支持 env 覆盖 auths 根（生产改动，未做）。
   负向探针（`YG_TEST_AUTHS_ESCAPE_PROBE=1`）会**故意**往真实 `auths/` 写一次再自行清理，
   属于永久保留的验证后门——若不想要，删 `escapeProbeEnv` 相关三段即可。
5. **`go test -coverprofile=<相对名>` 的后缀行为未深究**：实测
   `-coverprofile=zzz-cov.out` 落盘为仓库根的 `zzz-cov`（`.out` 被去掉），
   且**与沙箱无关**（无 TestMain 的 `internal/logfmt` 同样如此）。本仓门禁不用
   `-coverprofile`（用 `-cover` 解析 stdout），故未继续追。建议一律用
   `"-coverprofile=$env:TEMP\xxx.out"`（绝对路径 + 双引号）。
6. **LESSONS L-3 的机器检查仍缺**：本轮已升到 Seen 2 并写了可复现探针
   （`cmd /c echo -coverprofile=$x`），但落地检查要改 `scripts/pre-commit.ps1`，
   而 T5 边界明令不许改 `scripts/`；Seen 3 必须落地，别再只加计数。
7. **本机无 gcc → 未跑 `-race`**（CI 独占）。沙箱的 `SetClock` 与 `randInt64N` 同纪律
   （测试注入发生在任何并发 pick 之前），但**没有 race 证据**；两个新增测试文件里
   对 `p.byUID` 的读取按既有惯例显式加 `p.mu`（本任务新增的读都在锁内）。
