# 变异核验 — panel 达标未领/领奖口径（A7/D3 的深度补课，A6 剩余项）

- 日期：2026-10-19
- 分级：**L1**（只加测试与核验资产，不改产品行为）
- 清单：`scripts/mutation/mutations/panel-claim.json`（8 条，首次入库）
- 驱动：`scripts/mutation/mutation-check.ps1`（本轮新增 `(cached)` 判据）

## 一、为什么要做

A6 的旧结论是「panel 只做 5 条临时变异，没有入库清单」——那 5 条既不可复跑，也没有覆盖
A7/D3 这次真正改掉的东西。`disposeTask` + `claimRewardFor` 是**两条入口共用的一条判定**
和**三个调用点共用的一个领奖出口**，这种「收敛点」最适合变异核验：一处改坏，行为会静默地
在另一条入口上出错（正是原来那个 bug 的形状）。

## 二、清单覆盖（8 条）

| id | 变异 | 想钉住的行为 |
|---|---|---|
| PA1 | 达标判据 `>=` → `>` | 恰好达标（5/5）必须算「达标未领」，不能退化成「未达标」去跑动作 |
| PA2 | 锁定任务不再跳过 | 每日解锁窗口的 locked 任务不该被自动化尝试 |
| PA3 | 无自动化动作的任务也进处置 | 入队后 `autoActionFor` 为 nil 会直接报错 |
| PA4 | `growthPending` 用 `== taskRun` | 入队口径必须与处置口径同源（达标未领仍要入队去领） |
| PA5 | mp 领奖降到 Web 域 | mp 任务缺 `X-Client-Platform` 头就领不到 |
| PA6 | 队列侧不领奖 | 回到 A7 原 bug：一键完成领、队列不领 |
| PA7 | 领奖失败原因被折掉 | 失败必须带 `claim_error` 与上游状态码 |
| PA8 | 达标未领缺 `claimable` 标记 | 前端靠这个字段打「可领」信号 |

## 三、首轮：6 咬住 / **2 存活**（真缺口）

```
包 internal/panel：咬住 6 / 存活 2 / 无效 0     exit 1
PA7 存活  达标未领领奖失败时把原因折掉
PA8 存活  一键完成的达标未领项不再标 claimable
```

两条存活都是**我这轮新写的 claim-only 分支**没有测到的部分：

- `autoAllClaimOnly` 的失败路径（`cerr != nil`）此前无任何断言——把原因折掉也能绿；
- 达标未领项的 `item["claimable"]=true` 没有断言——去掉它前端就收不到「可领」信号。

## 四、补测试 → 二轮 8/8

新增 `TestClaimOnlyFailureIsVisible`（一键完成带 `claim_error` + 文案含「领奖失败」+ 状态仍 `done`；
队列侧文案同款且不中断），并在一致性用例里补 `claimable` 断言。

```
包 internal/panel：咬住 8 / 存活 0 / 无效 0     exit 0
PA7 咬住  | --- FAIL: TestClaimOnlyFailureIsVisible
PA8 咬住  | --- FAIL: TestTaskEntryPointsAgreeOnClaim
```

副本工作区干净（8 条变异全部还原）。

## 五、复现

```powershell
$clone = Join-Path $env:TEMP 'mut-panel'
git clone --quiet . $clone
powershell -NoProfile -File scripts/mutation/mutation-check.ps1 `
  -Clone $clone -MutsFile scripts/mutation/mutations/panel-claim.json -Pkg internal/panel
# 期望：咬住 8 / 存活 0 / 无效 0，exit 0
```

## 六、驱动改动（LESSONS L-17 的机器检查）

`mutation-check.ps1` 新增：`go test` 输出含 `(cached)` ⇒ 判 **无效变异**。
起因是 2026-10-18 我在错误的 workdir 做过一次 ad-hoc 变异，读到原树 + 缓存，得到「变异全绿」的假象。

> **诚实边界**：这条判据在 `-count=1` 下不会触发，本轮**没构造出真实的缓存命中场景**，
> 因此该分支属**未覆盖（unknown）**，不写成「已验证生效」。它的价值是防止后人去掉 `-count=1`
> 或改调用方式时把「未生效」读成「存活」。

## 七、回滚

删 `scripts/mutation/mutations/panel-claim.json` 与 `TestClaimOnlyFailureIsVisible` 即可；
产品代码未改动。

---

## 八、第二批：终态分支（`panel-autotask.json`，7 条）

第一条清单钉的是「判定与领奖口径」，这一批钉的是 `runAutoAll` / `runGrowthQueued` 的**终态分类**——
把失败读成成功的那些分支（都是「不报错但语义反转」的形态）。

| id | 变异 | 首轮 |
|---|---|---|
| AX1 | 「该账号无此任务」`skipped` → `done` | **存活** |
| AX2 | 任务查询失败 `error` → `skipped` | 咬住（`RunAutoAllListErrorItem`） |
| AX3 | 动作失败 `error` → `done` | 咬住（`RunAutoAllActionErrorItem`） |
| AX4 | 批量接受失败 `error` → `skipped` | 咬住（`RunAutoAllDefaultBatchAcceptError`） |
| AX5 | 回读去掉 `after.Claimable` 条件 | 咬住（`TaskEntryPointsAgreeOnClaim`） |
| AX6 | 队列侧查询失败改成 `return "", nil` | 咬住（`RunGrowthQueuedClaimAndQueryErrors`） |
| AX7 | 队列侧回读失败仍继续领奖 | 咬住（`TaskEntryPointsAgreeOnClaim`） |

**AX1 是第一轮的真实缺口**：`TestPanelAutoTaskRunAutoAllSkipsTerminalAndAbsent` 只断言了
「该账号无此任务」这句**文案**，没断言 `status`——于是「缺任务」被写成 `done`（看起来像已经做完）
也能绿。补 `status == skipped` 后：

```
包 internal/panel：咬住 7 / 存活 0 / 无效 0     exit 0
```

两批合计 **15 条入库变异（8 + 7），全部咬住**。复跑命令同上，把 `-MutsFile` 换成
`scripts/mutation/mutations/panel-autotask.json`。
