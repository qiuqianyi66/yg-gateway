# ADR-024 — 单账号在途并发闸作为独立风控维度（与 RPM 正交）

- 状态：已采纳（2026-11-05）
- 关联：ADR-020（RPM 硬闸）、ADR-021（proxy_status 观测）、
  `docs/plans/2026-11-05-magpie-rhythm-closeout.md`、REQ `docs/prd/REQ-2026-11-06-magpie-rhythm-closeout.md`

## 背景

RPM 硬闸（ADR-020）只约束"每账号每分钟请求数"——管**速率**。它不拦"同一时刻有 5 个
在途请求"：速率可以合规而在途打满。magpie 的 REA 把"RPM / 并发"作为**一对**风控维度，
且其源实现明确记录"5-6 并发即风控"。yg 全仓实测无任何在途计数（grep
MaxConcurrency/semaphore/inflight 零命中）。

## 决定

1. **新增独立 `concurrencyLimiter`，不与 rpmLimiter 合并**。两者语义正交（速率 vs 在途），
   合并会把"等待下一个令牌"和"等待在途槽位"混成一个队列，既无法单独观测也无法单独关闭。
   挂载顺序：`concurrencyRoundTripper` 外层 → `rpmRoundTripper` 内层（先拿在途槽位再拿
   速率令牌，避免"拿到令牌却在途排队"占用令牌窗口）。
2. **复用 rpm 的分桶口径**（X-Machine-ID，空→global），不引入新的账号标识。
3. **默认 0 关闭**（装配期 fatal 若负值→钳 0），与 rpm_per_account 同流程：机制先落地，
   阈值由生产日志 derive 后再建议开启，不直接抄 magpie 的 5-6。
4. **/status 只增键 `rhythm`**（每账号 {in_flight, queue_depth}）——这是 yg 的增量，
   magpie 无 per-账号在途观测（其 REA §2.7 自承 unknown）。可见优先于精确：
   暴露事实是**观察**（实时计数），不是推断。
5. 关闭态（0）下 `concurrencyRoundTripper` 纯透传、零开销。

## 取舍

- 备选 A：在 rpmLimiter 内部加并发计数——被拒（两个维度无法独立配置/观测，见决定 1）。
- 备选 B：只做全局并发闸（不分账号）——被拒（风控面是按账号的，全局闸会互相拖累）。
- 备选 C：直接抄 magpie 的 5-6 默认值——被拒（无生产日志支撑的阈值是"魔法数"，同 RPM
  校准流程：先默认关、日志 derive 后建议）。

## 后果

- `/status` 新增 `rhythm` 键（零回归：零配置返回 {}，旧消费方忽略未知键）。
- **阈值已校准（2026-11-06）**：8 天生产日志（5998 条有账号归属请求）实测到达时在途
  p99=2 / 峰值 4；候选 N=4 误伤率 0.000%、N=3 为 0.850%。**建议
  `concurrency_per_account: 4`**（默认仍 0 关，由用户决定启用）。结论与脚本见
  `.harness/changes/2026-11-06-concurrency-calibration/`。
- **桶名可读化（2026-11-08）**：`rhythm` 的桶 key 是 `X-Machine-ID`（36 hex），运维看不出
  账号。改为在 **server 输出层**映射成 `昵称(uid8)`（无昵称 → uid8；`global`/未知桶原样保留），
  上游分桶语义不动——upstream 是协议层，不该知道账号昵称。派生复用同一
  `deriveAccountStableID`（导出 `MachineIDForUID`），测试钉死「桶名派生 = 出站头」。
  见 `.harness/changes/2026-11-08-rhythm-readable-keys/`。
