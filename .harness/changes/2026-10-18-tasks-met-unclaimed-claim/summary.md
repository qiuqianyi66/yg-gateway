# 变更记录 — 「达标未领」两条入口统一为「去领奖」（A7 / D3，L2）

- 日期：2026-10-18
- 分级：**L2**（奖励领取行为变更：一类任务从领不到变成领到）
- 需求：`docs/prd/REQ-tasks-met-unclaimed-claim-2026-10-18.md`

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `internal/panel/taskcenter.go` | 新增 `taskDisposition`（`taskSkip`/`taskRun`/`taskClaim`）+ `disposeTask` 唯一判定；`growthPending` 改为 `disposeTask(t) != taskSkip`；新增 `claimRewardFor`（mp/web 领奖唯一出口）；`runGrowthQueued` 改为按处置分支执行（达标未领：领奖一次，不跑动作） |
| `internal/panel/autotask.go` | `runAutoAll` 的 `skipped` 判定改为 `switch disposeTask(...)`，新增 `taskClaim` 分支（达标未领 → 只领奖）；三处领奖调用统一走 `claimRewardFor` |
| `internal/panel/dispose_task_test.go` | **新增**：`disposeTask` 表格 8 条 + 两条入口一致性测试（达标未领/已领取/未达标 × 一键完成/队列） |
| `internal/panel/coverage_autotask_test.go` | 三处旧期望更新为新语义（旧期望正是本条要改掉的行为），断言强度不变：仍钉「领奖 1 次 + chat 域 + mp 头」 |

## 二、为什么（一句话）

同一个任务状态，走队列能领到奖、走「一键完成」被 `skipped` 领不到，两条路径都不报错——积分白丢。

## 三、怎么验证的

- `go test ./internal/panel/` → **ok 50.8s**（全包 1 次跑完）。
- 新增 8 条 `disposeTask` 表格（已领/锁定/无动作×2/达标/超目标/未达标/目标为 0）+ 3 组一致性用例。
- 改了 4 处旧期望，逐条说明：`runAutoAll` 达标项由 `skipped` → `done + claimed`；队列 mp 达标项由「动作自领 + 追加文案」→「直接领奖一次」；队列双账号用例第 1 项文案随之更新。

## 四、牙齿（`%TEMP%\mut-dispose` 副本，与工作区同源）
| 变异 | 结果 |
|---|---|
| M1 `disposeTask` 达标分支 `return taskClaim` → `return taskSkip` | **红**：表格 达标/超目标 2 条 + 一致性「领奖次数 = 0, want 1」 |
| M2 `runGrowthQueued` 的 `case taskClaim:` 插早退 | **红 4 组**：`RunGrowthQueued*` 各子用例（动作/领奖顺序错位） |
| M3 队列侧 `p.claimRewardFor(a, code)` → 常量 0（等价于不领奖） | **红**：`两条入口领奖次数不一致：一键完成=1，队列=0` |

M3 是这条待办的核心断言：它红出来的正是原来那个 bug 的形状。

> 门禁拦下一次回退：首版把 `taskClaim` 分支内联在 `runAutoAll` 里，`check-depth` 报
> `oversizeFuncs 7 → 8`（`runAutoAll` 越过 120 行）。**没有放宽阈值**，而是把领奖分支抽成
> `autoAllClaimOnly`——这正是「抽函数变可测/变短」而不是「改阈值」的用法。

## 五、回滚

`git revert <本提交>` → 回到「一键完成跳过达标未领 / 队列先跑动作再领奖」的旧行为。

## 六、注意

- 本轮**没有**改 `autoActionFor` 的动作实现，也没改 `ClaimReward*` 的报文（R8 口径不触）。
- 「达标未领但上游 claim 返回 0 分 0 能」的文案从「奖励此前已领取」改成「奖励为 0 或此前已领取」（原文案在奖励为 0 的任务上是假断言）。
