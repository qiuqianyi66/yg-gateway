# 2026-11-10-scheduler-task-gate 变更摘要

需求：`docs/prd/REQ-2026-11-10-scheduler-task-gate.md`
分级：L2（业务逻辑：同族任务并发重入 → 上游计次调用翻倍）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：internal/scheduler/scheduler.go` | `Scheduler` 新增 `taskGates [taskGateSlots]sync.Mutex`（按 `taskKind` 下标取锁，定长数组零值即可用，测试里的 `&Scheduler{}` 也不会 panic）与 `balanceGate sync.Mutex`；新增 `gate(k, name)`：`TryLock` 失败即跳过并记一行日志，越界 kind 放行不拦；`RunCheckinNow` / `runActivity` / `runKeepalive` / `RunBalanceRefreshNow` 接入 |
| `修改：internal/scheduler/travel.go` | `RunTravelNow` 接入 `taskTravel` |
| `修改：internal/scheduler/blackcat.go` | `RunBlackcatNow` 接入 `taskBlackcat`（夜间窗口校验在前，窗口外仍直接返回） |
| `修改：internal/scheduler/snapshot.go` | `RunSnapshotNow` 接入 `taskSnapshot`（目录未配置时仍直接返回） |
| `新增：internal/scheduler/task_gate_test.go` | 在 `/v2/report` 上把两趟真的叠起来：排程那趟卡在请求里 → 手动触发必须立刻跳过（calls 仍为 1）；放行后手动触发恢复（calls=2）；并发峰值恒为 1 |

**没做的（明写）**：
- 不排队、不重试：撞上就跳过（排队会在前一趟结束后立刻补跑第二趟，等于串行重复）。
- 不给 `RunStreakBonusNow` 上锁：它只由 `RunCheckinNow` 末尾调用（已在 `taskCheckin` 锁内），
  面板无该入口（`panel.go` 实测 5 处 `RunXxxNow`：checkin/travel/activity/keepalive/balance）。
- 不改 `runBatch` 并行派发：不同族仍并行。
- 锁结构没用最初写的 `sync.Map`（taskKind → `*sync.Mutex` 惰性建锁）：全量门禁的「新增行无
  未论证的静默忽略」把 `v, _ := taskGates.LoadOrStore(...)` 判红——`LoadOrStore` 第二个返回值
  虽是 `loaded bool` 不是 error，但 R10 的形态检项不接受 `_`。改定长数组后同一语义、无返回值丢弃。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 新用例 | `go test ./internal/scheduler/ -run TestTaskGateSerializesSameFamily -count=5` | 全绿（0.07s） |
| 整包 | `go test ./internal/scheduler/ -count=2` | ok 39.1s（含原有 30+ 用例） |
| 覆盖率 | `go test ./internal/scheduler/ -cover` | 94.7% |
| 静态检查 | `go vet ./internal/scheduler` / `gofmt -l internal\scheduler` | 0 / 无输出 |
| 全量门禁 | `scripts/pre-commit.ps1` | 11/11（日志 `%TEMP%\yg-precommit-p25.log`） |

**负向探针**（副本 `%TEMP%\ygg-mut-gate`，已删除）：把 `release := s.gate(taskActivity, "activity")`
变异成 `release := func() {} // 空锁` → 用例 5s 报红
`手动触发 5s 未返回：同族互斥缺失，它排进了正在执行的那趟`。

## 三、测试本身踩的坑（值得记）

第一版用例是**同步**调用 `RunActivityNow()` 断言「立刻返回 + 只打一次上游」。门控缺失时它不是
失败而是**排进正在跑的那趟**，于是整个用例挂到 `go test` 的 10 分钟超时；再加上
`defer srv.Close()` 早于 `t.Cleanup` 执行，Close 会等卡在 `<-release` 的 handler，二次挂死。
最终形态：手动触发跑在 goroutine 里 + 5s 超时断言，`t.Cleanup` 按 LIFO 顺序先放行 handler、再 Close。
没有证据的「防御性超时」在这里是有用的：它把「挂死」变成「5s 报红」。

## 四、怎么回滚

`git revert <提交>`：删掉 `taskGates`/`balanceGate`/`gate` 与各入口的两行接入即可，行为回到
2026-10-22 审计时的「互不排斥」。不涉及二进制/配置/凭证。

## 五、同批核过但**没改**的：P2-3（idle.go「读缓冲无界」）

结论：**该条已不成立**，不修（L1 证据）：

- 单帧：`sse.go:44-95` 的 `readSSELine` 用 `ReadSlice` + `maxSSEFrameBytes`（1 MiB）累积，
  超限返回 `errFrameTooLarge`，注释明确「把畸形不换行的流变成错误而不是 OOM」。
- 整流聚合：`sse.go:243` 的 `trackedAggregateBytes() > maxAggregateBytes`（16 MiB）在上限处收口。
- 透传路径：`handler_fallback.go:94` 的 `io.Copy` 边读边写给客户端，`io.Copy` 内部 32 KiB 缓冲，
  不存在「先攒起来」的缓冲；SSE 主路径的 `bufio` 缓冲是 `64 KiB`（`sse.go:106/928`）。
- `idle.go` 自身 `Read(p)` 直接把调用方缓冲交给底层流，不额外分配——所谓「读缓冲」由上述消费者决定，
  三条消费者都有界。

真要再动，只能改背压语义（10-22 审计的原始判断），而当前没有任何超限事故可作依据 → 不做。
