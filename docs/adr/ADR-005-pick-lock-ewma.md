---
last_updated: 2026-10-12
status: 已实施（用户选定「后端选号优化」方向后交付）
owner: YG
adr_number: 005
prd: docs/prd/REQ-yg-gateway-deep-optimization-2026-10-12.md
---

# ADR-005 选号去写锁 + EWMA 表现回灌

> 来源：深度优化轮第二轮候选（后端选号优化，用户 2026-10-12 选定并交付）。
> 前序：ADR-003 D-402 明确把「pick 改读锁 + lastUsed/usedSeq 原子化」列为需新 ADR 的
> 承重墙项；ADR-004 已处理粘性派生键。本文记录 D-600（去写锁）与 D-601（EWMA 回灌）
> 的决策、并发契约、兼容边界与回滚。

---

## 一、背景

选号是每请求必经路径，而 `pick()` 长期持**全池写锁**（`p.mu.Lock()`）；粘性命中
（`PickByUID` / `PickByUIDForModel`）同样走写锁。D-402 只做了时间节流（prune 由
每轮 85% 降到极少触发），**写锁串行点仍在**：并发请求的选号在锁上排队，账号/流量
放大后是全局吞吐瓶颈。

第二个问题来自**选号决策只用瞬时信号**：熔断（连续 3 次 5xx）、冷却（429/402/WAF）、
连败降权（连续 5 次未知失败）都是**连续性硬门槛**。一个「每 3 笔失败 1 笔」（间歇
30% 失败）的账号永远不会触发任何门槛，却以全权持续被选中——用户侧表现为间歇性报错，
且旧的成功率因子（successCount/(successCount+errTotal)）因 errTotal **终身累计、
只增不减**被删：早期出过一次错的号永久压权、永不恢复。

---

## 二、D-600 选号去写锁（pick 改 RLock + 原子写回 + prune 移出热路径）

**问题**：写锁是选号热路径的唯一串行点（粘性路径同病）。

**决策**：

1. `pick()` 改持 `p.mu.RLock()`，并发 pick 可并行；粘性命中路径同步改 RLock。
2. **选中写回全部原子化**（读锁下禁止写普通字段，否则数据竞争）：
   - `entry.lastUsed` → `atomic.Int64`（unixnano，0=从未使用）
   - `entry.usedSeq` / `pool.pickSeq` → `atomic.Uint64`（`Add(1)` 后 `Store`）
   - `entry.breakerProbeAt` → `atomic.Int64`（0=无试探）
3. **探索台账独立小锁**：`exploreLast`（map）+ `costExploreEvents` 由新 `exploreMu`
   保护——pick 改读锁后 map 不能在读锁下写，且「窗口判定 + 置位」必须原子（否则并发
   pick 重复探索）。`CostExploreStatus` 同步改走 exploreMu。
4. **过期模型级台账清理（prune）移出热路径**：新增 `pruneAllLocked`，在 `saveLocked`
   顶部与 flusher tick（静默期兜底）调用；原 pick 内的 prune 删除。读路径本就按 TTL
   跳过过期项，晚删一轮零正确性影响；时间节流 `pruneInterval`（10s/entry）不变。

**为什么不用 ADR-003 延期备注里的「版本化快照 + 池版本号失效重建」**：
RWMutex 的 RLock 本身就为读锁持有者提供一致快照（读锁期间无写者，所有普通字段读稳定），
手动维护「候选快照 + 版本号 + 失效重建」等于维护**双份账本**，只在连 RLock 都嫌重的
场景才有意义——本网关选号 ~2.4µs，RLock 并行后已足够。真正需要处理的只有 5 类写回点
（lastUsed/usedSeq/pickSeq/breakerProbeAt/探索台账），原子化即可，无需失效机制。

**并发契约（语义放宽的边界）**：写锁串行时代「同一瞬间第 2..N 个 pick 看到 lastUsed
已更新，被挤向其他账号」。改读锁后**同一瞬间的并发 pick 可能同选一号**。自愈链：

- handler 选号后必须 `Acquire`（per-account + global 在途租约）成功才发上游；失败 →
  加入 `tried` → 重新 pick（handler.go 轮换路径）；
- per-account 在途上限天然限流，Acquire 拒绝的请求**不产生上游调用**；
- 防撞号窗口对「下一个进入者」继续生效（原子写回立即可见）。

结论：并发同选不构成正确性问题（仅多几次 pick 迭代），**不引入 CAS-claim**——收益
仅限同 tick 的并发请求，而 Acquire 已兜底，复杂度不值。

**锁序**：`p.mu`（RLock）→ `exploreMu`（leaf，不反向拿 p.mu）；`pruneAllLocked` 只在
持写锁路径调用。无新增嵌套锁。

**兼容**：零配置、零持久化字段变更；读锁单次一致性 = 写锁语义。既有全部选号/熔断/
冷却/粘性测试（含 TestPickAntiThunderingHerd、TestBreakerHalfOpen*）不变通过。

**回滚**：pick.go/state.go 的锁与原子写回为互斥替换，单文件 revert 即回；无状态迁移。

---

## 三、D-601 EWMA 成功率/延迟信号回灌选号权重

**问题**：见「背景」。瞬时硬门槛管不住间歇性软失败；终身累计成功率因子不可用。

**决策**：每账号维护两个 **EWMA** 信号（运行态，不持久化，重启回中性）：

- **成功率 sr**：`NoteSuccess` → 向 1 收敛；`NoteError`（权威 5xx 系）与 `NoteFailures`
  （未知失败）→ 向 0 收敛。**懒初始化 1.0**（乐观：首个事件前视同从未失败——否则健康
  新号从头几十个样本都被低于基线而误罚）。
- **延迟 lat**：`RecordTokenUsage`（每个实际发起尝试，含失败路径）→ 毫秒 EWMA，首个
  观测直接赋值（不从 0 慢爬）。
- **权重因子 `ewmaWeightFactor`**（在 `routingWeightOf` 内，钳制后不颠覆
  credits/闲置/快过期的既有主导）：
  - 成功率：sr > 基线 0.95 → 线性加分到 **+15%**（sr=1 封顶）；sr < 基线 → 线性减分到
    **-15%**（sr=0 封底）。
  - 延迟：仅惩罚显著慢于**池延迟中位数**的账号（> 1.5× 中位），线性升到 **-30%** 封顶；
    低于阈值不罚、快号**不额外加分**（避免正反馈振荡）。
  - 冷启动中性：任一样本数 < `min_samples`（默认 5）→ 对应因子恒 1（不惩罚新号/没流量号）。
  - 综合因子 ∈ [0.595, 1.15]：**不可能清零账号权重**（饿死防护）。
- 默认关闭（`pool.ewma.enabled=false`，零回归）；**热生效**（`SetEwma`，改配置无需重启）。

**为什么与硬门槛并存而不是合并**：熔断/冷却管「已经坏」的号（连续性证据），EWMA 管
「间歇性差」的号（比例证据），正交；EWMA 惩罚上限远低于硬门槛的出池，不会在硬门槛前
抢先改判。429/402/WAF 类冷却失败**不喂** sr（它们已有独立惩罚且占满冷却期，再喂是
重复计罚，与连败降权喂入口径对齐）。

**喂入口径的已知放宽**：延迟含客户端引起的快速失败（内容拦截/坏参数等）——它们也走
`RecordTokenUsage`。影响仅可能拉低账号延迟 EWMA（让账号看起来更快），无惩罚方向风险；
这类失败不调 NoteSuccess/NoteError → 不影响 sr。

**配置**（`config.json`，字段热生效）：
```json
"pool": {
  "ewma": { "enabled": true, "alpha": 0.1, "min_samples": 5 }
}
```
- `alpha` ∈ (0,1]，0/缺失回落默认 0.1，>1 报错；
- `min_samples` ≤ 0 回落默认 5；
- 默认 `enabled=false`（键缺席即现状行为）。

**可观测性**：`/status` 每账号新增 `ewma_success` / `ewma_latency_ms` / `ewma_samples`，
仅 ewma 开启且样本 > 0 时透出（面板 JSON 纯增量，零破坏）。

**兼容/回滚**：默认关闭 = 现状逐位一致；开启后随时可关（热生效）。信号不持久化 →
回滚无状态残留。

---

## 四、测试

- **D-600**：既有全部选号/熔断/冷却/粘性测试不变通过；`TestModelCooldownsLazyCleanup`
  契约更新（清理点 pick → saveLocked，同时断言「pick 不再删 map」守住读锁契约）；
  新增 `TestPickConcurrentRLockSmoke`（200 并发选号冒烟）。
- **D-601**：`TestEwmaDisabledRegression`（关闭=旧权重逐位一致）、`NeutralColdStart`
  （样本不足因子恒 1）、`SuccessFactorBounds`（±15% 钳制 + 单调）、`LatencyFactor`
  （仅罚 >1.5× 中位、-30% 封顶）、`FeedsFromNotes`（喂入契约）、`PickShiftsWeight`
  （5 成 5 败交替 → 确定性 r=0 选好号、r=末尾仍可选坏号=不饿死）、`MedianLatency`
  （下中位口径）、`LatencyPenalizesSlowInPick`、`StatusExposure`；config 侧：
  默认值、文件解析、归一（alpha>1 报错）。
- **数据竞争**：本机无 gcc，`go test -race` 不可用（同 ADR-003 备注）；由 CI 的
  Linux race job 兜底。D-600 的结构性防线 = 读锁路径禁止普通字段写入（已 grep 复核：
  pick 内仅原子写回 + exploreMu）。

---

## 五、被弃方案

- **版本化快照 + 池版本号失效重建**（ADR-003 延期备注原方案）：双份账本；RLock 已给
  一致快照，收益为零（见 D-600）。
- **CAS-claim 保留防撞号语义**：Acquire + tried 已兜底并发同选，复杂度不值（见 D-600）。
- **延迟因子对快号加分**：正反馈会让快号越用越多、慢号饿死，违背打散意图；纯惩罚更稳。
- **成功率用池相对基线**：全池性劣化（上游整体故障）时全员同比例受影响，相对排序不变，
  固定基线 0.95 足够且可预测；池相对基线随噪声漂移，不值得。

---

## 六、结论

两项均不改变正常路径语义（D-600 零配置、D-601 默认关）。D-600 消除选号热路径唯一串行
点；D-601 开启后让「间歇性差」的账号温和让位（+15%/-30% 钳制内），并由 /status 透出
信号供运维观测。已实施、全量测试通过、go vet/gofmt 干净；部署到 D: 实例（enabled=true、
保守默认参数）并真机验证。
