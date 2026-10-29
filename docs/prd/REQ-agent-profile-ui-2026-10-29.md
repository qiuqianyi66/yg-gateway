# REQ: 面板 agent 流量画像 UI（V3-10 面板端，R5，立项 2026-10-29）

> 状态：**立项待执行**（等外部 trends 轮合并释放 app.js）。数据侧已落地，本 REQ
> 只立项与锁定 UI 契约，不含代码改动。

## 必要性

- **真实问题**（2026-10-29 实测）：V3-10 数据侧已落地（`f3385f4`：reqlog 加
  session_fp/agent_kind + `internal/reqlog/profile.go` 聚合 + `/status` 输出
  `agent_profile` 块），但**面板不展示**——数据采集了，用户/排障时看不到
  「哪些 agent 在消耗配额、哪些会话占大头」。养号与配额排查只能 curl /status
  或看日志，与「单二进制 + Web 管理面板」定位不符。
- **不做会怎样**：静默——agent 构成不可见，配额异常（如某 agent 刷爆 cache miss）
  难定位；V3-10 的采集价值只兑现一半。
- **更便宜的等效手段**：无（面板是唯一面向用户的可视化入口；日志/curl 不是面板）。

## 数据契约（已落地，本 REQ 消费方）

`/status` 的 `agent_profile` 块（`internal/reqlog/profile.go`，字段名即 wire 名）：

- `window`: `{events, from, to}`——输入窗口元信息（最近 1000 条 chat 事件）。
- `by_agent`: `map[string]AgentBucket`——`requests / sessions / prompt_tokens /
  completion_tokens / cache_hit_tokens / cache_miss_tokens`。键 `unknown` =
  UA 未采集（**与 `other` 严格区分**，REQ-agent-traffic-profile §三态）。
- `top_sessions`: `[]SessionStat`（截断 10）——`fp / agent_kind / turns /
  prompt_tokens / completion_tokens / cache_hit_tokens / cache_miss_tokens /
  first_seen / last_seen`。
- **无信号时整块省略**（unknown 表达，不输出空块）。

## 方案

1. **panel 后端**：新增 `GET /panel/api/agent_profile`——从 reqlog archive 读最近
   1000 条 chat 事件，调 `reqlog.AggregateProfile` 现算（纯函数，无进程内状态）；
   无信号返回 `{present:false}`（与 /status 的整块省略同源语义）；未接线
   RequestLog 返回 501（与 request_metrics / trends 端点同口径）。
2. **面板 UI**（app.js，外部轮合并后实现）：在面板加「流量画像」区块——
   - `by_agent` 汇总条：每桶请求数 / 会话数 / token 三件套 + 缓存命中/未命中；
     桶名直显（claude_code/codex/openai_sdk/...），`unknown` 桶标注「UA 未知」，
     `other` 标注「未识别 UA」——三值语义透传到 UI，禁止合并展示。
   - `top_sessions` 榜：turns 前 10，显示 fp 前缀 + agent_kind + turns + token 总量。
   - 无信号（`present:false`）隐藏区块，不显示空态。

## Given-When-Then 验收（实现时）

- When 面板加载且 /status 有 agent_profile 信号，Then 页面出现流量画像区块，
  by_agent 每桶数字与 /status 同源一致；unknown 与 other 分桶展示。
- When 无信号，Then 区块整体隐藏（无空态、无假数据）。
- When 会话数超过 10，Then 榜只显示 turns 最高的 10 条（与 maxTopSessions 对齐）。
- When 实现提交，Then go build + 全量测试绿（外部 trends 轮合并后基线）+ 边界/depth/
  context 棘轮不回退；app.js 行数棘轮（当前 4317）只许收紧或持平。

## 约束

- 消费 reqlog.Profile 现有字段，不改 wire 形状；`cache_miss_unknown` 语义（miss
  未知 ≠ miss=0）原样透传，面板不得用 `!field` 判否。
- 实现窗口：外部 trends 轮合并释放 app.js 后执行（避免同文件并发编辑冲突，L-21）。
- 纯前端 + 一个只读端点，无部署/生产影响。
