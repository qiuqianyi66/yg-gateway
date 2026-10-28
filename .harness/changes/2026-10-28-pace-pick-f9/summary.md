# 2026-10-28 pace-pick-f9 (F9)

## 背景与必要性

F9（PLAN-frontend-functional-2026-10-27）：现状「快过期优先」是常量虚拟实例
`expiringVirtualSlots=×2`——只看「有没有快过期批次」，不看剩余量与距到期时长；
「100 额度 1 小时后到期」和「1 额度 100 小时后到期」拿到同一加成，前者流失风险
高 4 个数量级（magpie 源码级研读 §五① 论证的失败模式）。REQ：
`docs/prd/REQ-pace-pick-2026-10-28.md`。

## 改了什么

- **`internal/pool/pace.go`（新）**：pace 语义（= 最早到期批次剩余额度 ÷ 距到期
  小时数，<1h 钳 1h 防除零）+ `SetPaceMode`（默认关）+ `poolMedianPace`（expiring
  候选下中位，<2 样本返回 0）+ `paceFactorOf`（1 + clamp(ratio−0.5, 0, 1) ∈ [1,2]；
  无基准回退原常量）。
- **`internal/pool/pick.go`**：`pick()` 在 pace 开启时算池级 medianPace（与
  medianLatencyMs 同法，显式传参无共享状态）；`routingWeightOf`/`pickWeighted`
  签名加 medianPace；expiring 分支：pace 开启 → `paceFactorOf`，关闭 → 原
  `expiringVirtualSlots`（零回归）。
- **`internal/pool/pool.go`**：`paceEnabled` 字段。
- **`cmd/server/config.go`**：`Pool.PaceMode bool (json:"pace_mode")`（默认 false）。
- **`cmd/server/main.go`**：启动 + 热生效两条路径接 `SetPaceMode`（同
  recent_usage_factor 接线）。
- **`internal/pool/pace_test.go`（新）**：4 测试——因子边界（中位×1.5/半位×1/
  封顶×2/<1h 钳位/无基准回退）、开/关权重与封顶、单 expiring 回退、2000 次长期
  分布（高 pace 占多数、普通号不饿死）。既有 expiring ×2 常量测试在传 medianPace=0
  下继续守护默认关语义；12 处 routingWeightOf/pickWeighted 调用点补 `, 0`。
- **文档**：README「多账号池」行补 pace 说明并顺手修正历史漂移（×3 → ×2，实测
  expiringVirtualSlots=2）；PLAN F9 行 ✅；REQ-INDEX + 行。

## 怎么验证的

- `go test ./internal/pool ./cmd/...` 全绿（4 个新测试 + 全部既有测试，签名改动
  无回归）；gofmt 零漂移；go vet 干净。
- 零回归论证：pace 默认关 → routingWeightOf 分支恒走 `expiringVirtualSlots`，
  与改动前逐位一致；medianPace=0（稀疏场景）也回退常量；既有测试传 0 显式守护。

## 怎么回滚

单提交 `git revert`（纯代码 + 文档，config 默认关无生产行为影响）。若生产已开启
`pace_mode`，回滚后重载配置即可关回常量语义。
