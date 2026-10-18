# 变更记录 — 变异核验扩展到 internal/pool（承重墙，A6 增量）

- 日期：2026-10-18
- 分级：**L1**（纯核验 + 工具修正；无生产代码改动）
- 计划：`docs/PLAN-open-items-2026-10-17.md` 的 **A6**（变异核验穷尽度，「改到哪条分支补哪条」）
- 工具：`scripts/mutation/mutation-check.ps1` + `scripts/mutation/mutations/pool.json`

## 一、为什么是 pool

`internal/pool` 是「养号友好型选号」的承重墙：积分保底、在途上限、健康过滤、请求级轮换。
这些分支写错的后果不是报错，而是**静默把号用坏 / 超发 / 反复打同一个失败号**——
属于「不报错但更危险」那一类。此前它和其它四个包一样只有「自述」。

## 二、结果：6 条变异，6 条被真测试咬住

| ID | 变异 | 咬住它的测试 |
|---|---|---|
| P1 | 保底判据 `credits >= floor` → `credits > 0`（保底形同虚设） | `TestCreditFloorBlocksPaidBelowFloor` |
| P2 | 实测台账判收费 `CostPer1k > 0` → `>= 0`（免费模型也被拦） | `TestCreditFloorAllowsFreeBelowFloor` |
| P3 | `pick` 不再跳过已试过的号（请求级轮换失效） | `TestPickExcluding` |
| P4 | 目录倍率判收费 `v > 0` → `>= 0` | `TestCreditFloorUnobservedFreeModelStillAllowed` |
| P5 | 去掉在途占满判定（同号可被超发） | `TestPickSkipsInFlightFull` |
| P6 | 去掉健康过滤（冷却/熔断/余额耗尽的号也参与） | `TestModelCooldownsPickSkipsLimitedModel` |

**边界**：这 6 条同样是我读完代码挑的（选择偏差），且只覆盖同步单测层；冷却时长、EWMA 衰减、
持久化择新、并发交错都没打（后两类需要 `-race`，本机无 gcc）。

## 三、这一轮暴露并修掉的两个**方法学缺陷**（比 6 条结果更重要）

1. **编译失败曾被记成「咬住」**。P3 的第一版把 `tried[uid]` 判据改成 `&& false`，而 `uid` 在
   该函数里只有这一处用法 → `declared and not used: uid` → `[build failed]`。驱动因为只看
   `$LASTEXITCODE -ne 0`，把「代码根本编译不过」当成了「测试覆盖到」。
   **修法**：驱动新增 `无效变异` 判定（命中 `[build failed]` / `declared and not used` /
   `undefined:` / `cannot use`），编译失败一律不算咬住。P3 已改成可编译的等价变异
   （`tried[uid] && false`），现在由 `TestPickExcluding` 真咬住。
2. **`go test` 的 stderr 曾被当成脚本异常**。`$ErrorActionPreference='Stop'` 下，原生命令往
   stderr 写（编译错误）会抛 `NativeCommandError`，脚本直接中断而不是记录结果——第一次跑 P3
   就是这样断在半路的。**修法**：调用期临时降级 `Continue`，异常清理仍在外层 `try/finally`。

顺带把退出码写明确：全咬住 → `0`；有存活/无效 → `1`（此前会泄漏最后一次 `go test` 的 1）。

## 四、回滚

无生产代码改动；工具修正是幂等的。回滚 = `git revert`。