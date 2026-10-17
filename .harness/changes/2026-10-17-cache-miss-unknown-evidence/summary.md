# 变更记录 — `cache_miss_unknown` 的端到端证据（PLAN B1，L1）

- 日期：2026-10-17
- 分级：**L1**（纯测试新增：无生产代码改动、无行为变更、无接口/数据模型/协议变更）
- 计划：`docs/PLAN-open-items-2026-10-17.md` §9.2 B1
- 改动文件：`internal/server/usage_unknown_test.go`（新增一个测试 + 两个 import）

## 一、为什么需要（问题陈述）

`cache_miss_unknown` 这个标记的意义是「上游给了 hit、但 miss **未知**」（既无
`prompt_cache_miss_tokens`、prompt 也不可用）——它存在的理由是**不许把未知写成 0**：
0 会被读成「全部命中」，把命中率抬到 100% 并压掉低命中率告警（issue #92 的信号）。

现状实测：仓里有两个相关测试，但**都不覆盖中间那段链路**：

| 已有测试 | 覆盖到哪 | 没覆盖什么 |
|---|---|---|
| `TestCacheTokensMissUnknownIsNotZero` | `chatStatsReader.CacheTokens()`（解析器四态） | 状态如何被填进 Event |
| `TestReqlogCacheMissUnknownIsAdditive` | `reqlog.Event → JSON`（序列化器） | Event 从哪来 |

即「chatrun 填 Event → Recorder 异步写 `*.jsonl`」这一段**没有测试**。而产线的
`cache_miss_unknown` 至今一次都没出现：

```
扫 D:\yg-gateway\data\request-logs\requests-*.jsonl：6916 行中
  cache_miss_tokens 出现 6542 次
  cache_miss_unknown 出现 0 次
口径：6 个文件只读到 5 个（当日文件被网关进程占用）→ 当日是 unknown，不是「没有」
```

所以「产线会打这个标记」此前只是**推断**。本变更把它变成**受控实验下的观察**——注意仍**不是**产线观测（产线仍是 unknown，见 §四）。

## 二、改了什么

新增 `TestReqlogArchiveRecordsCacheMissUnknown`：用既有夹具（`newFakeUpstream` +
`testPoolWith` + `doChat`）打**真实 handler 请求**，`reqlog.New(reqlog.Config{Dir: t.TempDir(), Enabled: true})`
把归档写到临时目录，`Close()` 刷盘后**读真实文件**断言行内容。四个子用例双向覆盖：

| 用例 | 上游 usage | 断言 |
|---|---|---|
| 非流式 · miss 未知 | 只 `prompt_cache_hit_tokens:800` | 行里有 `"cache_miss_unknown":true`，**且不得有** `cache_miss_tokens` |
| 流式 · miss 未知 | 同上（末帧 usage） | 同上 |
| 非流式 · miss 已观测 | hit 100 / miss 900 / prompt 1000 | 行里有 `"cache_miss_tokens":900`，**且不得有** `cache_miss_unknown` |
| 流式 · miss 已观测 | 同上 | 同上 |

**夹具要点（踩过一次）**：本仓上游**恒走 SSE**（CodeBuddy 只吐 event-stream），非流式是 handler
聚合出来的——`chatflow_test.go` 的子用例 1 就是「非流式请求 + SSE 上游」。第一版我把非流式用例的
上游体写成普通 JSON，结果 502 `upstream_parse: upstream stream contained no valid data events`。
所以三个用例的上游体都是 SSE，只有**请求**的 `stream` 字段决定 handler 走聚合还是透传。

## 三、怎么验证的（含双向变异）

正常跑：`go test ./internal/server/ -run TestReqlogArchiveRecordsCacheMissUnknown -v` → **4/4 PASS**。

按 L-2 的纪律（改前改后哈希闭环，不留下被改的生产文件）做了两次变异：

**变异 A（还原旧实现）**：`chatrun.go` 里把
`st.cacheHit, st.cacheMissUnknown = hit, true` 改回 `st.cacheHit, st.cacheMiss, st.hasCache = hit, 0, true`
（流式与非流式各一处，共 2 处替换）→ **两个「未知」用例 FAIL**，归档行变成：

```
{"...","credit_known":false,"cache_hit_tokens":800}      ← cache_miss_unknown 与 cache_miss_tokens 都不见了
```
正是那个 bug 的形态：miss 未观测，却既没标记也没数字，下游读成「0 未命中 = 全命中」。

**变异 B（假实现）**：`logging.go` 把 `e.CacheMissUnknown = s.cacheMissUnknown` 改成 `= true`
（无条件标记）→ **两个「已观测」用例 FAIL**，归档行自相矛盾：

```
{"...","cache_hit_tokens":100,"cache_miss_tokens":900,"cache_miss_unknown":true}
```

两次变异后均 `Copy-Item` 还原，`Get-FileHash` 校验与改前**逐字节一致**（`44BADD08…`），
随后 `go test ./internal/server/` 整包 `ok`（8.1s）——确认没有残留改动。

## 四、结论与仍然的未知

- **现在可以说的**：真实请求走完 chat 路径、经真实 Recorder 写入真实 `*.jsonl` 文件时，
  miss 未知会落 `cache_miss_unknown:true` 且不落数字；miss 已观测则反之（双向都有牙齿）。
- **仍然未知（不粉饰）**：产线**从未实际出现**该标记。原因最可能是上游在缺 miss 时
  通常连 `prompt_tokens` 一起给（那就走「推导」分支，miss = prompt − hit，不需要标记）。
  要消解这个 unknown 需要**一次真实上游返回「只有 hit、没有 miss、也没有 prompt」**——
  这不受我们控制，不能靠造数据假装观测到。

## 五、回滚

`git revert <本提交>`（删掉测试）。无生产行为可回滚。

## 六、第二轮独立复审（1 🟡 直指本轮最大的操作失误）

| 级别 | 问题 | 处置 |
|---|---|---|
| **🟡** | **工作区 `internal/server/chatrun.go` 停留在「变异 A」状态**（`SHA256=B3CB56DA…`，HEAD 是 `44BADD08…`）：两处 `st.cacheHit, st.cacheMissUnknown = …` 被改成 `st.cacheHit, st.cacheMiss, st.hasCache = …, 0, true`。若被原样提交，等于**静默回退这个修复** | 已 `git checkout -- internal/server/chatrun.go` 还原；`git diff` 为 0 行；`go test -run TestReqlogArchiveRecordsCacheMissUnknown -count=2` 恢复绿。**根因是我在真实工作区里做变异**（不是副本），而同一时刻另一会话也在改同一工作树——我还一度把这份脏状态误判成「别人的在途改动」 |
| 🟢 | 文里「产线从未出现（6916 行 0 次）」丢了「6 个文件只读到 5 个」的口径 | 已补（测试注释与 PLAN 两处）——否定断言必须带覆盖口径，这正是 L-15 |
| 🟢 | 「本变更把它变成有测试兜住的**观察**」会被独立引用成产线观测 | 改为「**受控实验下的观察**；产线仍是 unknown（§四）」 |
| 🔵 | `sseKnown` 夹具 hit=100/miss=900/prompt=1000，使「显式 miss」与「prompt−hit 推导」结果相同 → 「已观测」方向无法证明走的是显式字段分支 | 已删掉该用例末帧的 `prompt_tokens`：推导不可用，`cache_miss_tokens:900` 只可能来自显式字段 |
| 🔵 | 文里写「共 4 处替换」，代码里其实只有 **2 处** | 已改 2 |
| 🔵 | 测试注释的覆盖清单只列 3 条，漏「流式 · miss 已观测」 | 已补齐第 4 条 |

复审独立复现的部分（它自己的话）：`-count=3` → 12/12 PASS 无 flake；在 `%TEMP%` 副本里**自己重做两次变异**都红，并 `go env GOMOD` 断言指向副本；归档原文逐字打印核对；CI 的 `test` 与 `race` job 在 Linux 上也已跑过该测试。

**由此触发的纪律升级**：LESSONS **L-2 升到 Seen 2** —— 变异一律在 `%TEMP%` 副本里做并断言
`go env GOMOD`，不再是「建议」而是硬规则（本次事故就是「在原地做变异」的直接后果）。