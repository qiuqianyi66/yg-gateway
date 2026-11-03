# 代码块：可取消等待 + 本地日期/小时键

唯一来源：`internal/pkg/timex`（2026-11-02 建）。测试：`timex_test.go`。

## 1. 可取消等待（**不要**再写一份 `time.Sleep` + `select`）

```go
// ✅ 轮转退避、限速、排程宽限一律用它
if !timex.SleepCtx(ctx, d) {
    return false // ctx 取消：客户端断连/优雅停机，立刻放弃本轮
}
```

语义（三条都要记住，改动前先看测试）：

- `d <= 0` 立即返回，但**仍反映 ctx 状态**（已取消 → false）。这条不是洁癖：
  测试把退避基数置 0 时靠它跳过等待，同时保证"已取消的上下文"不会被当成"等待成功"。
- 等满返回 true；中途取消立即返回 false（不等满时长）。
- 出处：`server/backoff.go` 与 `scheduler/scheduler.go` 曾各有一份等价实现，
  因依赖方向禁止互引而被迫复制（原注释自认"消费侧实现"）——已上移。

## 2. 本地日期键（分片键 / 文件名 / 报表列）

```go
day := timex.Date(t)          // "2006-01-02"
stamp := timex.DateTime(t)    // "2006-01-02 15:04:05"
hourKey := timex.Hour(t)      // "2006-01-02T15"
```

**不变式**（有测试 `TestHourStartInvariant`）：`timex.Hour(timex.HourStart(t)) == timex.Hour(t)`。
usage 的写入侧用 `Hour(now)` 生成 `h:` 分片键、查询侧用 `HourStart(now)` 算窗口下界，
两侧必须同源，否则窗口错一格（少算/多算一小时，不报错）。

## 3. 什么时候**不要**用 timex

- 业务口径的"今天"：签到日、计费周期、CST 零点 → 留在领域包
  （如 `upstream/degrade.go` 的 `nextMidnightCST`，它绑定时区策略，不是通用日期格式化）。
- 测试文件：**故意**保留 `Format("2006-01-02")` 字面量，独立于实现——
  布局常量被误改时测试会红，而不是跟着一起错（PRD「非目标」已声明）。

## 反例

```go
// ❌ 自己写一份：语义会与 timex.SleepCtx 漂移（尤其 d<=0 与 ctx 已取消的组合）
t := time.NewTimer(d)
select {
case <-ctx.Done(): return false
case <-t.C: return true
}
```
