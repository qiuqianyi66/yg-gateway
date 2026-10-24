# 算法深读评审（2026-10-24）

> 范围：选号两阶段（pick.go）、EWMA 回灌（entry.go）、冷却/熔断状态机（cooldown.go）、
> 延迟分位估算（metrics/histogram.go）。方法：逐文件精读 + 手推权重/退避公式 + 机器验证。
> 产出修复 2 处（同提交带回归测试），记录不改 3 处。

## 修了什么

### F-1 pickWeighted 抽签基准与截断排序不一致（已修）

`pick()` 头部契约：「maxCredits 统一用全集口径……截断排序与抽签权重共享同一基准」。
但 `pickWeighted` 内部用 **top5 子集重算 maxCredits**——当 top5 因快过期虚拟实例（×3）
/闲置补偿混入低分号、而全集最高分号未进 top5 时，credits 比例的分母变小，抽签分布
偏离排序意图（`+1` 基线使两个基准不可交换：6:2 ≠ 11:3）。

**修法**：`pickWeighted(cands, maxCredits, medianLatencyMs)` 改为接收调用方传入的全集
基准。回归测试 `TestPickWeightedSharesFullSetMaxCredits`：top5={500,100}、全集 max=1000
→ 注入随机源看到的 total 必须 = 8×10⁶（旧实现 14×10⁶；带默认闲置补偿时 18×10⁶ vs 24×10⁶）。

### F-2 熔断截止的时钟口径分裂（已修）

`recordBreakerFailureLocked` 用 `time.Now()` 写 `breakerUntil`，而 half-open 门槛
（`breakerHalfOpen`）用 `p.now()`（可注入时钟）比较——两侧口径不同，注入时钟下熔断
窗口错位，测试也不可冻结。改为 `p.now().Add(d)`。
回归测试 `TestBreakerUntilUsesInjectableClock`：SetClock 冻结后触发熔断，
breakerUntil 必须 = 冻结时刻 + cooldown。

## 记录不改的

| # | 位置 | 观察 | 为什么不改 |
|---|---|---|---|
| N-1 | pick.go 等权重洗牌 | 只检查 `ws[i].w == ws[0].w`，不与 ws[0] 相等的截断边界并列检测不到 | Go map 迭代本身随机化，字典序饿死在实践中已被打断；彻底修法是「先排序再对并列尾段洗牌」，属结构改动，收益边际 |
| N-2 | histogram +Inf 桶 | 上界取最后边界 ×2（120s→240s）插值 | 注释已自辩（分位数要量级不要极值）；SNAPSHOT 的 MaxMs 同口径 |
| N-3 | EWMA 乐观初值 | 首事件懒初始化 sr=1.0 | ADR-007 明确取舍（样本不足保留数值+unknown），有测试锚定 |

## 实证为健康的（对照清单）

- **两阶段防惊群设计**：top5 截断 + 层内加权随机 + 等权重洗牌 + usedSeq 单调序号 LRU
  兜底（规避 Windows ~0.5ms 时钟精度导致 lastUsed 全等）——惊群三道防线完整。
- **成本分层探索**（issue #136 a′）：tier 0 垄断 + tier 1 冻结存在时**搭车改道**探索，
  零新增上游请求、IP 零增量（WAF 友好），exploreMu 保证窗口判定+置位原子。
- **积分保底两级判费**：本地实测台账 → 上游目录倍率兜底（kimi-k3-1 实案注释：无观测
  恒放行会被高价新模型打穿），「未知放行」是有意的保守选择且写明理由。
- **熔断状态机**：阈值触发、指数退避**有封顶**（breakerCooldownMax）、half-open 按
  breakerProbeGap 限速放行、NoteSuccess 清 retryCount——与 AGENTS 红线口径一致。
- **软冷却封顶**：`cappedSoftUntilLocked` 把上游 reset 墙钟截断到 now+softRateMax。
- **EWMA 因子**：双向钳制 [0.85, 1.15]+延迟 [0.7,1]（饿死防护）、冷启动中性（样本
  < minSamples 因子恒 1）、池级基准用**下中位**（保守罚慢号，避免单超时抬基准）。
- **读锁 pick + 原子写回**（ADR-005 D-500）：热路径无写锁，语义放宽（并发同选）有
  Acquire 租约 + tried 轮换自愈闭环。
- **分位直方图**：桶内线性插值而非取上界（系统性高估被显式规避）、编译期桶数自检、
  byModel 防伪模型名无界增长（maxModels 上限丢弃）。

## 验证

`go test ./internal/pool/ -count=1` 全绿（含 2 个新回归测试）；gofmt 干净。
