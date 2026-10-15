---
last_updated: 2026-10-14
status: 已实施
owner: YG
adr_number: 006
prd: docs/prd/REQ-yg-gateway-hotpath-optimization-2026-10-14.md
---

# ADR-006 热路径去重解析 + 流式可靠性三件套 + 可观测导出

> 来源：本轮「继续优化网关流程」（用户 2026-10-14 授权，只取结果）。
> 前序：ADR-005（选号去写锁 + EWMA）解决了**选号算法**侧的问题；
> 本文处理**请求本身**：同一份 body 被反复解析、流式缺整请求兜底、指标无时序导出。
> 本文记录 D-700 ~ D-710 的决策、等价性契约、兼容边界与回滚。

---

## 一、背景

静态审阅（本仓热路径）+ 外部竞品调研（new-api / one-api / Cloudflare AI Gateway /
gpt-load）后确认三类可量化浪费：

1. **入站 body 被反复全文档解析**：一个 `/v1/chat/completions` 请求在热路径上
   触发 6 次以上全量 `json.Unmarshal`——peek(model/stream)、日志取 model、
   工具名单、会话键、轮级键、conversationId，外加惰性的带图判定。
   本仓早已用 `hotpath_bench_test.go` 固化过这个基线。

2. **出站 body 每尝试两次 JSON 往返**：`prepareBody` 先跑 payload 规范化
   （unmarshal+marshal），紧接着跑 prompt_cache_key 注入（又一轮 unmarshal+marshal）；
   而它在 `ChatStreamContext` 内，轮转时重复 `MaxRotate` 次。

3. **流式缺两层兜底**：
   - 响应头惰性下发——上游「已接受请求但迟迟不吐首 token」期间客户端看不到任何东西；
   - 只有「首字节前」与「流中静默」两个超时，缺**整请求**上限——上游持续滴字节
     即可无限占住请求/账号租约/全局在途名额；
   - 无单帧/聚合上限——`bufio.ReadString` 遇到不吐换行的流会一直读到宿主内存上限。

---

## 二、D-700 入站单次解析（`internal/session.RequestView`）

**决策**：新增惰性视图 `session.RequestView`，顶层只解析一次（字段名 → 原始字节），
子树（messages / tools / metadata）**按需解析且只解析一次**。各消费方改用 `*View` 变体：

| 消费方 | 新入口 |
|---|---|
| peek(model/stream) | `View.Model()` / `View.Stream()` |
| 日志 model | 复用已解析的 `modelStr`（`newChatStat` 改签名收 string） |
| 工具名单 | `upstream.ToolNameAllowlistView` |
| 会话键 | `session.ExtractKeyOptView` |
| 轮级键 | `session.TurnKeyView` |
| conversationId | `session.ResolveConversationIDView` |
| 是否带图 | `hasImageView`（错误路径惰性） |

**为什么顶层用 `json.RawMessage` 而不是 typed struct**：typed struct 在**任一字段
类型不符**时整体解析失败（`{"user_id":123}` 会让整份 body 解析失败，连 model 都拿
不到），而旧实现是逐字段 `strOrEmpty` 容错。RawMessage 永不因字段类型失败，逐字段
取用时才判定——语义与旧实现一致。

**等价性契约（本决策的验收核心）**：派生会话键、轮级键、conversationId、工具名单
必须与旧实现**逐位相同**，否则粘性绑定与上游聚合 ID 会静默漂移。
守门测试：
- `TestViewEquivalenceWithBytePaths`（22 组 body × 两种派生开关 × 三个键）；
- `TestToolNameAllowlistViewEquivalence`（11 组 body，`reflect.DeepEqual`）；
- `TestRequestViewStream`（与 `Unmarshal(&bool)` 逐值对照）。

**容错口径一并对齐**：`ToolNameAllowlistView` 在遇到畸形条目时**放弃整份名单**
（返回 nil），与旧实现「一次 typed Unmarshal 失败即 nil」一致——不做「更宽容」的
改进，避免严格/弱判定边界悄悄变化（标记修复层的判据）。这是刻意的保守选择。

---

## 三、D-701 出站单次往返

**决策**：抽出 `prepareBodyAST(obj, ...)`（只改 AST，不 marshal），新增
`PrepareBodyOptWithEffortsAndDefaultAndCache(src, ..., cacheUID, cacheConversationID)`
在**同一个 AST** 上先规范化、再注入 cache key，最后只 marshal 一次。
`prepareBody` 改走新入口。

**等价性契约**：与「先规范化再 InjectPromptCacheKey」两趟版本逐位等价——
规范化管线不动 `conversation_id` / `conversationId` / `prompt_cache_key`，
故 cache key 的会话哈希源取值不变。
守门测试：`TestPrepareBodySingleRoundTripEquivalence`（12 组用例 × sanitize 两态，
含 effort 降级、孤儿 tool 清理、别名翻译、畸形 body）。

**为什么保留旧入口**：`PrepareBodyOptWithEffortsAndDefault` 仍是「不注入 cache key」
的公开语义（有独立测试钉住），不破坏既有调用方。

---

## 四、D-702 流式可靠性三件套 + 两个上限

| 项 | 实现 | 默认 |
|---|---|---|
| **首帧前下发 headers** | `StreamHint` 设完 SSE 头立即 `WriteHeader(200)` + `Flush` | 恒开 |
| **整请求 deadline** | `Client.StreamTotalTimeout` → 调用方 ctx 之上再派生一层 deadline | `stream_total_timeout_seconds`，0 = 关 |
| **SSE 心跳** | `WithKeepAlive(d)`：静默超 d 写 `: ping\n\n` 注释帧 | `sse_keepalive_seconds`，0 = 关 |
| **单帧上限** | `readSSELine` 用 `ReadSlice` 累积，超 `maxSSEFrameBytes`（1MiB）返回 `errFrameTooLarge` | 恒开 |
| **聚合上限** | `Aggregate` 累计超 `maxAggregateBytes`（16MiB）返回 `errFrameTooLarge` | 恒开 |

**为什么心跳默认关**：注释帧会改变出站字节流，测试与既有客户端行为一并零回归；
由部署方按中间层形态显式开启。开启时写路径加互斥（心跳 goroutine 与主循环并发写
同一 `ResponseWriter`），且 goroutine 由 stop 通道 + WaitGroup 保证在函数返回前退出
（R9/R12）。

**为什么上限恒开**：上限只在**畸形流**上生效（正常帧远小于 1MiB），不改变正常
路径的字节；而没有上限就是一个可被上游触发的 OOM 面。超限语义：上游畸形，
handler 记 502 观测、**不罚号不轮转**（换账号撞同一份畸形响应）。

---

## 五、D-708/D-709 刷新链路

- **ctx 继承**：新增 `RefreshTokenContext(ctx, a)`，handler 传 `r.Context()`。
  此前用 `context.Background()`——客户端断开/停机都打断不了，最坏占满 30s，
  期间账号在途名额与全局名额一直被占。`RefreshToken(a)` 保留为 Background 包装。
- **singleflight**：`refreshGroup` 按 uid 合并并发刷新，N 个并发临期请求只打一次
  上游。等待方 ctx 取消即返回（不等 leader 的 30s）；leader 结束即 `delete`，
  map 不留条目（有测试钉住无泄漏）。

---

## 六、D-710 `/metrics`

自实现 Prometheus text exposition（零新依赖），装配在 `GET /metrics`，
鉴权与限流口径与 `/status` 完全一致。

暴露：`requests_total{outcome}` / `requests_in_flight` / `request_success_rate` /
`request_duration_ms` 直方图（全局 + `model=` 标签）/ `accounts{state}` /
`accounts_in_flight` / `global_in_flight` / `tokens_total{kind}` /
`tokens_cache_hit_total`。

**标签转义**：模型名来自客户端，伪造 model 名是真实攻击面——引号/反斜杠/换行
一律转义，否则暴露文本可被注入假指标行（有测试钉住）。

---

## 七、兼容边界

| 面 | 结论 |
|---|---|
| 选号/冷却/熔断/EWMA | **零改动**（ADR-005 公式不动） |
| 上游协议报文 | **零改动**（R8）：`wb2api-<ms>`、`chatcmpl-wb2api`、UA 指纹均未触碰 |
| 派生会话键 / 轮级键 / conversationId | **逐位相同**（守门测试覆盖） |
| 出站 body | **逐位相同**（两趟 vs 单趟对照测试覆盖） |
| 流式出站字节（默认配置） | **逐位相同**（心跳默认关） |
| 配置 | 新增两个字段，均**装配期**（需重启）；缺省 0 = 关闭/不限（零回归） |
| 新增端点 | `/metrics`（新路由，不影响既有路由） |

## 八、回滚

- 代码：`git revert` 本轮提交（或回退二进制，见 deployment 记录的 `.bak`）。
- 配置：两个新字段缺省即旧行为，无需改配置回滚。
- 数据：本轮**不改任何落盘格式**（state.json / usage.json / 归档 JSONL 均未动），
  回滚不涉及数据迁移。
