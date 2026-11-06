# 变更记录 — magpie 节奏面收口：并发闸 + 保活确定性分片（2026-11-05）

> REQ：`docs/prd/REQ-2026-11-06-magpie-rhythm-closeout.md`（本目录内）。
> 计划：`docs/plans/2026-11-05-magpie-rhythm-closeout.md`（plan-control 门 1 已批）。
> ADR：`docs/adr/ADR-024-concurrency-gate-and-rhythm.md`、`docs/adr/ADR-025-keepalive-deterministic-spread.md`。

## 改了什么

**F1 单账号在途并发闸 + rhythm 可观测**
- 新增 `internal/upstream/concurrency.go`：`concurrencyLimiter`（FIFO 等待队列，
  acquire 满即阻塞、ctx 取消不泄漏槽位）、`concurrencyRoundTripper`（外层挂载，
  透传 CloseIdleConnections）、`Client.concurrencyWait/Release`（X-Machine-ID 分桶，
  0=直放）、`Client.RhythmStatus()`（每账号 in_flight/queue_depth）。
- `internal/upstream/client.go`：`ConcurrencyPerAccount` + `concMu`/`concLimiters`。
- `internal/upstream/transport.go`：`concurrencyRoundTripper{rpmRoundTripper{tr}}` 双层。
- `internal/server/status.go`：`/status` 只增键 `rhythm` + `rhythmSnapshot()`。
- `cmd/server/config.go` + `main.go` + `config.example.json`：`upstream.concurrency_per_account`
  （默认 0 关闭、负值钳 0、装配期接线）。
- 测试：`internal/upstream/concurrency_test.go`（4 个）、
  `internal/server/handler_rhythm_test.go`（1 个）。

**F2 保活确定性分片**
- `internal/scheduler/scheduler.go`：`keepaliveSpread`（10min 包级 var，activityAccountDelay
  同口径——不做配置化以免 config 噪音，与计划里 keepalive_spread_sec 的偏差在此声明）；
  `keepaliveOffset(uid)` = fnv32a(uid)%spread 秒；`runKeepalive(ctx)` 按偏移升序睡到
  各自计划时刻（单号每天同一相对位置、总时长 ≤ spread、ctx 取消放弃剩余）；
  `RunKeepaliveNow()` 改为 background ctx 包装；`runBatch` taskKeepalive 走 ctx 版；
  每号打 slot 日志（spread>0）。
- 测试：`internal/scheduler/keepalive_spread_test.go`（3 个 + testMachineID 派生桩）。

## 为什么之前没拦住

- 并发闸：RPM 硬闸（081e200）只对齐"速率"一个风控维度；REA §2.7 对在途并发自承
  unknown（magpie 自己也没有 per-账号观测），照抄清单时把未实现部分标 unknown 后就不了了之
  ——**"标 unknown" 不等于"登记待办"**，这是 L-22/L-15 复合面。
- 保活分片：保活从引入起就是"立即全刷"，从未用风控视角审视"N 个登录动作同刻"的形状；
  且 magpie 的 8 天周期被（正确地）拒了，连带把"错峰"这一层也一并丢了。

## 怎么验证的

- **牙齿实跑（先红后绿）**：
  - F1：`concurrencyWait` 的 key 强行改 `"global"` → `TestConcurrencyRoundTripperBuckets`
    exit 1（红）；恢复后 exit 0。
  - F2：`runKeepalive` 的 `timex.SleepCtx` 替换为 `_ = d` → `TestKeepaliveSpreadDeterministic`
    报「间隔 got 0s, want ≈ 1s」exit 1（红）；恢复后 exit 0（-count=5 稳定）。
- 全量 `go test ./...`（30 包）、`go vet ./...`、`gofmt -l cmd internal`（空）、
  `scripts/pre-commit.ps1` 11 项全绿（含 check-harness 检项 1-10、上下文字节预算）。
- 生产部署后冒烟三条（/healthz、/v1/models、流式+非流式）。

## 怎么回滚

- F1：`config.json` 里 `upstream.concurrency_per_account` 置 0（装配期即关，零状态）；
  二进制回滚 `scripts/deploy-prod.ps1 -Exe <备份>`。
- F2：默认 10min 分片是安全方向（窗口内散布），如需回到"立即全刷"，回滚二进制即可
  （代码改动无持久状态、无请求报文变化）。
- 无数据迁移、无外部副作用、无 wire 形状变更（/status 只增键，消费方兼容）。
