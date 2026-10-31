# REQ-context-guardrail-2026-10-31：上下文护栏（软告警 + 硬拒 + 会话限流）

> 状态：**设计定稿，代码待实现**（本切片只交付 PRD/ADR/summary 三文档）。
> 依据：2026-10-31 实弹观测（yg-gateway request-logs，observation）+ 2026-10-31 排查记录。

## 必要性（阶段 0）

1. **真实问题（实弹观测，非转述）**：网关 request-logs 显示「单会话上下文无界增长 + 多会话高并发」是**常态**：
   - 2026-10-31：386 条 chat 请求中 377 条（**97.7%**）prompt ≥30 万 token，最大 **735,026**（20:17 仍在涨）；20:01 单分钟 20 个请求，19:56–20:02 每分钟 10–20 个；至少 4–5 个会话并行（prompt 序列分簇可证）。
   - 2026-10-30：658 条，54% ≥30 万，最大 **827,964**；2026-10-29：268 条，53% ≥30 万，最大 506,916。
   - 后果链（同日观测）：单条请求重算全量历史（credit 5.97/条）→ 费用爆炸；TTFB 5–20s；上游 `context canceled`（19:55:04）；dsh 客户端多 agent 并行导致 `session/agent-busy` 拒收；思考模型在海量历史后小输出预算下图片正文被挤空。
2. **不做会怎样**：上下文继续无界 → 积分按「每请求重算全量历史」烧穿、账号被上游风控/降级、服务间歇不可用（`context canceled` 已有先兆）。这是项目「积分不浪费、账号活得久」定位的直接反面。
3. **现状是否已变**：网关**无任何上下文规模护栏**——现有约束只有 `MaxBodyBytes`（入站字节上限，不管 token 数）与 `keyRPM`（按请求数限流，不管单请求规模）。`count_tokens.go` 的 `estimateTokensJSON`（V3-1 交付，derivation 口径）已存在可直接复用，无需新依赖。
4. **更便宜手段**：护栏 = 请求前估算（复用现成估算器）+ 阈值判定 + 命中打标/拒绝，纯函数 + 配置热生效，无更便宜替代。

**结论：做**（L2 业务逻辑；`internal/server` + `cmd/server/config.go` + 测试；不碰上游协议与 data model）。

## 范围

| 编号 | 文件 | 验收（Given-When-Then） |
|---|---|---|
| G1 | `internal/server/guardrail.go`（新） | Given 配置 `guardrails` 各项为 0（默认关）；When 任意请求到来；Then `Guardrail.Check` 恒放行，行为与改动前逐位一致（零回归） |
| G2 | `internal/server/guardrail.go`（新） | Given `context_warn_tokens=500000`；When 估算 prompt ≥ 阈值；Then 不阻断，reqlog 事件标 `guardrail_hit=warn`，响应加 `X-Gateway-Warning` 头（估算值 + 阈值），webhook 告警按 key+1 分钟去重最多 1 条 |
| G3 | `internal/server/guardrail.go`（新） | Given `context_max_tokens=800000`；When 估算 prompt ≥ 阈值；Then 立即 413 + code `context_length_exceeded` + `Retry-After: 60`，**不**读上游、不选号、不排队，事件标 `guardrail_hit=blocked` |
| G4 | `internal/server/handler.go` | Given 请求进入 `chatCompletions`；When 阶段② `deriveSessionKeys` 之后、阶段③ `prepareOutbound`（提示词改写）之前；Then 对**客户端原始请求**执行 `Guardrail.Check(sessKey, estTokens)`，改写在检查之后（护栏面向原始输入） |
| G5 | `cmd/server/config.go` + `cmd/server/main.go` | Given 配置含 `guardrails.context_warn_tokens / context_max_tokens / session_rpm`（非法/负值归一 + 报错，与 `pool.failure_window` 同口径）；When 热更新；Then `Set*` 同步护栏状态，0 = 关，默认值见下节 |
| G6 | `internal/server/guardrail.go`（新） | Given `session_rpm>0`；When 同一 `sessKey` 1 分钟窗口请求数超限；Then 429 + `Retry-After: 60`（固定窗口，复用 `keyrpm.go` 自写无依赖风格；`sessKey` 为空即散客不计） |
| G7 | `internal/server/status_guardrail_test.go`（新，或并入 guardrail_test） | Given 命中 warn/blocked/session 限流各若干；When 请求 `/status`；Then 返回 `guardrail_stats`（各命中计数、最近命中时刻、估算 vs 上游 usage 对照样本 ≤20 条）；无命中时整块省略（未知表达，与 agent_profile 同源） |
| G8 | `internal/server/guardrail_test.go`（新） | 单测：估算口径（复算 `estimateTokensJSON` 已知样例）、阈值边界（恰等于/差 1）、零回归（全 0 放行）、`sessKey` 限流窗口翻转、命中标记进事件；`go test ./internal/server/ -run Guardrail` 全过 |

## 实现契约（实施切片照做，非可选）

**配置（config.json 新增块，与 `alerting` 平级）**

```json
"guardrails": {
  "context_warn_tokens": 500000,
  "context_max_tokens": 800000,
  "session_rpm": 0
}
```

- 0 = 关；负值归一 + 报错（同 `pool.failure_window` 口径）；未配置该块 = 全部 0 = 零回归。
- 解析后字段 `GuardrailsWarnTokens / GuardrailsMaxTokens / GuardrailsSessionRPM`（`json:"-"`，同 `FailureWindowDur` 先例）；`cmd/server/main.go` 装配 `Set*` 热生效（同 `SetFailureWindow` 先例，锁内同步）。

**reqlog 事件新增字段**：`GuardrailHit string json:"guardrail_hit,omitempty"`——`"warn" | "blocked" | 缺失` = 未命中。omitempty 三值纪律与 `SessionFP/AgentKind` 同族（V3-10），旧归档行读回空串，wire 不产出空键。

**API 契约（写死，测试钉住）**

| 情形 | HTTP | body error code | Retry-After |
|---|---|---|---|
| 超 `context_max_tokens` | 413 | `context_length_exceeded` | 60 |
| 超 `session_rpm` | 429 | `session_rate_limit` | 60 |
| 超 `context_warn_tokens`（不阻断） | 200 正常流 | —（响应头 `X-Gateway-Warning: estimated prompt tokens N exceeds warn threshold M`） | — |

413/429 均用 `writeOpenAIError`（与 `server_busy` 同族），message 带估算值与阈值（如 `estimated prompt tokens 823000 exceed guardrail max 800000; reduce conversation context or start a new session`），**不**读上游、不选号、不排队。

**/status 新增 `guardrail_stats`**（无任何命中时整块省略，unknown 表达与 `agent_profile` 同源）：

```json
"guardrail_stats": {
  "warn_hits": 12,
  "blocked_hits": 3,
  "session_rpm_hits": 0,
  "last_hit_at": "2026-10-31T20:17:00+08:00",
  "calibration_samples": [{"est": 523000, "upstream": 530120}]
}
```

- `calibration_samples`：最近命中（warn/blocked 均可）的估算 vs 上游实际 `prompt_tokens` 对照，≤20 条；上游缺失的样本**不进**对照（unknown 不冒充 0，四态纪律）。
- 实现复用 `agentProfileSnapshot` 的「请求时现读 archive 1000 条 + 纯函数聚合」路径，不引入常驻 goroutine。

**日志脱敏（R2）**：护栏日志只记 `session_fp`（盐哈希）与 `key_id`，不记会话原文/请求体/完整键名。

**测试清单（映射 G1–G8，函数名即断言意图）**

| 测试函数 | 断言 |
|---|---|
| `TestGuardrailAllZeroPasses` | 全 0 配置恒放行，行为与改动前逐位一致 |
| `TestGuardrailWarnMarksAndHeader` | 超 warn：不阻断、事件标 `guardrail_hit=warn`、`X-Gateway-Warning` 头出现 |
| `TestGuardrailBlocked413` | 超 max：413 + code + Retry-After + 事件标 blocked |
| `TestGuardrailHookPoint` | 挂点在阶段②后、改写前（对客户端原始请求判定） |
| `TestGuardrailConfigNormalize` | 默认/0/负值/非法归一 + 报错 |
| `TestGuardrailSessionRPM` | 窗口翻转、第 RPM+1 次拒、散客（sessKey 空）不计 |
| `TestGuardrailStatsOmittedWhenNoHits` | 无命中时 `/status` 无 `guardrail_stats` 键 |
| `TestGuardrailEstimateBoundary` | 估算恰等于/差 1 阈值的边界行为 |

## 默认值策略（基于 10-29~31 观测，实施时按实弹校准）

| 配置项 | 默认 | 依据 |
|---|---|---|
| `context_warn_tokens` | 500000 | 今天 40%（153/386）超 50 万——作为「该会话需要治理」信号，面板可查，不吵（webhook 去重） |
| `context_max_tokens` | 800000 | 今天最大 73.5 万**不误伤**；昨天 82.8 万会被拦（拦极端失控，防全量历史无限膨胀） |
| `session_rpm` | 0（关） | 改变客户端可见行为，默认不启用；用户按需开（如 6 req/min） |

校准方法：`/status` 的估算 vs 上游 usage 对照样本，累计后回调阈值与 `estimateTokensJSON` 权重（权重是 derivation，真机可校准——沿用 count_tokens.go 注释口径）。

## 估算口径（诚实声明，四态纪律）

- 估算值 = **derivation**（`estimateTokensJSON` 启发式：CJK 全角计 1、其余计 1/4 向上取整），**不是**上游 usage 精确值。拒绝决策必须发生在请求前，上游 usage 请求后才可得——这是用估算的唯一原因。
- 命中事件的 reqlog 同时记估算值与（若已发出）上游实际 `prompt_tokens`，供对照校准；未发出则 `prompt_tokens` 缺失 = unknown，不得按 0 处理。
- `session_rpm` 计数口径：与 keyRPM 相同——`record` 对每次实际请求（含将被 429 的），统计=真实速率，不是放行数。

## 不做（显式排除）

- **不改 dsh 客户端**（会话压缩/裁剪/拆新会话/图片即弃是客户端侧治理，属另一切片；本网关只做服务端护栏）。
- 不做请求排队（与 ADR-003 全局准入同理由：429 + Retry-After 让客户端退避，不掩盖问题）。
- 不动 `MaxBodyBytes` / `keyRPM` 现有语义；不引入外部 tokenizer 依赖。
- 本期不做按 `agent_kind` 差异化阈值、不做单会话 token 增量环比告警（future，记入 Learning）。

## 回滚

- 配置层：三项全设 0（或删 `guardrails` 块）热生效即回退，零回归。
- 代码层：`git revert` 实现提交（guardrail.go + handler 挂点 + config/main + 测试 + 本文档），全部为新增行/新增文件，无删除与协议变化。
- 部署：`deploy-prod.ps1` 内建备份与失败自动回滚。

## Learning（本切片）

根因：**上下文无界增长 + 多会话并发无控**是「图片识别失败 / agent-busy / 费用爆炸」的共同病根；模型与 max_tokens 只是触发器。
流程学习：AI 转述的排查归因（「19:56 配置变更」）与实弹不符，用网关 request-logs + 状态文件时间戳核实后才成立——本切片全部必要性数据均来自 request-logs 实测。
