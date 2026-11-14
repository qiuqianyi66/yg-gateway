# ADR-027：退役未接线的抽象层（provider.Registry / quota 包 / router 策略接口）

- 状态：已接受（2026-11-14）
- 前置：ADR-026（provider 抽象分层）

## 背景

ADR-026 提出把「上游是什么形状 / 额度语义 / 选号策略」拆成 provider → quota → router 三层，
pool 保留 credits 状态机，两者并存，按 P1 / P2 分期接线。

当时的写法是**先建层、后接线**：三层都写了类型、接口、实现与测试，并标注「P0 阶段无人
接线」。P1 只接了 `provider.Kind` / `provider.QuotaKind` 两个常量用于 config 校验。

## 观察（2026-11-14 全仓核实，非推断）

`go run golang.org/x/tools/cmd/deadcode@latest ./...` 输出中，以下符号报告 unreachable：

| 包 | 符号 | 交叉验证结果 |
|---|---|---|
| `internal/quota` | 整包（`Metered`/`Window` 的 `Kind`/`Remaining`/`Exhausted`） | `grep 'yg-gateway/internal/quota"'` 只命中它自己的测试文件 |
| `internal/provider` | `NewRegistry` / `Register` / `Get` / `List` / `Len` / `cloneDescriptor` / `Descriptor.Validate` / `Kind.valid` / `QuotaKind.valid` | 唯一调用方是 `provider_test.go`；`cmd/server/provider_wiring.go` 用普通 map 建 `modelProvider`/`providerEndpoints` |
| `internal/router` | `Strategy` / `Mode` / `Rotate` / `Usage` | `internal/pool/pick.go` 只用 `router.View` 与 `router.Order{}.Pick` |

`deadcode` 有大量误报（接口方法、测试专用 helper），因此每条都做了 grep 交叉验证；只有
上表三项与 `pool.floorBlockedForModel`（仅一条测试调用、生产用 `floorBlockedForRealmModel`）
经核实为真死。

`router` 还有更硬的一层证据：`pickNonCredits` 构造的 `router.View` 每一项都写死
`Healthy: true`，而 `Order.Pick` 的语义是「返回第一个 Healthy 的下标」——必返回 0。
即该抽象在本项目里是**恒等变换**，还多一次 `[]View` 的 append 分配。

## 决策

删除以上未接线部分，只保留真正被生产消费的枚举与函数：

- `internal/provider` 留 `Kind` / `KindCodeBuddy` / `KindOpenAICompat` / `KindArk` / `QuotaKind` / `QuotaCredits` / `QuotaMetered` / `QuotaWindow`（config 校验在用）。
- `internal/quota`、`internal/router` 整包删除；`pickNonCredits` 直接 `cands[0]`，语义与旧行为逐位一致（map 迭代随机首个）。
- `pool.floorBlockedForModel` 删除，测试改调 `floorBlockedForRealmModel(..., "", ...)`。
- `scripts/check-boundaries.ps1` 的 allow 表同步移除 `quota` / `router` 条目。

## 后果

- 生产代码 −366 行、测试 −384 行，文件 −4 个；包数 33 → 31。
- 二期若要 provider 注册表 / 非 credits 轮转策略，**必须同时接线**：先建层后留白的做法在
  本仓库被证明会沉淀成「看起来能用、实际空转」的代码，而读者无法从文件本身判断它是否
  生效——`ADR-026` 的 P0 注释是唯一线索，且它随着时间已经不是最新事实。
- 边界检查的 allow 表变短，`-Fix` 重新生成的结果与手改一致。

## 被否决的替代方案

1. **保留并标注「预留」**——否决理由：代码里已有「P0 阶段无人接线」的注释，仍然需要一次
   deadcode + grep 才能确认；注释不是证据，删除 + 本 ADR 才是。
2. **保留 router 抽象但接上 InFlight/LastUsed 做真实的最少使用策略**——否决理由：当前
   `pickNonCredits` 服务的是非 credits 的 metered/window 账号，生产规模下「取首个合格号」
   已满足需求；在没有实际并发压力的证据前引入轮转/最少使用是无依据的功能扩张。
3. **只删 router、保留 quota**——否决理由：quota 连一个 import 都没有，删除零风险；
   留着唯一效果是把「额度语义未来会搬家」的意图留在仓库里，而这已由 AGENTS.md 的
   Product Direction 承载。
