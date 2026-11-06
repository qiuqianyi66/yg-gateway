# PLAN — magpie 节奏面收口：并发闸 + 保活错峰（2026-11-05）

> 方法：doubao-coding-design-solution（Evidence-locked）。每条方案回链当前源码行号。
> 依据：`docs/reference/magpie-rea-full-2026-11-03.md` §2.5 节奏面 + §④ 清单
> （REA 原话"RPM/并发硬闸"是一对）+ 本次对 yg 源码的只读实测。
> 门控：plan-control（门 1 需求分析，批准后进编码）。

---

## 0. 触发与证据基线（本次实测，非记忆）

用户质疑 magpie 吸收深度 → 重读 REA 源 533 行 + 逐点对照 yg 源码，确认 2 处真实未吸收：

| 点 | magpie 做法（REA 行号） | yg 现状（实测） | 差距 |
|---|---|---|---|
| 单账号在途并发闸 | `concurrency.go:15-27` FIFO 车道，5-6 并发即风控 | `internal/upstream` 全仓 grep：MaxConcurrency/semaphore/inflight **零命中**；只有 rpm.go 限速率 | **缺并发闸**（RPM 管"多快"，不管"同时几个"） |
| token 保活错峰 | `keepalive.go:42-56` 8 天周期**每轮只续 1 个号**，反对"一天内 N 个号一起刷" | `scheduler.go:595-620` RunKeepaliveNow **串行全刷所有号**；`auth.go:84` 注释自承"对每个非禁用账号刷新" | **缺错峰**（多号同窗口批量登录动作） |

已核实等价/已覆盖（不重复做）：软死亡→cooldown 到期自恢复；代理分桶→proxyClients 按 URL 分 key
（client.go:179-190）；onePort 反伪造→/v1 与 /panel 同一 key 口径（panel.go:7）；耻辱排序→EWMA+半开
探测更强；在途 reservation→无 key 额度不适用；养号总开关→每类已有 *_enabled 散落开关（R1 无自动发现）。

## 0.5 设计取向：吸收教训，不复制形态（用户指令 2026-11-05）

magpie 的两处教训是**抽象价值**（"并发是独立于速率的风控维度"、"批量登录动作是风控面"），
不是**实现模板**。yg 的形态由自身定位决定（AGENTS.md：CodeBuddy 深耕 + 养号友好 + 可审计 + 单二进制）：

| 维度 | magpie 形态 | yg 自己的形态（本计划） | 为什么 |
|---|---|---|---|
| 并发闸结构 | 独立 concurrency.go 车道 | 独立文件但**复用 yg 既有分桶语义**（X-Machine-ID + 默认 0 关 + 装配期字段），与 rpm.go 同族不同实现 | 不动已部署的 rpm.go（承重墙）；两闸正交 |
| 并发闸可观测 | **per-账号在途是 unknown**（REA §2.7 自承无观测字段） | 加 **/status 只增键**（rhythm：每账号在途/排队深度） | yg 的可审计定位的自然延伸；magpie 明确没有，这是我们的增量 |
| 保活错峰 | 8 天周期每轮 1 个号（`keepalive.go:42-56`） | **确定性分片**：每日窗口内按 UID 哈希散布各号到窗口内不同时刻，日志可审计 | yg 是"每日主动保活"策略，magpie 8 天分片过重；分片不随号数线性拖长、重启可复现 |
| 校准 | 常数（5-6 并发） | 同 RPM 流程：8 天日志 derive 建议值，机制先落地默认关 | yg 已有校准纪律（RPM 40 先例） |

---

## 1. 功能点

### F1 单账号在途并发闸（concurrencyRoundTripper）+ 可观测

**方案**（吸收"并发是独立风控维度"的教训，形态做成 yg 可观测版本）：

- 新文件 `internal/upstream/concurrency.go`：
  - `concurrencyLimiter`：`count`（在途）+ `max` + FIFO 等待队列；`acquire(ctx)` 在途 ≥ max
    阻塞（ctx 取消返回 ctx.Err()），`release()` 在 RoundTrip 返回后归还；`snapshot()`
    返回当前在途/排队深度（供 /status）。
  - `concurrencyRoundTripper`：`RoundTrip` 先 acquire → `next.RoundTrip` → defer release；
    透传 `CloseIdleConnections`（rpm.go:90-97 同款，防清池能力回归）。
  - `Client.concurrencyWait(req)`：key = X-Machine-ID（空 → "global" 桶，与 rpm.go:107-110 同口径）；
    `ConcurrencyPerAccount <= 0` 直放（零回归）。
- `Client` 加字段 `ConcurrencyPerAccount int`（client.go:119-123 RpmPerAccount 同款，装配期零值=关闭）。
- 挂载：与 rpmRoundTripper 同位置（client.go:264 附近 newUpstreamHTTP 外层包一层；rpm 与并发闸正交——
  速率闸管 1s 槽位、并发闸管在途上限，各自独立 limiter、独立锁，**不动已部署的 rpm.go 结构**）。
- **可观测（yg 增量，magpie 明确没有）**：
  - `Client.RhythmStatus() map[string]any`：每账号 `{in_flight, queue_depth}`（读锁取 snapshot，
    未装配/零配置返回空表）。
  - `/status` 只增键 `"rhythm"`（status.go:26-70 同款模式，零回归只增键）：`"rhythm": h.upstream.RhythmStatus()`。
    运维一眼看到"某号当前几个在途、几个排队"——与 proxy_status 同哲学：**可见优先于精确**。
- 配置：`cmd/server/config.go:324` 同款 `ConcurrencyPerAccount int json:"concurrency_per_account"`
  （upstream 段）+ normalize 负值钳 0（config.go:929-930 同款）；`main.go:272` 接线；
  `config.example.json:99` 加行；ADR 一篇（与 ADR-020 同族，说明与 RPM 的分工与正交）。

**Given-When-Then 验收**：
1. Given `concurrency_per_account=2`；When 同账号并发发起 5 个出站请求；Then 同时刻在途数 ≤ 2，
   其余排队，前序完成后按 FIFO 放行（`TestConcurrencyLimiter` + `TestConcurrencyRoundTripper`）。
2. Given 不同账号（不同 X-Machine-ID）；When 各自并发；Then 互不干扰（各账号独立计数）。
3. Given `concurrency_per_account=0`（默认）；When 任意并发；Then 直放零回归（行为与现在逐位一致）。
4. Given 排队中 ctx 取消；When 等待；Then 立即返回 ctx.Err()，不悬挂（同 rpm.go:43 语义）。
5. Given 闸开启且 2 个在途；When GET /status；Then `rhythm` 键含该账号 in_flight=2（只增键，零回归）。
6. **牙齿**：把 acquire 的"在途 ≥ max 阻塞"改成直放（还原 bug）→ 测试 1 FAIL；把 key 分桶改成
   全用 global（还原 bug）→ 测试 2 FAIL；把 RhythmStatus 从 /status 去掉（还原漏挂）→ 测试 5 FAIL。

**待校准**（机制先落地默认关，与 RPM 同流程）：8 天 request-logs 算单账号最大在途并发
（60s 窗内不同 ts 的活跃请求重叠数），derive 建议值，校准结论单独落
`.harness/changes/2026-11-05-concurrency-calibration/`。

### F2 token 保活确定性分片（不照抄 magpie 8 天周期）

**方案**（吸收"批量登录动作是风控面"的教训，形态做成 yg 可审计分片）：

- **分片语义**：keepalive 触发（默认 22:00）起，各号在 `[start, start+spread)` 窗口内按
  `fnv32(uid) % spread` 计算确定性偏移，按偏移升序执行——同一号每天同一相对位置（重启可复现），
  N 号总时长 ≤ spread（不随号数线性拖长，区别于串行 sleep 方案），窗口整体散布（不批量）。
- 实现：
  - `scheduler.Config` 加 `KeepaliveSpread time.Duration`（默认 10min；0 = 保持现状串行全刷，
    测试/用户可关）。
  - `RunKeepaliveNow`（scheduler.go:595-620）改为：收集 (uid, offset) → 按 offset 升序 →
    逐号 `timex.SleepCtx(ctx, 到该号计划时刻的剩余)` 后刷新（runActivity:552-556 同款 ctx 可取消）。
  - 日志可审计：每号 `keepalive %s: slot +%ds（offset=%ds）`——计划时刻对账一眼可见。
  - 迟到补跑/面板手动触发：窗口起点 = 调用时刻，同语义适用（不确定于墙钟小时）。
- 配置：`cmd/server/config.go:228` Schedule 段加 `KeepaliveSpreadSec int json:"keepalive_spread_sec"`
  （默认 600，负值钳 0）；`main.go:290` 接线；`config.example.json` Schedule 段加行。
- 语义说明：保留"每日全刷"（yg 主动保活策略，区别于 magpie 8 天周期——后者适合低频多号，
  yg 常驻保活不需要），只把"瞬间全刷"变成"窗口内确定性散布"。

**Given-When-Then 验收**：
1. Given 池内 5 号、spread=5s；When RunKeepaliveNow；Then 全部刷新调用发生在窗口内且相邻调用
   间隔分布符合 offset 升序（同一 uid 两次运行偏移一致——确定性断言）。
2. Given spread=0；When RunKeepaliveNow；Then 零延迟串行（与现在行为一致，回归测试保持绿）。
3. **牙齿**：把"按 offset 排序"改成"按池序"（还原 bug）→ 测试 1 的确定性断言 FAIL；
   去掉账号间 sleep（还原 bug）→ 间隔断言 FAIL。

---

## 2. 承重墙声明（默认不改，触及必须显式声明）

| 模块 | 为什么是承重墙 | 本次是否动 |
|---|---|---|
| `internal/upstream`（出站唯一路径） | R6 红线：出站请求集中此包，单 sink | F1 新增文件 + Client 字段 + 挂载点，不改既有转发逻辑 |
| `internal/scheduler` 调度主循环 | 四类任务排程、墙钟/睡眠语义（scheduler.go:386-418） | F2 只改 RunKeepaliveNow 函数体，不动 nextWake/Run 结构 |
| `internal/auth` 凭证锁 | 单持有 + 加锁语义（auth.go:78-116） | 不改；RefreshToken 调用方式不变 |
| `cmd/server/config.go` | normalize 默认值纪律 | 只加两个新字段 + 钳 0，不动既有默认 |
| RPM 硬闸 rpm.go | 已部署机制 | F1 复用其分桶语义，**不重构 rpm.go 本身** |

## 3. 红线（零违反）

- R6 出站集中在 internal/upstream：F1 落点天然满足；F2 走既有 RefreshToken 路径。
- R8 报文内容不动：并发闸只卡在途数，不改 header/body；错峰只加 sleep。
- R1/R2：新增日志不含凭证；并发闸按 X-Machine-ID 分桶（已有脱敏先例）。
- 棘轮：check-depth / check-context-budget 只许收紧；新字段默认 0/关不抬预算。
- 测试同提交（L-11）；提交只含本任务文件（L-21）。

## 4. 粒度与顺序

- F1 ≤ 4h（concurrency.go + 字段 + 配置 + 测试）；F2 ≤ 2h（delay 字段 + 函数体 + 测试）。
- 顺序：F1 → F2（互不依赖，可并行分批；但同一次 pre-commit 全量过）。
- 变更记录：`.harness/changes/2026-11-05-magpie-rhythm-closeout/`（REQ 四问 + GWT + summary + 回滚）。

## 5. 回滚

- F1：配置回 0（concurrency_per_account=0）即关；二进制回滚走 R15 deploy-prod.ps1（备份→冒烟→失败回滚）。
- F2：keepalive_spread_sec 回 0 即恢复瞬间全刷（保持现状串行）；二进制回滚同 F1。
- 两者均无持久状态迁移、无外部副作用变更（分片只是节奏变化，请求内容不变）。

## 6. 不做（明确排除，防范围蔓延）

- 不引入 magpie 式"8 天周期每轮 1 个号"分片调度（对 yg 每日保活策略过重；确定性分片已满足
  "不批量动作"目标，且总时长不随号数线性增长）。
- 不做并发阈值校准以外的观测面板改动（在途并发是瞬时量，/status rhythm 键已覆盖可观测性）。
- 不把 F1/F2 与 RPM 校准合并成一个"节奏大包"（各自独立可回滚、可评审）。
- 不给并发闸加告警推送（failed 时长分布仍无观测，与 REQ 留档一致）。
