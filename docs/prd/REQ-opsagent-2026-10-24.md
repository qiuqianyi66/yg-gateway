# REQ: 网关内置运维 agent（opsagent，2026-10-24）

> 状态：**立项待执行（等条件）**。本文只立项，不含代码改动。
> 路线依据：`docs/roadmap/HARNESS-GAP-AND-AGENT-EVOLUTION-2026-10-24.md` §三 L2。

## 必要性

- **真实问题**（2026-10-24 实测）：网关 7×24 常驻，异常信号已经齐备——alerting webhook
  （余额告急/会话失效/被封）、reqlog 归档、metrics 分位、pool 冷却状态——但**从信号到动作
  靠人**：看到告警 → 人来判断（是账号坏了还是模型被限？）→ 人来行动（换号/调权重/等签到）。
  夜间与无人时段的异常只能等下次有人看面板。
- **不做会怎样**：静默浪费——402 冷却的号占着池位等次日 04:00，期间流量压到其余号；
  unknown 台账里的疑点（模型成本未知、支持能力未知）无人推进消解。
- **现状是否变了**：unknown registry（带 revision）与 evidence 四态已落地（ADR-007 系），
  scheduler 五类任务已稳定运行——**受约束 agent 的骨架已存在**，缺的是把「观察→假设→
  行动→审计」串起来的编排层。此前没做是因为骨架未齐，现在齐了。
- **更便宜的等效手段**：告警文案里附「建议动作」（纯文案，无编排）——可作为第一步降级，
  但无法闭环「行动后验证」，收益止步于少翻一次面板。

## 形态（MVP：观察-建议，不做自动行动）

`internal/opsagent`（暂名）周期性（复用 scheduler 心跳节拍）执行一轮：

1. **观察**：聚合 metrics/reqlog/pool 状态的既成事实（observation），不引入新抓取。
2. **假设**：对可解释的异常产出 hypothesis，登记为 unknown（requires 字段照旧强制，
   revision 语义照旧）——例如「uid X 在模型 M 上连续 402，怀疑 quota 结构变化，
   requires: 一次该模型带 credit 的实测」。
3. **建议**：把建议动作（含依据的四态标注）经 alerting webhook / 面板 notice 输出，
   **不直接执行**。
4. **审计**：每轮产出一条 evidence 记录（本轮看了什么、推了什么、置信多少）。

MVP 的「行动」只有两类白名单：①触发一次成本探索（pick 已有的 explore 机制，
零新增上游请求）；②面板 notice。其余一切动作（改配置、换号、冷却策略）保持人工。

## Given-When-Then 验收（MVP）

- When 一轮 opsagent 完成，Then evidence 落一条记录，含 observation/inference 标注，
  无未标注的推断（四态纪律）。
- When 产生假设，Then unknown registry 新条目带 requires + revision，且 Resolve 走
  既有校验（过期 revision 必报错）。
- When 白名单外的动作被触发，Then 拒绝并审计（护栏 teeth 测试：注入一个非白名单
  动作意图，断言拒绝路径）。
- When 关闭开关（config 默认 **off**），Then 行为零变化（零回归）。

## 解锁动作

① ADR：编排层位置（scheduler 内 vs 独立包）与节拍选择 → ② MVP 实现（默认 off）
→ ③ 真机观察一轮 → ④ 再评估是否开放第二类白名单动作。

## 2026-10-24 MVP 落地后的实测发现（后续项登记）

- **面板可见性缺口**：opsagent 登记的 unknown 条目只在 `/metrics` 的
  `unknowns_open` 计数里可见（`pool.OpenUnknownCount` = registry 全量）；
  面板按账号的 Unknowns 列表走 `unknownSeen`（`uid|model` cost 专用键，
  `internal/pool/unknown.go:173`），**不含** opsagent 条目。完整可视化需动
  `internal/panel/app.js`——该文件冻结在 `maxFrontendLines` 棘轮上限（4378 行），
  加视图前必须先在别处瘦身。解锁顺序：app.js 减 ≥100 行（如抽公共渲染函数）→
  面板加「全部未消解 unknown」视图（读 registry 全量，非按账号过滤）。
