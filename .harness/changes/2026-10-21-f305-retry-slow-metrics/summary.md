# F-305 可观测性补齐：重试放大率、慢请求追踪

> 需求：`docs/prd/REQ-yg-gateway-deep-optimization-2026-10-12.md` 第 87 行（F-305，P1）
> 分级：L2（业务逻辑 / 观测口径）
> ADR：不需要——F-305 的口径决策在 PRD §六#3（不引 hdrhistogram，沿用内置固定分桶）与
> ADR-006 D-710（/metrics 自实现 Prometheus 文本导出）已覆盖，本轮无新权衡。

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/metrics/attempts.go` | `AttemptsHistogram`（固定整数分桶 1/2/3/4/6/8/+Inf，atomic 桶）、`AttemptsStats`（全局+按模型注册表，同 Registry 范式，maxModels=200 防伪造 model 名无界增长）、无标签 `Counter`（慢请求信号）。零新依赖。 |
| `修改：internal/metrics/prom.go` | 新增 `HistogramInt`：整数直方图暴露为 `_bucket{le="n"}` + `_sum` + `_count`。 |
| `新增：internal/server/retry_metrics.go` | `observeRequestEnd`：请求收口（defer 挂载）观测点——放大率直方图 + 慢请求计数，成功/失败/轮转耗尽/fallback 全结局恰好一次。 |
| `修改：internal/server/handler.go` | Config 加 `Attempts *metrics.AttemptsStats` / `SlowRequests *metrics.Counter` / `SlowRequestThreshold time.Duration`；chatCompletions 加 `defer h.observeRequestEnd(st, prep.bareModel)`。 |
| `修改：internal/server/metrics_handler.go` | /metrics 暴露新指标族：`yg_gateway_upstream_attempts_per_request`（histogram，全局+按模型）、`yg_gateway_upstream_attempts_total`（counter）、`yg_gateway_slow_requests_total`（counter）。 |
| `修改：cmd/server/main.go` | 构造 `NewAttemptsStats()` + `NewCounter()` 并注入 Config；阈值由配置换算。 |
| `修改：cmd/server/config.go` | 新增 `observability.slow_request_threshold_seconds`（缺省 120，<=0 回落 120）。 |
| `新增：internal/metrics/attempts_test.go` | 分桶落点 / nil 安全 / 按模型维度 / Counter。 |
| `新增：internal/server/retry_metrics_test.go` | 收口桶位、慢阈值两侧+阈值=0关闭、端到端 defer 钩子、exposition。 |

**指标语义**：
- `yg_gateway_upstream_attempts_per_request`：每个「打到过上游」的用户请求的上游尝试次数
  直方图。le=1 = 无换号重试；>1 = 发生重试/换号。放大率均值读法：
  `upstream_attempts_total / requests_total{outcome="completed"}`（0 次尝试的网关本地
  拒绝请求在分母里作 0，不进直方图——避免污染 le=1 桶）。
- `yg_gateway_slow_requests_total`：端到端耗时超过阈值（默认 120s）的完成请求计数。
  纯观测，不改写请求路径；阈值 <=0 = 关闭（旧配置零回归）；无后台 goroutine（R9）。
- 标签：仅 `model`（上游目录有界）；UID 绝不做标签（R2）。

**没做的（明写）**：
- 未引 hdrhistogram / 任何 go.mod 新依赖（PRD §六#3 拍板）。
- 慢请求计数器未做 model 标签维度（模型维度已在 request_duration_ms 直方图里，本信号
  只服务「超阈值几次」告警）。
- 未动 docs/（PRD 回填由另一 shard 负责）、internal/pool、internal/panel。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 受影响包测试 | `go test ./internal/metrics/... ./internal/server/... ./cmd/...` | 全 ok |
| vet | `go vet ./...` | 通过 |
| 格式 | `gofmt -l internal/metrics/ cmd/server/ internal/server/` | 无输出（attempts.go 初始有一处运算符间距漂移，已 gofmt -w 修平） |
| 全量回归 | `go test ./...` | 20 ok + 3 no test files（与基线一致） |
| 深度棘轮 | `scripts/check-depth.ps1 -Detail` | ✅ 深度指标未回退（>1000 行文件数仍=5，最大 2058） |

**负向探针**（均实际还原验证后恢复，三处全红，与预期一致）：
1. **摘掉 chatCompletions 的 defer 钩子** → `TestF305DeferHookRunsOnRealChat` FAIL
   （「收口观测次数 = 0 want 1」）。
2. **摘掉 observeRequestEnd 里的 `SlowRequests.Inc()`** → `TestObserveRequestEndSlowThreshold`
   FAIL（「above threshold: slow=0 want 1」）。
3. **整段摘除 metricsHandler 的 F-305 exposition 段** → `TestMetricsF305Exposition`
   12 条断言全部 FAIL（missing HELP/TYPE/样本行）。

## 三、未完成 / 待补（诚实清单）

- [ ] 生产部署后观察：默认 120s 阈值是否误报（requires：生产跑一段时间看
      `slow_requests_total` 与 `request_duration_ms` 直方图尾部是否一致）。
- [ ] 放大率在生产的真实分布未采样（requires：跑几天看 `upstream_attempts_per_request`
      的 le=1 桶占比，确认账号池健康度）。
- [ ] config 层不支持「显式关闭慢请求」（<=0 会回落默认 120s，与
      RequestRetentionDays 同范式）；handler 层阈值=0 可关，测试已钉住。需要配置级
      关闭时用 *int 指针语义（同 MaxBodyBytes），本轮不做。

## 四、怎么回滚

纯代码改动：`git revert <本提交>` 即可。无落盘格式变更（state.json / usage.json /
归档均未动），无 go.mod 变更。新增配置字段缺省即生效（120s），回滚后该字段被忽略，
无需改 config.json。

## 五、Learning

根因：chatCompletions 的收口点天然唯一（defer），观测点挂 defer 即可覆盖全结局；
但 defer 参数在 defer 语句处即求值——第一次接线时 defer 放在变量解构之前直接引用
`bareModel`，编译期才报 undefined，改用 `prep.bareModel` 化解（已在注释里写明，防重排）。
