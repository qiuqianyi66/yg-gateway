# 2026-10-26 agent-traffic-profile（V3-10：agent 流量画像）

## 背景与必要性

ROADMAP v3 P2 项（V3-10，NEW-FRONTIERS #3）：编码 agent 的流量模式（长对话、
高频工具循环、cache 复用）与人类完全不同，而 reqlog 此前无法回答「谁在打、
一个会话吃了多少 token、cache 命中如何」。V3-8（kind 维度）立了地基后，本项
补两个维度：`session_fp`（会话键盐哈希）+ `agent_kind`（UA 分类），并在
`/status` 出聚合 JSON。面板 UI 等 V3-6 E2E 解锁。

动工前研究收口（实证，非转述）：

- 会话键 `session.ExtractKeyOptView` 已在 `deriveSessionKeys` 里算出（粘性路由
  同源）；盐哈希 `session.RequestIDForKey` 已存在，出站 `X-Conversation-Request-ID`
  就在用它——直接复用，不新造哈希。
- reqlog 事件构造点唯一（`requestTrace.event()`），`requestTrace` 已经在
  `withAuth` 链路里从 context 取得到，handler.go 在 `deriveSessionKeys` 之后
  挂 `trace.setSessionFP(sessKey)` 是最小侵入。
- 聚合不引入常驻 goroutine：`/status` 请求时现读 archive 1000 条 + 纯函数聚合，
  与已有 `/panel/api/request_logs` 同路径；与 `upstream.TaskMetrics`（per-conv
  内存 TTL，只回答「最近一个会话」）不重叠。

## 改了什么

1. `internal/reqlog/reqlog.go`：`Event.SessionFP` / `Event.AgentKind` 两字段
   （`omitempty`，三值）。
2. `internal/server/agentkind.go`（新）：`ClassifyAgentKind(ua)` 闭词表 6 类 +
   `other` 兜底；空 UA → 空串（unknown，不冒充 other）。
3. `internal/server/logging.go`：`requestTrace.sessionFP` 字段 + `setSessionFP`
   setter（统一走 `session.RequestIDForKey`）；`event()` 填两字段。
4. `internal/server/handler.go`：`chatCompletions` 在 `deriveSessionKeys` 之后挂
   `trace.setSessionFP(sessKey)`；`/status` 新增 `agent_profile` 块
   （`agentProfileSnapshot()` 读 archive 1000 条 chat 事件后聚合；无信号时
   整块省略——与 task_metrics/protocol_drift 同源 unknown 表达）。
5. `internal/reqlog/profile.go`（新）：`AggregateProfile(events)` 纯函数，
   输出 `window` / `by_agent{}` / `top_sessions[]`。

## 怎么验证的（GWT 兑现）

- **G1 session_fp 打标**：`TestRequestMetricsTagsSessionFPAndAgentKind`——带
  `metadata.conversation_id=test-conv-xyz` 的请求，事件 `SessionFP ==
  session.RequestIDForKey("test-conv-xyz")`（可复算），UA `claude-cli/...` →
  `agent_kind=claude_code`。
- **G2 unknown vs other**：`TestRequestMetricsAgentKindOmittedWhenClientInfoOff`——
  `RecordClientInfo=false` 时 `AgentKind` 空串（unknown），**不**兜底成 other；
  SessionFP 仍打（会话键来自 body，与 UA 开关正交）。
- **G3 历史行兼容**：两字段 omitempty，V3-10 之前的归档行读回空串，wire 不产出
  `"session_fp":""`；聚合时空 fp 不计 top_sessions、空 kind 进 `unknown` 桶。
- **G4 聚合**：`TestAggregateProfileByAgentAndSessions`——3+2+1+散客 7 行事件，
  by_agent requests/sessions/tokens 逐一对账，top_sessions 按 turns 降序。
- **G5 cache miss 未知不冒充 0**：`TestAggregateProfileCacheMissUnknownNotCounted`——
  `cache_miss_unknown=true` 的行 miss 不累加，hit 仍累加。
- **G6/G7**：`agentProfileSnapshot()` 在 0 事件 / 全历史行（无信号）时返回 nil，
  `/status` 不出现 `agent_profile` 键；既有全部键零回归（全量 23 包测试过）。
- **词表表驱动**：`TestClassifyAgentKind` 16 个 case 钉死每个分支 + 大小写/空白。
- 门禁：全量 `go test ./...` 23 包全过（reqlog 0.17s / server 9.3s / panel 48s）；
  go vet 零告警；gofmt 零漂移；check-boundaries 退出 0；check-depth 未回退
  （maxFileLines 1089→1070 顺手收紧）。

## 怎么回滚

单提交 revert。两新字段 omitempty，回滚后新归档行不再携带，旧归档里多出的
`session_fp`/`agent_kind` 键被旧代码 Unmarshal 丢弃——双向兼容，无迁移需求。
`/status` 上 `agent_profile` 键随回滚消失，既有消费方按「键缺失=未观测」处理。

## 诚实记录

- **agent_kind 是 inference，不是 observation**：UA 客户端完全可控可伪造，
  词表不可能覆盖所有客户端（dsh/Aider/Cline/Roo/Cherry Studio/Cursor 等未列）。
  消费方按「近似流量构成」读，禁止按「精确客户端清单」做计费/拦截决策。
  词表后续按需补，每补一项同步本文件 + REQ + 测试表。
- **session_fp 是 derivation**：从 sessKey 派生；而 sessKey 本身可能是客户端
  显式 conversation_id（observation）或内容派生键（system+首条 user 哈希，
  inference）——四态纪律里它落在 derivation 档，消费方应知道这一点。
- **无会话身份的散客**：客户端没发会话键且内容派生关了（`DisableSessionDerive`），
  SessionFP 空——这些请求仍进 by_agent.requests，但不进 top_sessions、不计
  sessions 数。这是设计内行为（他们没有「会话」可聚合）。
- **面板 UI 未做**：app.js 4378 冻结，等 V3-6 E2E 解锁后随瘦身边际补；本项
  只出 `/status` JSON。
- **独立复审**：本变更未做（子 agent 配额不稳，环境约束见 PLAN-next §3）；
  自查覆盖 G1–G7，按 L-16 本条不冒充独立 review，待补入口=本文件。
