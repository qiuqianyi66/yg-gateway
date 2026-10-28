# 2026-10-28 key-rpm-visualization-f6 (F6)

## 背景与必要性

F6（PLAN-frontend-functional-2026-10-27 A3）：`api_keys[].rpm` 可配限额，但面板
只显示「配置了多高限额」，看不到**实际请求速率**——某把被分享出去的 key 是否逼近/
打穿限额完全无感。且旧 `keyRPMGuard` 只对有 rpm 配置的 key 在 allow 内计数（统计口径
≠真实请求速率，被 429 的也不计）。REQ：`docs/prd/REQ-key-rpm-visualization-2026-10-28.md`。

## 改了什么

- **`internal/server/keyrpm.go`**：重构为**单计数器**——`record(key)` 记录每次实际请求
  （含将被 429 的、无 rpm 配置的 key），`allow` 只判不限（先 record 后 allow，限流语义
  与旧实现逐位一致：每窗口恰好放行 RPM 次）；每 key 维护 12 分钟闭合历史桶（分钟翻转
  环形截断）+ 当前窗口 + 进程累计 + 惰性 GC（13 分钟无活动回收）；`Snapshot()` 按
  key_id 排序输出 `{key_id, limit, rpm, total, history[]}`（无观测返回空切片）。
- **`internal/server/handler.go`**：withAuth 内 `record(key)` 先于 `allow`（计数含被 429
  的请求）；新增 `KeyRPMStats()` 导出方法。
- **`internal/panel/panel.go`**：`Config` 增 `KeyRPMStats func() any` 回调（回调而非类型
  依赖，守住 panel 不得反向依赖 server 的红线）；overview 输出 `key_rpm`——未接线时
  键省略（四态：未观测），接线后恒输出（空数组 = 观测到 0 请求，非 unknown）。
- **`cmd/server/main.go`**：panel 创建先于 handler → 用闭包变量晚绑定（`keyRPMStatsFn`
  占位，handler 创建后 `keyRPMStatsFn = func() any { return h.KeyRPMStats() }`）。
- **`internal/panel/app.js`**：Key 卡片 RPM 限额 badge 旁加**实况槽**（`RPM 当前/限额` +
  12 点纯 CSS 迷你柱状曲线，高度相对 max(限额, 历史峰值) 归一）；`loadOverview` 每 5s
  轮询经 `refreshRpmLive` 就地刷新槽位（不整表重渲、不重绑事件）；`rpmStatsById` 未观测
  时保持 null（与「观测到 0」分离）。
- **`internal/panel/index.html`**：key-badge 段后加 `.rpm-live` / `.rpm-bars` 样式。
- **测试**：`keyrpm_test.go` 更新为 record+allow 流程 + 新增 4 个（含 429 计数/无限额
  计数、分钟翻转、桶封顶、排序与空快照）；`coverage_handlers_test.go` 新增 overview
  key_rpm 三态（未接线省略/接线透出/零观测空数组）；`frontend_ux_test.go` 新增
  `TestAppJSKeyRPMVisualization` 特征串守护。
- **文档**：README「多 API Key 派发」表补实况 RPM 行；PLAN F6 行 ✅；REQ-INDEX + 行。

## 怎么验证的

- `go test ./internal/server -run TestKeyRPM ./internal/panel -run "TestPanelHandlersOverview|TestAppJS"` 全绿；
  全量 `go test ./...` + go vet + gofmt 零漂移（见 pre-commit 11/11）。
- 零回归论证：allow 语义逐位保留（RPM=2 恰放行 2 次第 3 次拒，测试显式守护）；panel
  未接线路径（测试/旧部署）overview 无新键，wire 形状不变。

## 怎么回滚

单提交 `git revert`（纯代码 + 文档，无配置项、无生产行为影响）。若生产在跑旧面板，
回滚后 overview 自然省略 key_rpm（未接线 = 未观测），前端无槽位，无兼容问题。
