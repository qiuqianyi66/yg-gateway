---
last_updated: 2026-11-21
status: 已实施
owner: YG
modification_level: L2（选号成本判定 / 积分保底）
complexity: B 模块级
---

# 成本 tier 置信度：单条 credit=0 不得直接判免费

## 一、必要性

### 要解决的真实问题

`internal/pool/pick.go` 的 `costTier` 此前只要看到一条 `CostPer1k<=0` 的观测，
就把该 (账号, 模型) 判为 tier 0（实测免费），选号时给予最高优先级；积分保底
`floorBlockedForRealmModel` 同样直接返回“不拦”。

而 `credit` 来自上游 usage 上报：字段缺失时会被读成 0。代码注释记录的实际案例
（global 侧 `deepseek-v4.1-flash`）就是：上游已改价收费，网关因一条 0 观测判免费，
把流量持续压到该模型上，440 积分半天耗完。

这不是展示层的四态标注问题，而是**选号决策把推断当成观察**：
一条样本被当成“观察到的事实”，导致付费流量被免费层垄断，且不报错。

### 不做会怎样

- 后果可见性：低（日志没有“误判免费”告警，面板显示 tier 0 也合理）。
- 严重度：高（积分被快速消耗，触发账号硬冷却，免费模型也一起不可用）。
- 触发面：任何上游 `credit` 缺失/延迟上报的模型；global 侧已发生过。

### 现状是否已变

没有变：`modelCostEntry.Samples` 已持久化，但 tier 判定完全没用样本数。
目录倍率兜底只覆盖“无观测”和“目录收费”两种情形，覆盖不了“有一条 0 观测 +
目录未下发/目录也是 0”的模糊区。

### 有没有更便宜的等效手段

- 只改面板标注：不够，决策仍在误判。
- 每次收费都向上游查 usage：成本高、有延迟，且不能解决 credit 缺失。
- 用目录倍率完全取代实测：目录未覆盖的模型会被永久锁死，违背养号目标。
- 最小改动：保留现有 EMA 台账，只给“判免费”加样本门槛与第二证据源。选择此方案。

结论：**做**（降级为判定规则收紧，不改数据结构、不改 wire 形状）。

## 二、范围

### 做什么

1. 单条 `CostPer1k<=0` → tier 1（unknown），不再直接 tier 0。
2. 累计 `minFreeCostSamples=3` 条零扣费观测 → tier 0（免费）。
3. 上游目录显式倍率 `0` 作为第二证据源，可让单条零观测提前判免费。
4. 目录显式收费与单条零观测矛盾 → tier 2（保守按收费）。
5. 积分保底同口径：单条零观测不再覆盖目录收费；累计满 3 条后才按实测免费放行。
6. 目录查询收敛为 `catalogRate`，区分“未知”与“显式 0”，不得把缺失当免费。

### 不做什么

1. 不改 `modelCostEntry` / `state.json` 结构，`Samples` 已有。
2. 不改上游协议报文与 `/status` wire 形状。
3. 不把“证据不足”判成收费：tier 1 仍按未知放行，避免新模型/重启用号
   永久学不回来（保留 `TestCreditFloorAllowsUnknownBelowFloor` 契约）。
4. 不做按模型/按域的可配置阈值：当前只有一个全局常量，配置项属于过度设计。

## 三、判定规则

| 证据 | tier | 积分保底 |
|---|---|---|
| 有正单价观测 | 2 收费 | 拦 |
| 目录收费 + 实测 0（任一条数） | 2 收费（矛盾保守） | 单条 0 拦；满 3 条放行（实测优先） |
| 目录显式 0 + 实测 0 | 0 免费 | 放行 |
| 仅实测 0，样本 < 3 | 1 未知 | 放行（未知不惩罚） |
| 仅实测 0，样本 ≥ 3 | 0 免费 | 放行 |
| 无观测 / 观测过期 | 1 未知 | 按目录倍率兜底 |

四态语义：tier 0/2 是 `inference`（凭样本外推），tier 1 是 `unknown`。
“没看到扣费”不等于“免费”，证据不足必须留在 unknown。

## 四、验收

- `TestModelCostTierRequiresRepeatedFreeEvidence`：单条 0 → tier 1；3 条 → tier 0；
  目录显式 0 提前毕业；目录收费 + 单条 0 → tier 2。
- `TestCreditFloorWeakFreeEvidenceWithPaidCatalogBlocks`：触底号 + 单条 0 +
  目录收费 → 不放行；补满 3 条后放行。
- `TestCreditFloorAllowsFreeBelowFloor` / `TestCreditFloorLocalLedgerFreeBeatsCatalogPaid`
  / `TestStickyPaidHitsCounter`：改用 3 条零观测表达“强免费证据”。
- `go test ./...`、`go vet ./...`、`staticcheck ./internal/...` 全绿。

## 五、回滚

单文件回滚 `internal/pool/pick.go` 的 `modelCostTier` / `floorBlockedForRealmModel` /
`catalogRate` 与测试即可；无数据迁移，`state.json` 兼容。回滚后恢复“单条 0 判免费”
的旧行为，风险同必要性段。
