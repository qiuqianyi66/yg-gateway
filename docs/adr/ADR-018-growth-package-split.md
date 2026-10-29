# ADR-018：upstream 拆出 growth 养号活动域（接口形态 + 拆包范围）

> 状态：**草案待拍板**（M4）。决策点：拆包范围（A' 六文件 vs 全拆 vs 不拆）。
> 前置：M3 已补齐 growth 域行为级测试基线（2026-10-29 提交 5e98c9b）。
> 依据 REQ-growth-package-split-2026-10-24（立项）+ 2026-10-29 实读证据（observation）。

## 背景

`internal/upstream` 74 文件 / 23.7k 行，是全仓最大包；「OpenAI↔CodeBuddy 协议转换」与
「养号活动自动化」两类正交职责同居。REQ 解锁动作①（测试基线）已完成，本 ADR 定②接口
形态与拆包范围。

## 实测证据（2026-10-29 全仓实读，非转述）

1. **活动域公开函数与外部引用面**：scheduler 18 处、panel 25 处（均经
   `cfg.Upstream.Xxx(a)` 组合对象调用，共 43 处）；server/pool 不直接用。
2. **耦合分级**（按依赖的核心能力）：
   - 低耦合（只依赖 `doJSON` + `auth.Auth`）：school、travel、tasks、streak、trial。
   - 高耦合：blackcat 依赖 `ChatStream`（client.go:516）+ `ReportChatActivityModel`
     （report.go:151）+ `ListTasks`；taskmetrics 的 `observeUsage` 被 client.go:608
     在 ChatStream 流路径内调用（交织最深）。
3. **billing/promo 不是养号活动**（REQ 粗分入活动域，实读修正）：
   - promo.go 的 `applyModelPromotions` 只被 models_fetch.go:557（目录构建）调用，
     拆出会引入 `models_fetch → growth` 反向依赖（目录挂载不属于养号）。
   - billing.go 是积分/用量查询（UserResource 族被 scheduler 签到决策、panel 展示、
     login/import 导入刷新用），无活动动作。
4. **R8 红线**：RunNightChats 报文 `wb2api-night-*` conversationId 由 growth 侧拼装，
   金样测试（r8_golden_test.go）经 Client 调用——拆后须保持调用形状零变化。

## 候选对比

- **候选 A（REQ 原案，全拆 9 文件含 billing/promo）**：promo 拆出后 models_fetch 需
  依赖 growth 拿挂载逻辑 → 边界倒挂；billing 与协议转换（计费查询）本是一家。
  **否决**。
- **候选 B（仅拆 taskmetrics）**：实测 taskmetrics 与核心流路径交织最深（observeUsage
  在 client.go 流路径内调用），拆它需动 ChatStream 的观察器接线，成本不小而收益最小。
  **否决**。
- **候选 A'（推荐）：拆 6 个养号活动文件**（school/travel/tasks/streak/trial/blackcat
  → `internal/growth`）；taskmetrics/billing/promo 留 upstream（交织深 / 非活动域）。
  - 接线：`growthTransport` 窄接口（4 方法，只依赖 auth 与基础类型）定义在 growth 包，
    `*upstream.Client` 实现之并内嵌 `growth.Client` 委托字段；upstream 保留 6 组方法
    转发存根 → **scheduler/panel 43 处调用点零改动**。
  - 边界：`growth → auth`、`upstream → growth`（transport 实现 + 委托字段）、
    `scheduler/panel → upstream`（不变，不新增 growth 引用面）。
  - R8 金样：测试留在 upstream 包，经 Client 转发，报文形状不变，零 diff 断言保留。
  - 预估改动：growth ~2k 行搬移（纯搬家，逻辑逐字不动）+ upstream 转发存根 ~50 行 +
    边界表 +2 方向 + 测试搬包（既有 6 文件测试随迁）。

## 决策

- **推荐 A'**：正交职责真正分开（养号活动 vs 协议转换/记账），边界检查粒度从整包
  降为单域，符合「养号逻辑改动不再进 23.7k 行定位」的 REQ 初衷。
- 候选 A' 未决细节（执行时定）：growth 包是否允许 import upstream 类型（ChatMeta 等）
  还是接口全用基础类型——倾向后者（接口瘦、无反向类型依赖），blackcat 的
  `SetNightChatSleep` 注入经 transport 注册。
- **不拆（维持现状）**：本次不采纳——REQ 立项理由（定位成本 + 边界粒度）仍成立。

## 怎么回滚

拆包是纯搬移（逻辑零变化）+ 转发存根：回滚 = `git revert` 拆包提交（growth 目录删除、
转发存根还原为方法体，R8 金样与测试回原包）。R8 金样 + mutation 套件保证搬移零语义漂移。
