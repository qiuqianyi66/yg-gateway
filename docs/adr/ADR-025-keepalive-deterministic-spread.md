# ADR-025 — 保活按账号确定性分片（不是固定 delay 串行、也不抄 8 天周期）

- 状态：已采纳（2026-11-05）
- 关联：`docs/plans/2026-11-05-magpie-rhythm-closeout.md`、REQ `docs/prd/REQ-2026-11-06-magpie-rhythm-closeout.md`

## 背景

`RunKeepaliveNow` 自引入起就是"串行立即全刷所有号"：22 点某一秒内 10 个账号同时发起
token 刷新（登录动作），密集登录动作是风控观测面（magpie REA §2.6）。

magpie 的解法是**8 天周期 + 每轮只续 1 个号**——对 yg 的"每日全刷"策略过重（会拖长
token 过期窗口）。yg 需要的不是"少刷"，而是**同样的刷新量，在时间轴上散开、且重启后
位置可复现**。

## 决定

1. **确定性分片（deterministic spread）**：`keepaliveOffset(uid) = fnv32a(uid) % spread 秒`，
   按偏移升序、睡到各自计划时刻。单号每天在同一相对位置（重启可复现，可审计），
   总时长 ≤ spread（不随号数线性拖长）。
2. **spread 默认 10 分钟、包级 var**（`keepaliveSpread`，与 `activityAccountDelay`
   同口径；不做 config 字段——单机运维不需要调这个，避免配置噪音。计划里的
   `keepalive_spread_sec` 配置化在此降级为包级 var，偏差已声明）。
3. **保留"每日全刷"策略**，不引入 magpie 式 8 天周期。
4. **ctx 感知**：`runKeepalive(ctx)`，取消即放弃剩余槽位（由 scheduler 的 RunBatch
   取消流程接管）；`RunKeepaliveNow` 保留为 background ctx 包装（手动触发兼容）。
5. 每号打 slot 日志（`slot +%s（offset=%s）`），可审计"这轮是否真的散开了"。

## 取舍

- 备选 A：固定间隔 delay 串行（每号刷完 sleep 30s）——被拒（总时长随号数线性增长，
  10 号 × 30s = 5min 且在窗口内位置随运行时长漂移，重启后不可复现）。
- 备选 B：随机 jitter——被拒（不可复现、不可审计，测试无法钉确定性）。
- 备选 C：magpie 8 天周期——被拒（与 yg 每日保活策略冲突，token 过期窗口过长）。
- 备选 D：配置化 spread——本次不做（见决定 2），若未来运维需要再升级为 config 字段。

## 后果

- 22 点窗口内刷新动作从"同刻批量"变为"10 分钟窗口均匀散布"。
- 测试可钉确定性（TestKeepaliveSpreadDeterministic：偏移升序 + 窗口利用 + 两次运行一致）；
  关闭态 spread=0 与旧行为逐字一致（TestKeepaliveSpreadZero）。
