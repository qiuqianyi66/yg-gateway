# 2026-10-25 opsagent-panel-view（阶段 2：可观测闭环）

## 背景与必要性

用户指令「定一个目标一次性做好，开子 agent 审查，明天验收」。目标：补齐 opsagent MVP
的最后一腿——产出面板可见、可审计。2026-10-24 实测缺口：agent 登记的 unknown 只在
`/metrics` 计数可见，按账号列表走 cost 专用键（`unknownSeen`）看不到；轮审计只在内存。

## 改了什么

- `internal/pool/unknown.go`：+`AllOpenUnknowns()`（registry 全量过滤 resolved，
  不分来源——cost 与 agent 两类条目同视图可见）。
- `internal/panel/opsagent_view.go`（新）：`GET /panel/api/opsagent/status` 只读端点
  （enabled 快照 + unknowns_open + 最近 10 轮审计；未启用 = 200 + `{"enabled":false}`
  引导态，非错误）。
- `internal/panel/opsagent.js`（新，~110 行）：自包含视图脚本，走 acl_notice.js/
  savings_note.js 的独立嵌入模式——**app.js 零改动**（maxFrontendLines 棘轮 4378 不动）；
  所有动态文本过 esc()（工程结构 §三），无内联事件（CSP script-src 'self'）。
- `internal/panel/index.html`：账号池视图底部「运维 Agent」卡片 + script 标签
  （1831→1846 行，max 不变，oversizeFrontendFiles 恒 2）。
- `internal/panel/panel.go` / `index.go`：Config.OpsAgent 字段（nil=未启用）、路由×2、
  embed+serving（与既有静态资源同口径）。
- `internal/opsagent/opsagent.go`：+`ConfigSnapshot()`（面板展示参数快照，装配期固定）。
- `cmd/server/opsagent_wiring.go`：startOpsAgent 拆成 `buildOpsAgent`（构造，enabled=false
  或 pool 缺失 → nil）+ `startOpsAgent`（nil no-op）；main.go 把 notifier/agent 构造
  提前到 panel.New 之前（面板需拿实例），main.go 999 非空行（<1000 ✓）。
- `scripts/check-boundaries.ps1`：panel 行 += opsagent/unknown（只读展示，理由注释）。
- `docs/prd/REQ-opsagent-2026-10-24.md`：阶段 2 勾销 + 剩余解锁顺序。

## 怎么验证的

五包定向：panel 95.5% / pool 83.7% / opsagent 95.7% / cmd/server 74.0% / server 77.3%，
全绿。新增测试：`TestOpsAgentStatusDisabled`（引导态）、`TestOpsAgentStatusEnabled`
（unknown 过滤 resolved、审计轮含 capacity 建议、CSP 头在 API 也在）、
`TestOpsAgentScriptServing`（JS 资源 serving）、`TestOpsAgentObserversProjection`
（既有，词表归一）、wiring 三态。`node --check opsagent.js` 通过。
**插曲**：10-24 夜后台全量测试被机器休眠挂起 13.8h 触发 go test 墙钟超时
（日志时钟 22:07→11:56 实证）——非代码挂死，重跑即绿。

## 怎么回滚

单提交 revert。未启用部署（config 无 opsagent 键）行为零变化：面板卡片显示引导态。
