# REQ: F6 key RPM 实况可视化（2026-10-28）

> 状态：**落地（2026-10-28）**。GWT 全部通过（server 计数/快照单测 + panel overview
> 透出 + app.js 槽位渲染，前端特征测试守护）。

## 必要性

- **真实问题**：`api_keys[].rpm` 已可配限额（P0-3 限流器按 key 独立 1 分钟窗口），但
  面板只显示「配置了多高限额」（RPM 限制 badge），看不到**实际请求速率**——某把被
  分享出去的 key 是否逼近/打穿限额、哪把 key 在高峰时段打满，运维完全无感。
- **不做会怎样**：限流只有「打到 429 才知道」，没有趋势可见性；多 key 场景无法回答
  「哪把 key 在打爆池子」。
- **现状是否变了**：从未支持（keyRPMGuard 只做限流判定，不记录/不暴露实际计数；
  且只对有 rpm 配置的 key 计数，被 429 的请求也不计——统计口径缺失）。
- **更便宜的等效手段**：无——reqlog 聚合太重且非实时，进程内滚动桶是唯一合适载体。

## 方案骨架（复用既有旋钮模式：默认行为零回归）

- **server**：`keyRPMGuard` 重构为**单计数器**——新增 `record(key)` 记录每次实际
  请求（含将被 429 的、无 rpm 配置的 key），`allow` 只判不限（withAuth 先 record
  再 allow，旧限流语义逐位保留：每窗口恰好放行 RPM 次）；每 key 维护**闭合分钟历史
  桶**（容量 12，环形截断）+ 当前窗口计数 + 进程内累计，`Snapshot()` 输出
  `[{key_id, limit, rpm, total, history[]}]`（按 key_id 排序，无观测返回空数组）。
- **panel**：`Config` 增 `KeyRPMStats func() any` 回调（main 后接线，handler 创建于
  panel 之后，用闭包变量晚绑定）；`overview` 输出 `key_rpm` 字段（回调未接线时省略
  = 未观测；接线后恒输出，空数组 = 观测到 0 请求，非 unknown）。
- **前端**：key 卡片 RPM 限额 badge 旁加**实况槽**（`RPM 当前/限额` + 12 点纯 CSS
  迷你柱状曲线，`data-rpm-live`）；`loadOverview` 每 5s 轮询就地刷新槽位（不整表
  重渲，不重绑事件）；stats 早于卡片渲染时槽位缺失 → 随 config 重载补渲。
- **无新依赖**：滚动桶复用 ratelimit 自写风格；曲线纯 CSS（app.css 加 2 条规则）。

## Given-When-Then 验收（执行时）

- When withAuth 收到 key 请求（有/无 rpm 配置、被 429 与否），Then record 计数
  全部入账（统计口径 = 真实请求速率，非「被放行数」）。
- When 某 key 一窗口内第 RPM+1 次请求，Then 与旧语义一致：前 RPM 次放行、之后 429
  （record+allow 拆分后限流行为逐位不变；既有 keyrpm 测试更新为 record+allow 流程
  后全绿）。
- When 分钟翻转，Then 当前计数压入历史桶、桶数封顶 12（环形截断，防无界增长）；
  Snapshot 返回的 history 尾元素 = 当前窗口计数（曲线连续）。
- When panel.Config.KeyRPMStats 未接线，Then overview 无 key_rpm 键（四态：未观测）；
  接线后输出数组（空数组 = 观测到零请求）。
- When app.js 收到 overview，Then key 卡片出现 RPM 实况槽（数值 + 曲线），5s 轮询
  就地更新不整表重渲（frontend_ux_test 守护特征串）。
- Then 全量 go test 绿、gofmt 干净、pre-commit 11/11；README 多 API Key 段落补行。

## 解锁动作

① 实现（本 REQ）→ ② 生产面板 key 页观察真实 RPM 分布，评估是否需要默认开启/告警。
