# REQ: F9 pace 选号模式（2026-10-28）

> 状态：**落地（2026-10-28）**。GWT 全部通过（单测：因子单调/封顶 + 池中位 + 分布 +
> 默认关零回归）；config 开关默认关。参考：magpie 源码级研读（`docs/reference/
> magpie-deep-read-2026-10-25.md` §五①，pace = 周剩余份额 ÷ 距 reset 小时数）。

## 必要性

- **真实问题**：现状「快过期优先」是常量虚拟实例 `expiringVirtualSlots=×3`——只看
  「有没有快过期批次」，不看**剩余量**和**距到期还有多久**。两个极端都被扁平化：
  「100 额度 1 小时后到期」和「1 额度 100 小时后到期」拿到同一加成，前者流失风险
  高 4 个数量级（magpie §五① 论证的 least-used / smart 各自的失败模式）。
- **不做会怎样**：快过期批次在到期前用不完就白丢（积分浪费），与「账号活得久、
  积分不浪费」的产品支柱直接冲突。
- **现状是否变了**：从未支持（routingWeightOf 无 pace 维度）。
- **更便宜的等效手段**：无——这是路由权重的平滑化改造，唯一实现路径。

## 方案骨架（复用既有旋钮模式：默认关 = 零回归）

```
pace = creditsEarliestRemaining ÷ max(1, 距 creditsEarliestExpiry 小时数)   // 每小时流失量
池级基准 medianPace = expiring 候选的 pace 下中位（同 poolMedianLatencyMs 口径，
<2 样本返回 0 → 因子回退 expiringVirtualSlots，稀疏场景与原行为逐位一致）
paceFactor = 1 + clamp(pace/medianPace − 0.5, 0, 1)   // 中位=×1.5；半位以下=×1 无加成；
                                                        // 1.5× 以上=×2 封顶（与原 expiringVirtualSlots=2 上限对齐）
paceEnabled 时 routingWeightOf 用 paceFactor 替代 expiringVirtualSlots 常量
```

- **不动选号结构**：仍是 top5 截断 + pickWeighted 加权抽签；pace 只是乘性因子
  （与 EWMA / softband / recentUsage 同模式）。medianPace 与 medianLatencyMs 同法：
  pick() 计算一次，线程式传入（签名显式传参，无隐式共享状态）。
- **config**：`pool.pace_mode`（bool，默认 false）→ `SetPaceMode`，启动 + 热生效
  两条路径都接（同 recent_usage_factor 接线）。
- **公式常量**（pick.go 或 pickreason.go 顶部）：`paceBoostFloor=0.5`（低于半中位
  不加成）、`paceMaxBoost=2.0`（封顶，与原 ×3 对齐）。

## Given-When-Then 验收（执行时）

- When `pace_mode=false`（默认），Then routingWeightOf 与改动前逐位一致
  （expiring 账号仍 ×expiringVirtualSlots；既有 expiring 分布/权重测试不改语义全绿）。
- When `pace_mode=true` 且两个 expiring 候选 pace 不同（高：100 额度/1h；低：
  100 额度/100h），Then 高 pace 号权重大于低 pace 号（因子单调），且 ≤×3 封顶。
- When 只有一个 expiring 候选（medianPace=0），Then 回退 ×expiringVirtualSlots
  （稀疏场景不改变行为）。
- When 高于中位 1.5× 以上，Then 因子封顶 ×2（与原 expiringVirtualSlots=2 上限对齐，
  不放大极端值）。
- Then 全量 go test 绿、gofmt 干净、pre-commit 11/11；README 能力表「快过期优先」
  行补 pace 模式说明。

## 解锁动作

① 实现（本 REQ）→ ② 生产上 config 开启观察（人工决策，验证真实分布后再考虑默认开）。
