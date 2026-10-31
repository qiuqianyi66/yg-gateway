# ADR-019：上下文护栏——请求前估算 + 双阈值 + 会话限流

> 状态：**草案待拍板**（M1）。
> 依据：REQ-context-guardrail-2026-10-31（立项）+ 2026-10-29~31 request-logs 实测（observation）。
> 决策点：估算口径、双阈值默认值、拒绝语义、会话限流是否本期落地。

## 背景

实弹观测（10-29~31）：单会话 prompt 无界增长是常态（30 万+ 占 53–97.7%，最大 82.8 万），
多会话并发（单分钟峰值 20 请求）放大每条请求的「全量历史重算」成本，导致费用爆炸、
TTFB 恶化、上游 `context canceled`、客户端 agent-busy、图片识别失败。网关需要服务端护栏，
与客户端侧会话治理（另一切片）形成双保险。

## 候选对比

### D1：请求前用什么数做判定？

- **候选 A：请求前估算**（`count_tokens.go` 现成的 `estimateTokensJSON`，derivation）。
  理由：拒绝/告警决策必须发生在派发上游**之前**；上游 usage 请求后才可得。
- 候选 B：等上游返回 usage 再事后告警。只能事后记账，拦不住「这一条」的伤害，且无拒绝能力。
  **否决**。
- 候选 C：引真 tokenizer（如 tiktoken）。精确但引外部依赖、按字节/字符估算已够护栏判定，
  违背「单二进制零依赖」定位。**否决**。

**决策：A。估算值为 derivation，/status 暴露「估算 vs 上游 usage」对照样本供校准。**

### D2：阈值形态与默认值？

- 候选 A：单硬阈值（超即拒）。今天 40% 请求超 50 万——一刀切拒绝会误伤大量存量会话。
- 候选 B：**双阈值**——软阈值（warn，不阻断，打标 + 面板可查）+ 硬阈值（blocked，拒绝）。
  默认 warn 500,000 / blocked 800,000：今天 40% 命中 warn（作为「需治理」信号）、0 条误伤
  blocked；昨天峰值 82.8 万会被拦（拦极端失控）。
- 候选 C：按 agent_kind 差异化阈值。今日证据不足（词表本身是 inference），不做。

**决策：B。软硬分离，默认 50 万 / 80 万，实施时按对照样本校准；阈值 0 = 关（零回归）。**

### D3：硬拒语义？

- 候选 A：413 + code `context_length_exceeded` + `Retry-After: 60`。与 `MaxBodyBytes` 同族
  （实体过长），客户端按 4xx 退避，语义明确。
- 候选 B：429 + Retry-After。429 已有 `server_busy`/全局准入用法，此处是**请求自身过大**，
  非服务繁忙，混用会误导客户端重试策略。**否决**。
- 候选 C：排队等上游。掩盖问题 + 队列复杂度，与 ADR-003 全局准入同理由。**否决**。

**决策：A。不排队、不降级、不选号，拒绝要便宜。**

### D4：会话级限流做不做？

- 候选 A：本期做，按 `sessKey` 固定窗口（复用 keyrpm.go 自写风格）。`sessKey` 已在阶段②
  派生（V3-10 已挂 session_fp），无新哈希。默认 0 = 关，用户按需开。
- 候选 B：本期不做，先观察 warn 数据。护栏最小集（warn+blocked）已覆盖核心伤害；但
  「单会话风暴打穿池子」与 keyRPM（按 key）是正交维度，sessKey 资产现成、实现成本低。

**决策：A（纳入本期）。固定窗口语义与 keyRPM 逐位一致（record 记真实速率、先 record 后
allow、第 RPM+1 次被拒），散客（sessKey 空）不计。**

### D5：webhook 告警怎么做才不吵？

- 候选 A：每次命中都发。40% 命中率下告警风暴，webhook 直接失效。
- 候选 B：**按 key + 1 分钟去重，最多 1 条/分钟**；完整命中统计进 reqlog 事件标记 +
  `/status` 聚合（面板可查）。告警只做「提醒有人要看」，明细走面板。

**决策：B。warn 命中打标进 reqlog（`guardrail_hit=warn|blocked`），不额外产出 wire 字段
（沿用 V3-10 的 omitempty 三值纪律）。**

### D6：命中怎么进观测层？

- `reqlog.Event` 加 `guardrail_hit`（omitempty 三值：`warn` / `blocked` / 缺失 = 未命中，
  与 `SessionFP/AgentKind` 同族——旧归档行读回空串，wire 不产出空键）。
- `/status` 加 `guardrail_stats`：warn/blocked/session_rpm 命中计数 + `last_hit_at` +
  `calibration_samples`（最近命中估算 vs 上游实际 `prompt_tokens`，≤20 条；上游缺失的
  样本**不进**对照——unknown 不冒充 0）。复用 `agentProfileSnapshot` 的「请求时现读
  archive 1000 条 + 纯函数聚合」路径，不引入常驻 goroutine。
- 决策理由：护栏命中是四态里的 **observation**（估算值本身是 derivation，但「命中阈值」
  是确定事实）；对照样本用于校准 `estimateTokensJSON` 权重（权重是 derivation，真机可调）。

## 怎么回滚

配置三项全设 0 热回退（零回归）；代码 `git revert` 实现提交（纯新增文件/行）；部署走
`deploy-prod.ps1` 内建备份与自动回滚。

## 诚实记录

- 估算值是 **derivation**，拒绝线不能读成「精确 token 计数」；校准样本来自 `/status` 对照。
- `session_rpm` 按 `sessKey` 限流——`sessKey` 本身可能是客户端显式 conversation_id
  （observation）或内容派生键（inference），四态纪律下它落在 derivation 档（沿用 V3-10 口径）。
- 本护栏**不替代**客户端侧会话治理（压缩/拆新/图片即弃），两者目标同向、手段互补。
