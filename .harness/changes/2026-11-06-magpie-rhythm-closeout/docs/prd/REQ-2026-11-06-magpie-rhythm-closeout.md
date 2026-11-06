# REQ — magpie 节奏面收口：并发闸 + 保活确定性分片（2026-11-05）

> 计划：`docs/plans/2026-11-05-magpie-rhythm-closeout.md`（plan-control 门 1，用户批准）。
> 吸收教训、不复制形态：magpie 的两处抽象价值（并发是独立风控维度、批量登录动作是风控面）
> 落成 yg 自身的可审计形态。

## 1. 为什么做

magpie REA §2.5 指出"RPM/并发硬闸"是一对，yg 此前只有 RPM（081e200）。本次只读实测确认
两处真实未吸收：

- **单账号在途并发闸**：`internal/upstream` 全仓 grep MaxConcurrency/semaphore/inflight 零命中
  ——RPM 管"多快"，不拦"同时几个"。短时间并发打满一个号（速率正常、在途高）是"把号打热"的
  另一条路（magpie concurrency.go "5-6 并发即风控"）。
- **保活错峰**：`RunKeepaliveNow`（scheduler.go 原 595-620）串行全刷所有号，22 点窗口内
  多号同刻批量登录动作。magpie keepalive.go 明确反对"一天内 N 个号一起刷"。

## 2. 为什么之前没有拦住

- 并发闸：RPM 硬闸引入时只对齐了"速率"这一个风控维度，REA §2.7 对在途并发自承 unknown
  （连 magpie 自己都没有 per-账号在途观测），抄它的清单时把未实现部分标 unknown 后就漏了。
- 保活错峰：保活逻辑从引入起就是"立即全刷"（`RunKeepaliveNow` 无窗口概念），没人对着
  风控视角审视过"N 个登录动作同刻"的形状。

## 3. 改了什么（F1/F2）

**F1 并发闸 + 可观测**（`internal/upstream/concurrency.go` 新增）：
- `concurrencyLimiter`：每账号在途计数 + FIFO 等待队列；`acquire(ctx)` 满则阻塞
  （ctx 取消返回 ctx.Err()，槽位转交竞态不泄漏）；`release()` 归还/转交。
- `concurrencyRoundTripper`：挂在 `newUpstreamHTTP` 外层（rpm 内层，正交），
  透传 `CloseIdleConnections`。
- `Client.ConcurrencyPerAccount`（装配期，0=关闭零回归）+ 配置
  `upstream.concurrency_per_account`（负值钳 0）。
- **yg 增量（magpie 没有的）**：`Client.RhythmStatus()` + `/status` 只增键 `rhythm`
  （每账号 {in_flight, queue_depth}）——可见优先于精确，与 proxy_status 同哲学。

**F2 保活确定性分片**（`internal/scheduler/scheduler.go`）：
- `keepaliveSpread` 包级 var（默认 10min，与 activityAccountDelay 同口径；不做配置化，
  避免 config 噪音——与计划里 keepalive_spread_sec 的偏差已在此说明）。
- `runKeepalive(ctx)`：按 `keepaliveOffset(uid)=fnv32(uid)%spread 秒` 升序、睡到各自计划
  时刻（同一号每天同一相对位置、总时长 ≤ spread、窗口内散布、ctx 取消放弃剩余）；
  `RunKeepaliveNow()` 保留为 background ctx 包装；`runBatch` 改走 ctx 版。
- slot 日志（spread>0 时每号一行计划时刻，可审计）。
- 不引入 magpie 式 8 天周期（yg 是每日主动保活策略，过重）。

## 4. 验收（Given-When-Then，已实测）

- Given concurrency_per_account=2、同账号并发 5 请求 → Then 在途 ≤ 2、FIFO 排队
  （TestConcurrencyLimiterFIFO / TestConcurrencyRoundTripperBuckets）。
- Given 不同 X-Machine-ID → Then 各账号独立计数互不干扰（同上）。
- Given 配置=0 → Then 直放零回归（同上 + TestConcurrencyRoundTripperBuckets 关闭态）。
- Given 排队中 ctx 取消 → Then 返回 ctx.Canceled 不悬挂
  （TestConcurrencyLimiterContextCancel / TestConcurrencyRoundTripperContextCancel）。
- Given 闸开启且在途 2 → Then /status rhythm 报 in_flight=2/queue=1，键恒存在（零配置 {}）
  （TestConcurrencyRoundTripperBuckets / TestStatusExposesRhythmKey）。
- Given 5 号 spread=3s → Then 调用按偏移升序、窗口被利用、两次运行相对偏移一致
  （TestKeepaliveSpreadDeterministic）；spread=0 → 立即串行（TestKeepaliveSpreadZero）。
- **牙齿实跑**：还原分桶 bug → TestConcurrencyRoundTripperBuckets FAIL（已跑，红）；
  还原 sleep → TestKeepaliveSpreadDeterministic FAIL（已跑，红）；恢复后全绿。

## 5. 回滚

- F1：`upstream.concurrency_per_account=0` 即关（装配期）；二进制回滚走 deploy-prod.ps1。
- F2：`keepaliveSpread=0`（包级 var 仅测试用；线上关分片需改代码回退或二进制回滚——
  spread 默认 10min 是安全方向，保留为默认）。二进制回滚同 F1。
- 无持久状态迁移、无请求内容变更（只改节奏）。
