# 变更记录：热路径去重解析 + 流式可靠性 + 可观测（2026-10-14）

> 分支：`main`（自托管单仓库，按开发流程规范以本目录 + git commit 做变更追踪）
> PRD：`docs/prd/REQ-yg-gateway-hotpath-optimization-2026-10-14.md`
> ADR：`docs/adr/ADR-006-hotpath-streaming-observability.md`
> 级别：L2（热路径重构 + 流式行为 + 新端点）

## 一、改了什么

### F-701 入站单次解析
- 新增 `internal/session/requestview.go`：`RequestView` 惰性视图（顶层一次解析，
  messages/tools/metadata 按需且只解析一次）。
- `session` 新增 View 变体：`ExtractKeyOptView` / `TurnKeyView` /
  `ResolveConversationIDView`；`deriveKeyView`、`userContentSignatureRaw`、
  `messageTextRaw` 与旧实现同口径。
- `upstream` 新增 `ToolNameAllowlistView`（容错口径与字节版一致）。
- `server`：`chatCompletions` 改为单次 `ParseRequestView`，peek/工具名单/会话键/
  轮级键/conversationId/带图判定全部改读视图；`newChatStat` 改收已解析 model；
  新增 `hasImageView`。

### F-702 出站单次往返
- `upstream/payload.go`：抽出 `prepareBodyAST`，新增
  `PrepareBodyOptWithEffortsAndDefaultAndCache`（规范化 + cache key 同一 AST，
  只 marshal 一次）。
- `upstream/cache_key.go`：抽出 `promptCacheKeyFrom(obj, uid, conv)`。
- `upstream/client.go`：`prepareBody` 改走单趟入口。

### F-703 degrade 去锁
- `server/degrade.go`：`until` 由 `time.Time`+mutex 改为 `atomic.Int64`（UnixNano）；
  `Active()` 全程无锁，`Trigger()` 仍在 mutex 下做「判定+置位」。

### F-704/705/706/707 流式可靠性
- `upstream/sse.go`：
  - `StreamHint` 设完头立即 `WriteHeader(200)` + `Flush`（首帧前下发 headers）；
  - 新增 `readSSELine`（`ReadSlice` 累积 + `maxSSEFrameBytes` 1MiB 上限）与
    `errFrameTooLarge` / `IsFrameTooLarge`；
  - `Aggregate` 加 `maxAggregateBytes`（16MiB）累计上限；
  - 新增 `WithKeepAlive(d)` + 包级 `SetSSEKeepAlive` / `SSEKeepAlive`
    （SSE 心跳，默认关）；写入路径在开启时经互斥锁串行。
- `upstream/client.go`：新增 `Client.StreamTotalTimeout`（整请求 deadline，
  在调用方 ctx 之上派生）。
- `server/handler.go`：流式分支传 `WithKeepAlive`（按配置）、新增
  `IsFrameTooLarge` 分支（502 观测，不罚号不轮转）。
- `cmd/server`：新增 `upstream.stream_total_timeout_seconds` /
  `upstream.sse_keepalive_seconds`（均装配期，缺省 0 = 关/不限），main 接线。

### F-708/709 刷新链路
- `upstream/client.go`：新增 `RefreshTokenContext(ctx, a)`（ctx 继承）+ `refreshGroup`
  （按 uid singleflight）；`refreshTokenLocked` 用调用方 ctx 叠 I/O 上限。
- `server/handler.go`：refresh 调用改传 `r.Context()`。

### F-710 `/metrics`
- 新增 `internal/metrics/prom.go`：Prometheus text exposition 构造器（零依赖）。
- 新增 `internal/server/metrics_handler.go`：`GET /metrics`（withAuth + rateLimited，
  与 `/status` 同口径）。
- `internal/metrics/histogram.go`：新增 `Registry.Global()`、`Snapshot.UnsafeHistogram()`。
- `internal/pool/pool.go`：新增 `InFlightTotal()`（账号级在途之和）。
- `server/handler.go`：注册路由 + `notFound` 已知路径表补 `/metrics`。

## 二、怎么验证的

### 等价性守门（本轮的核心风险控制）
| 测试 | 覆盖 |
|---|---|
| `TestViewEquivalenceWithBytePaths` | 22 组 body × derive on/off × 3 个键（会话/轮级/conversationId）逐位对照 |
| `TestToolNameAllowlistViewEquivalence` | 11 组 body，`reflect.DeepEqual`（含畸形条目容错口径） |
| `TestPrepareBodySingleRoundTripEquivalence` | 12 组 × sanitize 两态，单趟 vs 两趟逐字节对照 |
| `TestRequestViewStream` / `TestRequestViewModelKinds` | stream 布尔与 model 四态与旧实现对照 |

### 新增行为测试
`TestStreamHintHeadersBeforeFirstFrame`、`TestStreamHintFrameTooLarge`、
`TestAggregateFrameTooLarge`、`TestAggregateTotalTooLarge`、
`TestStreamHintKeepAlivePings`、`TestStreamHintDefaultNoKeepAlive`、
`TestRefreshTokenContextCancelled`、`TestRefreshTokenSingleFlight`、
`TestRefreshTokenSingleFlightSequentialReuses`、
`TestMetricsEndpointExposition`、`TestMetricsRequiresAuth`、
`TestMetricsMethodNotAllowed`、`TestMetricsDegradesWithoutOptionalDeps`、
`TestEscapeLabel`、`TestUpstreamStreamTotalAndKeepAlive`、
`TestDegradeGateActiveIsLockFree`、`TestPrometheus*`（4 例）。

### 门禁
```
go test ./... -count=1   → 20 包全绿（17 ok + 3 no test files）
go vet ./...             → 无输出
gofmt -l ./cmd ./internal → 仅 3 个历史文件漂移（config.go/httpauth/index.go/panel.go，
                            pre-existing，本轮未动）
二进制                   → 10.6MB（-trimpath -ldflags="-s -w"）
```
注：本机无 gcc，`-race` 不可用（CGO 不可开），并发路径以「设计 + 单测」保证。

## 三、部署与回滚

- 二进制备份：`D:\yg-gateway\yg-gateway.exe.pre-hotpath.bak`（R15）。
- 配置：两个新字段缺省即旧行为，**无需改配置**；如需开启见 ADR-006 §四。
- 回滚：回退二进制即可；本轮不改任何落盘格式，无数据迁移。
