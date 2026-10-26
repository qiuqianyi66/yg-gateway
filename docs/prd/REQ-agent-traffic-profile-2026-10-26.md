# REQ-agent-traffic-profile-2026-10-26 — agent 流量画像（ROADMAP v3 V3-10）

## 必要性（阶段 0）

1. **真实问题**：编码 agent（Claude Code / Codex / dsh / Cherry Studio 等）的流量
   模式与人类完全不同——长对话、高频工具循环、prompt cache 命中率高、单会话
   几十上百轮。当前 reqlog 只能按账号/模型聚合，看不到「谁在用、是哪个 agent、
   一个会话吃了多少 token、cache 命中如何」。养号友好的叙事缺下一代证据：
   agent 流量对账号的伤害模式（长上下文 → 高 prompt token → 更快烧积分）第一次
   不可见。
2. **不做会怎样**：V3-3 生产换版后，48h 数据回填时只能看全局 token 趋势，
   无法回答「是哪类客户端在烧号」「哪个会话在反复打同一账号」。养号决策
   （快过期优先、积分保底）缺少客户端维度。
3. **更便宜手段**：维度已在 V3-8 立好（kind=chat）；本项复用既有
   `session.ExtractKeyOptView`（粘性路由的会话键）+ `session.RequestIDForKey`
   （盐哈希，已在用），不引入新解析器；UA 分类用 6 行词表匹配。聚合纯函数
   跑在 /status 现有的 ReadArchive 路径上，不引入常驻 goroutine。
4. **现状是否已变**：ROADMAP v3 V3-10 立项；V3-8（kind 维度）已落地，依赖满足。

**结论：做。**

- 状态：🚧 进行中（2026-10-26）
- 来源：[ROADMAP-prd-v3](../roadmap/ROADMAP-prd-v3-2026-10-25.md) V3-10
  （NEW-FRONTIERS #3）
- 变更目录：`.harness/changes/2026-10-26-agent-traffic-profile/`

## 需求

### 数据面：reqlog.Event 加两列

| 字段 | 类型 | 语义 | 四态 |
|---|---|---|---|
| `session_fp` | string (32 hex) | 会话键的盐哈希：`session.RequestIDForKey(sessKey)`，与出站 `X-Conversation-Request-ID` 同源同盐 | **derivation**（从 sessKey 派生；sessKey 本身可能是客户端显式 conversation_id（observation）或内容派生键（inference）） |
| `agent_kind` | string (闭词表) | UA 分类结果 | **inference**（UA 客户端完全可控、可伪造、词表不全） |

两字段均 `omitempty`：缺失 = 未知，不产出 `"session_fp":""`。

**agent_kind 闭词表**（新增值必须同步本注释 + 分类器 switch + 测试表）：

| 值 | 匹配规则（UA 小写后包含） | 例子 |
|---|---|---|
| `claude_code` | `claude-cli` 或 `claude-code` | `claude-cli/1.0.x (external, cli)` |
| `codex` | `codex` | `codex_cli_rs/...` |
| `openai_sdk` | `openai-python` / `openai-node` / `openai-go` | `OpenAI/Python 1.x` |
| `anthropic_sdk` | `anthropic-` / `anthropic-sdk` | `anthropic-sdk-python/...` |
| `browser` | `mozilla/` | 人类浏览器 |
| `http_client` | `curl/` / `wget/` / `python-requests/` / `go-http-client/` / `node-fetch` / `undici` | 脚本/裸 SDK |
| `other` | 有 UA 但未命中以上任何一条 | （兜底，不静默丢进 unknown） |

**空串 = 未采集**：`logging.request_client_info` 开关关、或 UA 为空时，
`agent_kind` 留空（unknown），**不**填 `other`——`other` 是「UA 见到了但不认识」，
与「UA 没采到」严格区分。

### 聚合面：/status 新增 `agent_profile` 块

从 reqlog archive 读最近 N 条（默认 1000，封顶与 ReadArchive 一致）`kind=chat`
事件，纯函数聚合：

```json
"agent_profile": {
  "window": { "events": 842, "from": "2026-10-26T...", "to": "2026-10-26T..." },
  "by_agent": {
    "claude_code": {
      "requests": 500, "sessions": 12,
      "prompt_tokens": 123456, "completion_tokens": 23456,
      "cache_hit_tokens": 100000, "cache_miss_tokens": 5000
    }
  },
  "top_sessions": [
    {
      "fp": "ab12...cd", "agent_kind": "claude_code",
      "turns": 35,
      "prompt_tokens": 50000, "completion_tokens": 8000,
      "cache_hit_tokens": 45000, "cache_miss_tokens": 3000,
      "first_seen": "...", "last_seen": "..."
    }
  ]
}
```

口径：

- `by_agent` 按 `agent_kind` 汇总；`agent_kind` 为空的行计入 `unknown` 桶
  （**不**并进 `other`——前者是没采到 UA，后者是 UA 见到了不认识）。
- `top_sessions` 只统计 `session_fp != ""` 的行；按 turns 降序取前 10。
  无 session_fp 的请求（客户端没发会话键、内容派生也关了）不计入 sessions
  数——它们属于「无会话身份的散客」，by_agent 里 requests 仍算。
- `cache_hit_tokens` / `cache_miss_tokens` 直接累加；`cache_miss_unknown=true`
  的行**不**进 miss 累加（miss 未知不是 miss=0）。命中率不在 JSON 里现算
  （消费方自己除，避免我们把 0 当观察）。
- **整块省略条件**：archive 里 0 条事件、或最近 1000 条里没有任何
  `session_fp` 也没有任何 `agent_kind`（全是未知）时，`agent_profile` 键
  整体不输出——与 task_metrics / protocol_drift 的 unknown 表达同源
  （键缺失 = 未观测，不是「没有 agent 流量」）。

### 不做（红线）

- **不落盘新维度**：不把原始 conversationId / 原始 UA 之外的任何内容写进
  archive；session_fp 是盐哈希（同 RequestIDForKey 已有的出站头），
  不新增 PII。
- **不做面板 UI**：app.js 4378 冻结，等 V3-6 E2E 解锁。本项只出 /status JSON。
- **不告警、不拦截、不按 agent_kind 路由**：纯观测。
- **不引入常驻 goroutine**：聚合在 /status 请求时现算（与 request_logs 同路径），
  不维护进程内增量聚合器（TaskMetrics 是 per-conv 内存 TTL，本项是 archive 读后聚合，
  不重叠）。
- **不回填历史行**：V3-10 之前的归档行没有 session_fp/agent_kind，消费方按
  三值处理（缺失 = 未知），不做迁移脚本。

## Given-When-Then 验收

- **G1 打标-session_fp**：When 下游请求带 `metadata.conversation_id=abc`，Then
  reqlog 事件 `session_fp == session.RequestIDForKey("abc")`（32 hex，可复算）；
  When 请求无会话键且 `DisableSessionDerive=true`，Then `session_fp` 省略。
- **G2 打标-agent_kind**：When UA=`claude-cli/1.0.0`，Then `agent_kind=claude_code`；
  When UA 为空（`request_client_info=false`），Then `agent_kind` 省略；
  When UA=`Mozilla/5.0 ...`，Then `agent_kind=browser`。
- **G3 历史行兼容**：When 归档含 V3-10 之前的行（无这两字段），Then 读回
  两字段空串，wire 序列化不产出 `"session_fp":""`；聚合时这些行进 `unknown` 桶。
- **G4 聚合**：When archive 有 3 行同 session_fp（不同轮）、2 行另一 fp，
  Then `top_sessions` 按 turns=3 / turns=2 排序；`by_agent[kind].requests` 对。
- **G5 cache 未知不冒充 0**：When 一行 `cache_miss_unknown=true`，Then 它的
  `cache_miss_tokens` 不计入 by_agent/top_sessions 的 miss 累加（hit 若有值仍计入）。
- **G6 unknown 整块省略**：When archive 0 条 chat 事件（或全部事件两字段都空），
  Then /status 响应里**没有** `agent_profile` 键；When 有数据，Then 键出现。
- **G7 零回归**：When 不带 agent_profile 的既有面板/客户端读 /status，Then
  原有全部键（accounts/total/latency/task_metrics/...）逐位不变。

## 落点

- `internal/reqlog/reqlog.go`：`Event.SessionFP` / `Event.AgentKind` 两字段
  （omitempty）。
- `internal/server/agentkind.go`（新）：`ClassifyAgentKind(ua string) string`
  纯函数 + 闭词表常量。
- `internal/server/logging.go`：`requestTrace` 加 `sessionFP` 字段 + setter；
  `event()` 填两字段。
- `internal/server/handler.go`：`chatCompletions` 在 `deriveSessionKeys` 之后
  调 `trace.setSessionFP(sessKey)`。
- `internal/reqlog/profile.go`（新）：`AggregateProfile(events []Event) Profile`
  纯函数（无 IO、无时钟依赖——first_seen/last_seen 从 events 里 min/max）。
- `internal/server/handler.go` `status()`：组装 `agent_profile` 块
  （调 `RequestLog.ReadArchive(1000, Filter{Kind: KindChat})` 后聚合）。
- 测试：`reqlog_test.go`（G3）、`agentkind_test.go`（G2 词表）、
  `profile_test.go`（G4/G5）、`logging_test.go`（G1）、`handler_test.go`（G6/G7）。
