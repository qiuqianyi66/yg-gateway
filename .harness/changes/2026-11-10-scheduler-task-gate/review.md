# 代码审查 — 098c0c1（scheduler 同族任务互斥，P2-5）

## 问题清单（Findings）

未发现由本次变更新引入且需要报告的问题。

---

## 审查范围与方法

- 范围：`11856ab..098c0c1`，7 文件 +272（生产代码 4 个：`scheduler.go`、`travel.go`、
  `blackcat.go`、`snapshot.go`；新增测试 1 个；变更凭据 2 个）。
- 判据：本地深度技能库 `doubao-coding-review-code`（defect-first、完整 diff 覆盖、
  最短证据链、只报当前可触发且证据闭合的缺陷），叠加 `expert-reviewer` 的反向门限与假阳性清单。

## 候选账本（每个候选的终态）

| 候选 | 核验路径 | 终态 |
|---|---|---|
| 锁顺序导致死锁 | 各入口在函数**第一行**取 gate，之后才碰 `schedMu`/账号锁；`runBatch` 派发时不持任何锁；`RunCheckinNow` 末尾调的 `RunStreakBonusNow` 无 gate（`streak.go:21`），不存在嵌套取同族锁 | rejected |
| 面板并发触发 streak 绕过 | 全仓搜 `RunStreakBonusNow` 调用点：仅 `scheduler.go:572`（已在 checkin 锁内）与测试；`panel` 包无该入口（grep 零命中） | rejected |
| `gate` 越界返回空锁是后门 | 7 个 `taskKind` 常量值 0..6，`taskGateSlots=8`，越界分支当前不可达；保留它是为零值/未来 kind 不被误锁死 | rejected |
| 余额刷新双路径漏锁 | `StartBalanceRefresh` 的 ticker 与面板手动都收敛到 `RunBalanceRefreshNow`（`scheduler.go:795` / 面板入口），`balanceGate.TryLock` 在函数首行；无嵌套 | rejected |
| growth 族未上锁 | `runBatch` 的 `taskGrowth` 走 panel 侧 `GrowthHook` 异步回调（`scheduler.go:494-501`），其并发由 growth 客户端自身机制负责，且本次未改该路径——pre-existing 不属本次 finding | rejected |
| 测试因错误原因通过 | 逐条核对：`calls==1` 证明跳过不发请求、`maxInflight==1` 证明真串行、空闲后 `calls==2` 证明不是永久拒绝；空锁变异实测 5.02s 报红（`手动触发 5s 未返回`） | rejected |

## 验证缺口

- 未跑 `-race`：本机无 gcc（AGENTS.md 已记），并发正确性只能靠单测 + CI 的 race 作业。
- 生产侧只验证了部署冒烟（健康 2/4、非流式/流式全过）；同族互斥在生产环境的真实触发
  （排程与手动按钮撞窗口）尚未在生产观测到，属 unknown，不推断为「已验证」。

**verdict：pass**
