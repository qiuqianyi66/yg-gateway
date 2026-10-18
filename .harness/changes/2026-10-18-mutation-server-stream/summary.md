# 变更记录 — 变异核验扩展到 internal/server 的流式结果分类（A6 增量）

- 日期：2026-10-18
- 分级：**L1**（纯核验 + 新增测试；生产代码未改）
- 计划：`docs/PLAN-open-items-2026-10-17.md` 的 A6
- 工具：`scripts/mutation/mutations/server.json`（6 条）

## 一、为什么打这里

`chatrun.go` 的流式收尾 switch 是全仓**注释最详细、修复历史最明确**的一段：
空流 / 超大帧 / 上游 error 帧 / 客户端断连 / 真成功，五个分支各自对应一次真实事故
（`_ =` 吞错把失败流记成 200、限流号被当健康号且粘性钉住它……）。
按「注释密度 ≈ 历史血债」的经验，这类地方最该有测试兜底。

## 二、结果：6 条变异 → 2 咬住 / 4 存活（存活分三类，不能一概叫「缺口」）

| ID | 变异 | 结果 | 归类 |
|---|---|---|---|
| S2 | 错误帧判据 `errFrame != ""` 写反 | 咬住（`TestRequestMetricsRecordsStream`） | 已有覆盖 |
| S5 | 错误帧不再 `applyErrorPolicy`（限流号被当健康号） | 咬住（**本轮新增** `TestStreamOutcomeClassification`） | ✅ 已补 |
| S1 | 空流 `st.status` 502 → 200 | 存活 | **只影响 stderr 日志行**（见 §四） |
| S4 | 超大帧 `st.status` 502 → 200 | 存活 | 同上 |
| S3 | 断连 `OutcomeInterrupted` → `Success` | 存活 | 真缺口：需「写失败的 ResponseWriter」才能驱动 |
| S6 | 真成功分支去掉 `NoteSuccess` | 存活 | 真缺口：需断言池侧健康度/EWMA 变化 |

新增测试 `internal/server/stream_outcome_test.go`：表驱动四例（空流 / 超大帧 / error 帧 / 正常流），
断言 reqlog 事件的 `Outcome` 与（error 帧时）账号进入冷却。

## 三、S1/S4 为什么「存活」却不算测试缺口

它们改的是 `st.status`，而 **`st.status` 不进 reqlog**：

- `reqlog.Event.Status` 来自 handler 层：`h.cfg.RequestLog.Record(trace.event(status))`，
  而流式响应头早已发出 → 该值恒为 200；
- `st.status` 的唯一读者是 `logging.go:105` 的 `logChatRowEx(..., s.status, ...)`，即 **stderr 日志行**。

所以这两个变异的行为差异**只体现在日志行里**。要咬住它们必须捕获 log 输出，属另一种测试形态。

## 四、由此发现的一个观测事实（不是本轮引入，是首次被看见）

`chatrun.go` 里空流/超大帧分支写着「状态收敛到 502 观测」。**实测：这个 502 只出现在 stderr
日志行，reqlog 归档与面板看到的仍是 200**（因为 Event.Status 取的是 HTTP 码）。
也就是说「502 观测」目前只对看 stderr 的人成立，对看面板/归档的人不成立。

处置建议（**未做，需拍板**）：要么把 `st.status` 也带进 Event（会改归档语义与面板统计口径），
要么把注释改成如实描述「仅 stderr」。两条都要动观测口径，故本轮只记录。

## 五、剩余缺口（按可做性排）

1. **S3 断连**：写一个 `Write` 返回 error 的 ResponseWriter 驱动 → 断言 `OutcomeInterrupted`。
2. **S6 NoteSuccess**：断言成功一跳后池侧健康计数/EWMA 变化。
3. **S1/S4 日志行口径**：`log.SetOutput` 捕获后断言 status 字段（低价值，除非要做日志回归）。

## 六、回滚

无生产代码改动；`git revert` 即可。
## 七、第二轮补齐（2026-10-18 续）

1. **S3 已补测试**：`TestStreamClientDisconnectRecordsInterrupted` 用自定义 `failAfterWriter`
   （前 N 次写正常、之后返回 error，不实现 `http.Flusher` —— chatrun 不依赖它）模拟客户端断连。
   副本复跑：`[S3] 咬住 | --- FAIL: TestStreamClientDisconnectRecordsInterrupted`。
   → server 这批从「2 咬住 / 4 存活」变成 **3 咬住 / 3 存活**。
2. **`st.status` 注释已改如实**（`chatrun.go` 空流分支）：原文写「状态收敛到 502 观测」，
   实测该 502 只进 stderr 日志行，reqlog/面板恒 200。已在代码里写清，并注明「是否带进 Event 另议」。
3. **S6（去掉 NoteSuccess）仍存活，且判定为「当前无观测出口」**：
   `NoteSuccess` 改的是 `successCount` / `lastSuccess` / EWMA / fails / retryCount / breaker 等，
   而 `Pool` **没有任何导出访问器**暴露它们（只有 `CountsDetailed` 的 healthy/cooling 计数，
   在干净池上不变化）。要咬住 S6 必须先加访问器（生产代码 + 口径），属另一件事，本轮不做。
4. **S1/S4 仍为「只影响日志行」的变异**（见 §三），要咬住需捕获 log 输出。

### 7.1 server 本轮的最终账

| 结果 | 条目 |
|---|---|
| 咬住（3） | S2 错误帧判据（旧测试）、S5 错误策略（**新测试**）、S3 断连（**新测试**） |
| 存活·非缺口（2） | S1/S4：只改 `st.status`，仅 stderr 日志行可见 |
| 存活·真缺口但受阻（1） | S6：池侧无访问器，无法从外部断言 |

## 八、剩余（明确交接）

| 项 | 阻塞点 |
|---|---|
| S6 断言 | 需要 `Pool` 暴露成功计数/EWMA 的只读视图（生产代码改动，需立项） |
| S1/S4 断言 | 需 `log.SetOutput` 捕获日志行；价值低于上面两条 |
| B4b 残留 `cmd.exe` 清理 | 属 `deploy-prod.ps1` 变更，**必须配排练场景**（该脚本的全部前提就是「排练过」），单开一轮 |
| B1b 面板 unknown 有值态 | 需真实例 + 造一条 unknown 登记（触发条件是成本不明，要真实上游调用）；且已有 JS 级测试（3/3 反向变异红），边际价值低 |
## 九、S6 已补（第三轮，2026-10-18）

**结论：S6 不是「缺观测出口」，而是「缺断言」**——`pool.Status(uid)` 早已暴露 `SuccessCount`
（state.go:323 / 601），此前没人用它对成功路径做断言。

改法：`TestStreamOutcomeClassification` 加 `wantSuccessNote`，成功后断言 `Status("u1").SuccessCount > 0`，
失败路径断言 `== 0`（空流/超大帧/错误帧都不该记成功）。

副本复跑结果：**server 6 条 → 4 咬住 / 2 存活**
（S1 空流 st.status、S4 超大帧 st.status 仍存活，且已判定为「只影响 stderr 日志行」的变异）。

| 结果 | 条目 |
|---|---|
| 咬住（4） | S2 错误帧判据、S3 断连、S5 错误策略、**S6 成功计数** |
| 存活（2） | S1/S4：只改 `st.status`，仅 stderr 日志行可见（reqlog 的 Status 恒 200） |
## 十、S1/S4 已收（第四轮，2026-10-18）——并且纠正了一处我自己写错的观测结论

结论：**server 6 条变异现在 6/6 咬住**（在新克隆里复跑确认，0 存活 0 无效）。

### 10.1 出口查清了：chatLogOut（含面板日志视图）

`chatrun` 的 `st.status` 经 `chatStat.done()` → `logChatRowEx` 写进 **`chatLogOut`**
（`fmt.Fprintf`，不是标准 logger）。生产里 main 通过 `SetChatLogOutput` 给 `chatLogOut`
注入 MultiWriter，**镜像进面板的 `/panel/api/logs` 环形缓冲**。

所以 502 的可见性是：**面板日志视图能看到；面板统计与 reqlog 归档看不到**（后者取 handler 层
HTTP 码，流式恒 200）。

### 10.2 我上一轮写错的注释已改

§七 里我写过「只对看 stderr 的人成立，对看面板不成立」——**错的**：面板日志视图也能看到。
`chatrun.go` 的注释已按上面口径重写。

### 10.3 测试

新增 `internal/server/stream_logrow_test.go`：把 `chatLogOut` 重定向到 buffer，
捕获聊天表格行，断言空流/超大帧**记 502**、正常流**不记 502**。

> 过程记录（两类误判，都记下来免得复发）：
> ① 第一次捕获标准 `log`，一行都没抓到——因为表格行走 `fmt.Fprintf(chatLogOut)`；
> ② 因此一度以为「流式路径根本不写表格行、S1/S4 是等价变异」，差点把真缺口判成「不是缺口」。
> **先验证出口，再下结论。**

### 10.4 副本复跑

```
[S1] 咬住 | --- FAIL: TestStreamFailureStatusReachesLogRow
[S2] 咬住 | --- FAIL: TestRequestMetricsRecordsStream
[S3] 咬住 | --- FAIL: TestStreamClientDisconnectRecordsInterrupted
[S4] 咬住 | --- FAIL: TestStreamFailureStatusReachesLogRow
[S5] 咬住 | --- FAIL: TestStreamOutcomeClassification
[S6] 咬住 | --- FAIL: TestStreamOutcomeClassification
包 internal/server：咬住 6 / 存活 0 / 无效 0
```