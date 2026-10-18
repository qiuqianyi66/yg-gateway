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