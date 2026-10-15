---
last_updated: 2026-10-14
status: 已确认（YG 授权「继续优化网关流程，只要结果」→ 直接实施）
owner: YG
modification_level: L2（热路径重构 + 流式可靠性 + 可观测导出）
research: 静态审阅（本仓热路径）+ 外部竞品/网关调研（new-api / one-api / Cloudflare AI Gateway / gpt-load）
---

# YG Gateway 热路径优化轮 — 需求分析（PRD）

> 背景：ADR-005（选号去写锁 + EWMA）已交付。本轮把目光从「选号算法」移到
> **请求本身**：同一份 body 在热路径上被反复全文档 JSON 解析、流式缺少
> 整请求兜底、可观测缺少时序导出。
>
> 流程：按十阶段流水线，阶段 1-3 合并产出本 PRD（L2 全流程中的轻量前置），
> 阶段 4-10 见 ADR-006 与本仓 `.harness/changes/hotpath-optimization-2026-10/`。

---

## 一、范围边界

### 做什么

| 方向 | 目标 |
|---|---|
| **A. 热路径 CPU/分配** | 入站 body 单次解析复用；出站 body 单次 JSON 往返 |
| **B. 流式可靠性** | 首帧前下发响应头、单帧/聚合流上限、SSE 心跳、整请求 deadline |
| **C. 凭证链路联动** | token 刷新继承请求 ctx（客户端断开即停）+ 同账号并发刷新合并 |
| **D. 可观测** | Prometheus 文本格式 `/metrics`（零依赖自实现） |

### 不做什么（明确排除）

1. **不引入新依赖**：`/metrics` 自实现 text exposition，不引 prometheus/client_golang（项目哲学「最少依赖」）。
2. **不改上游协议报文**（R8 红线）：`wb2api-<ms>` conversationId、`chatcmpl-wb2api` 哨兵、UA 指纹一律不动。
3. **不动选号承重墙语义**：成本分层 / 冷却 / 熔断 / EWMA 权重公式零改动（ADR-005 已定）。
4. **不做请求级响应缓存 / 请求合并**：账号池场景下「同一 body 并发合并」会改变上游计费与养号节奏，需单独立项。
5. **不做多租户计费/配额**（沿用既有排除项）。

---

## 二、需求（F 编号）

### A. 热路径（P0）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-701 | **入站 body 单次解析**：一次 `json.Unmarshal` 同时取出 stream / model(含原始类型) / tools 名单 / 会话键 / 轮级键 / conversationId / 是否带图 | P0 |
| F-702 | **出站 body 单次往返**：payload 规范化与 prompt_cache_key 注入在同一 AST 上完成，只 marshal 一次 | P0 |
| F-703 | **degrade 判定去锁**：`degradeGate.Active()` 改原子读，消除每请求一次 mutex | P1 |

**F-701 验收（Given-When-Then）**
- Given 一个含 tools + 多轮 messages 的大 body / When 处理一次 `/v1/chat/completions` / Then 入站侧全文档 `json.Unmarshal` 次数 == 1（可解析时），其余消费方从同一结构体取值
- Given body 不可解析 / When 同样处理 / Then 各消费方回落既有空值语义（不粘性、无工具名单、模型名 "-"，与改动前逐位一致）
- Given 关闭内容派生（`session_sticky.derive=false`）/ When 请求无显式会话标识 / Then 仍返回空会话键（ADR-004 契约不回退）

**F-701 现状证据（静态审阅 + 既有 bench）**
```
handler.go:842  peek(stream/model)            ← 第 1 次全文档解析
handler.go:883  newChatStat → parseModelFromBody ← 第 2 次
handler.go:856  upstream.ToolNameAllowlist     ← 第 3 次
handler.go:908  session.ExtractKeyOpt          ← 第 4 次
handler.go:924  session.TurnKey                ← 第 5 次
handler.go:1089 session.ResolveConversationID  ← 第 6 次
handler.go:938  reqHasImage（惰性，仅错误路径）
internal/server/hotpath_bench_test.go 已固化 6 次解析的基线
```

**F-702 现状证据**
```
client.go:787 prepareBody
  → payload.go:35  unmarshal  → payload.go:87 marshal      （第 1 趟）
  → cache_key.go:40 unmarshal → cache_key.go:56 marshal    （第 2 趟，背靠背）
每次 ChatStreamContext 都跑一遍；轮转最多重复 MaxRotate 次。
```

### B. 流式可靠性（P0）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-704 | **首帧前下发响应头**：SSE headers set 后立即 `WriteHeader(200)` + Flush，让客户端 TTFB 感知与首 token 解耦 | P0 |
| F-705 | **单帧/聚合流上限**：SSE 逐行读取与非流式 `Aggregate` 都设单帧/整流体量上限，超限按「上游畸形」终止（不 OOM） | P0 |
| F-706 | **SSE 心跳**：流中静默超过阈值时写 `: ping\n\n` 注释帧，防中间层按空闲掐连 | P1 |
| F-707 | **整请求 deadline**：出站 chat 增加「整请求总时长」上限，与首字节上限、流中空闲上限三件套并存 | P1 |

**F-704 验收**
- Given 上游已回 200 但迟迟不发首帧 / When 客户端观察 / Then 已收到 200 + `Content-Type: text/event-stream`（不再等首 token）

**F-705 验收**
- Given 上游持续吐一个不含换行的超长 `data:` 行 / When 超过上限 / Then 网关终止该流并返回错误，**不**把该行整体读进内存
- Given 非流式聚合响应体超过上限 / When 读取 / Then 同上（对齐 `doJSON` 既有的 `io.LimitReader(1<<20)` 口径）

**F-707 现状证据**
```
client.go:693  ChatHTTP.Timeout = 0（显式不限总时长）
transport.go:48 ResponseHeaderTimeout = 60s（只管首字节前）
idle.go:49     monitorBody（只管流中静默）
→ 缺少「整请求」这一层：滴字节式上游可无限占住请求/租约/全局名额。
```

### C. 凭证链路（P1）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-708 | **refresh 继承请求 ctx**：`RefreshToken` 增加 ctx 变体，handler 传 `r.Context()`，客户端断开/停机即中断刷新 | P1 |
| F-709 | **同账号并发刷新合并**：按 UID singleflight，避免 N 个并发临期请求各打一次 refresh 端点 | P1 |

**F-708 验收**
- Given 账号 token 临期且客户端在 refresh 期间断开 / When 观察 / Then 刷新请求随 ctx 取消而返回，不占满 30s

### D. 可观测（P1）

| F-编号 | 需求 | 优先级 |
|---|---|---|
| F-710 | **`/metrics` Prometheus 文本导出**：请求/成功/失败计数、在途 gauge、延迟与 TTFB 分桶、按错误分类计数、用量 token 计数 | P1 |

**F-710 验收**
- Given 服务运行 / When `GET /metrics`（带鉴权） / Then 返回 `text/plain` exposition，至少含 `yg_gateway_requests_total` / `yg_gateway_requests_in_flight` / `yg_gateway_request_duration_ms` 分桶 / `yg_gateway_ttfb_ms` 分桶
- Given 未启用鉴权 / When 请求 / Then 与 `/status` 同口径（不鉴权放行）

---

## 三、影响分析

| 维度 | 结论 |
|---|---|
| 账号池/积分/调度语义 | **不变**（选号公式、冷却、熔断、EWMA 全不动） |
| Go 分层 | 新增 `internal/server/reqparse.go`；`internal/session` / `internal/upstream` 增加「已解析对象」消费入口（旧函数保留） |
| 面板 | `go:embed`，本轮不改前端 → **无需重编译面板语义**，但部署仍按 R13 重新构建二进制 |
| 配置 | 新增 `upstream.stream_total_timeout_seconds`、`upstream.sse_heartbeat_seconds`；均**装配期字段**（需重启），按 R11 标注 |
| 上游报文 | **不动**（R8）。单帧上限只影响畸形/超长流的终止时机，正常流零影响 |

---

## 四、非功能约束

1. 零新依赖（`go.mod` 不变）。
2. 全量 20 包测试绿 + `go vet` + `gofmt -l` 干净。
3. 每项改动配「有牙齿」的测试（还原 bug 必须 FAIL）。
4. 二进制替换前备份（R15）；部署后真机冒烟（healthz + 流式 + 非流式）。

---

## 五、任务拆分（≤4h / 项）

| # | 任务 | 对应 F |
|---|---|---|
| T1 | `internal/server/reqparse.go`：单次解析结构体 + 各消费方取用 | F-701 |
| T2 | `internal/session` / `internal/upstream` 增加 From/已解析变体 | F-701 |
| T3 | `payload.go` + `cache_key.go` 合并为单趟（新增 `PrepareBodyFull`） | F-702 |
| T4 | `degrade.go` 改 atomic | F-703 |
| T5 | `sse.go`：header 先行 + 单帧上限 + 心跳；`Aggregate` 限流 | F-704/705/706 |
| T6 | `upstream`：整请求 deadline（新 config 字段） | F-707 |
| T7 | `RefreshTokenContext` + 按 UID singleflight | F-708/709 |
| T8 | `internal/metrics` + `handler /metrics` | F-710 |
| T9 | 全量验证 + 部署 + 文档 | 全部 |
