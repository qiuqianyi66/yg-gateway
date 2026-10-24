# REQ: upstream 拆出 growth 域子包（立项，2026-10-24）

> 状态：**立项待执行**（等条件）。本文只立项与记录耦合事实，不含代码改动。

## 必要性

- **真实问题**（2026-10-24 体检实测）：`internal/upstream` 74 文件 / 23.7k 行，是全仓最大
  包；其中「OpenAI↔CodeBuddy 协议转换」与「养号活动自动化」（school/travel/blackcat/streak/
  tasks/taskmetrics/billing/trial/promo，约 9-10 文件 / ~2k 行）两类**正交职责**同居一个
  单层扁平包。改养号逻辑要在 23.7k 行里定位；边界检查只能整包粒度。
- **不做会怎样**：静默——包继续膨胀、新文件继续平铺，无报错。属于「能看懂但渐进危害」。
- **现状是否变了**：10-22 R-201 已把 client.go 机械拆出 billing 等文件（同包内拆文件，
  未拆包）；包级拆分从未做。
- **更便宜的等效手段**：文件级拆分已在做（棘轮在压 maxFileLines）。包级拆分的增量收益
  是「边界检查粒度 + 定位成本」，值得做但非紧急——结论：**等条件立项，不立即执行**。

## 耦合事实（2026-10-24 实测，为什么不是机械搬移）

1. growth 域函数是 `*Client` 的方法：`ListTasks` / `ClaimReward` / `schoolJSON` /
   `RunNightChat` / `ClaimGift` 等，方法不能跨包搬离类型。
2. 共用未导出 helper：`growthJSONMP` / `schoolJSON` / `observeUsage`（taskmetrics 与
   SSE 流路径交织：`usageObservingReader` 包裹 `ChatStream` 返回体）。
3. `blackcat.go` 调核心 `ChatStream` + `ReportChatActivityModel`；`SetNightChatSleep`
   是 client 上的回调注册。
4. R8 红线：报文内容字符串一个字节不能动；R6：出站必须仍集中 `internal/upstream`。

## 方案草案（执行前需 ADR 拍板）

- 候选 A：`internal/growth` 持有活动域，构造时注入窄接口（`type growthTransport interface`
  封 doJSON/growthJSON 能力），`*Client` 委托字段。边界新增 `growth → upstream`（理由：
  复用出站口，R6 不破）与 `scheduler/panel → growth`。
- 候选 B：仅拆 taskmetrics（与流路径交织最深、也最独立）+ 活动 API 文件保持，成本最小。
- R8 金样测试（`r8_golden_test.go`）与 mutation 套件作为搬移安全网。

## Given-When-Then 验收（执行时）

- When `internal/growth` 落地，Then `go build ./...` 与 23 包测试全绿，R8 金样零 diff。
- When 边界脚本 `-Fix` 重生成，Then allow 表新增方向逐条有注释理由。
- When 养号触发（签到/夜猫子/连登），Then 行为与拆包前一致（scheduler 调用点零语义变化）。

## 解锁动作（条件成熟后）

① growth 域行为级测试基线补齐（当前部分靠 coverage_* 间接覆盖）→ ② ADR-017 定接口
形态（候选 A/B）→ ③ 执行 + 边界白名单更新 + summary。
