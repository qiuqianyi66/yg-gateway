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

### 第二批（R10 红线修复 + 可观测补齐）
- `server/handler.go`：**修 R10 红线**——`fallbackForward` 此前 `_, _ = io.Copy(...)`
  静默吞掉传输错误，把「客户端断连 / 备用源半截流」一律记成成功转发；改为
  `newFlushWriter` 逐块 flush + 检查 `io.Copy` 错误，半截流返回 false 并记日志。
  非流式 `io.ReadAll` 失败同样不再伪装成功。
- `server/handler.go`：`applyErrorPolicy` 成为**上游错误分类计数的唯一汇聚点**
  （所有轮转失败分支都过这里，避免各调用点漏记/口径漂移）；流式分支在真拿到
  首帧时观测 TTFB（`cfg.TTFB`，0 = 无观测，空流/断连不记）。
- `metrics/histogram.go`：新增 `KindCounters`（按离散标签计数，标签数有界 64，
  空标签记 `(none)`，nil 接收者安全）。
- `server/handler.go` Config：新增 `TTFB *metrics.Registry` 与
  `ErrKinds *metrics.KindCounters`（均可选，nil = 不观测/不计数）。
- `server/metrics_handler.go`：暴露 `upstream_errors_total{kind}` 与
  `ttfb_ms` 直方图（全局 + model 标签）。
- `cmd/server/main.go`：装配 `ttfbReg` + `errKinds` 并注入 handler。

### 第二批的验证
| 测试 | 覆盖 |
|---|---|
| `TestFallbackForwardStreamTruncated` | 半截 SSE 流返回 false（还原成 `_, _ =` 会 FAIL） |
| `TestFallbackForwardStreamFlushes` | SSE 透传逐块 flush（断言 `ResponseRecorder.Flushed`） |
| `TestErrKindCountedOnErrorPolicy` | 分类计数由 applyErrorPolicy 统一汇聚（去掉 Inc 会 FAIL） |
| `TestMetricsTTFBAndErrorKinds` | TTFB 分桶 + 错误分类计数出现在 /metrics |
| `TestKindCounters*`（4 例） | 计数 / 空标签 / 上界有界 / nil 安全 |

### 门禁
```
go test ./... -count=1   → 21 包全绿（18 ok + 3 no test files）
go vet ./...             → 无输出
gofmt -l ./cmd ./internal → 仅 3 个历史文件漂移（config.go/httpauth/index.go/panel.go，
                            pre-existing，本轮未动）
二进制                   → 10.6MB（-trimpath -ldflags="-s -w"）
```
注：本机无 gcc，`-race` 不可用（CGO 不可开），并发路径以「设计 + 单测」保证。

## 三、部署与回滚

- 二进制备份：`D:\yg-gateway\yg-gateway.exe.pre-hotpath.bak`（第一批）、
  `yg-gateway.exe.pre-obs.bak`（第二批，R15）。
- 配置：两个新字段缺省即旧行为，**无需改配置**；如需开启见 ADR-006 §四。
  TTFB / 错误分类观测为**自动装配**（main 里创建并注入 handler），无需配置。
- 回滚：回退二进制即可；本轮不改任何落盘格式，无数据迁移。

## 四、真机验证记录

| 批次 | PID | 验证 |
|---|---|---|
| 第一批 | 15380 | healthz 3/4；/metrics 12 个指标族 + 真实计数（requests_total{succeeded}=3、tokens_total{kind=total}=15616、cache_hit=1280）；/v1/models 200（20 模型）；非流式 200 + usage（credit=0.04）；流式 SSE 全链路（reasoning → content → finish_reason=stop → usage → [DONE]，无 ping 帧=默认关） |
| 第二批 | 36508 | healthz 3/4；/metrics 新增 `ttfb_ms_count=12`（按模型 hy4-preview=2 / deepseek-v4.1-flash=7 / step-5-preview=3）与 `upstream_errors_total` 族 |

## 五、下一轮候选

1. Prometheus 指标补在途/租约时序（当前只有瞬时 gauge）
2. 长流场景显式开启 `sse_keepalive_seconds` / `stream_total_timeout_seconds` 的实测调参
3. pick 侧 scratch 切片的 sync.Pool 复用（收益待 bench 量化）
