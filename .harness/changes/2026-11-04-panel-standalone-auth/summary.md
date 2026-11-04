# 变更记录 — 面板独立文件族鉴权补漏（2026-11-04）

> 触发：用户截图面板用量页「读取趋势失败（未授权）」「读取流量画像失败（未授权）」。
> REQ：`docs/prd/REQ-2026-11-04-panel-standalone-auth.md`（本目录内）。

## 改了什么

- `internal/panel/trends.js` / `agent_profile.js` / `bench.js`：自包含 api() 的 fetch
  注入 `Authorization: Bearer <localStorage yg-gateway.key>`（与 opsagent.js /
  savings_note.js 正确先例同款；无密钥时不注入空头）。
- `internal/panel/frontend_ux_test.go`：新增 `TestAppJSStandaloneFetchBearer` 机制化检查
  ——逐文件扫描 *.js，凡含 `fetch('/panel/api` 必须含 Authorization 注入，否则 FAIL。
  未来新独立文件漏带即红（P1-3 机制化当时缺的正是这道检查）。

## 为什么之前没拦住

P1-3（b544037）「api() 强制鉴权机制化」审计的是 app.js 的 api() 调用点；
独立嵌入文件族（trends/agent_profile/bench/opsagent/savings_note）各自带独立 fetch，
不在 app.js 源码里，审计没覆盖。opsagent/savings_note 因各自实现时带了 Bearer
（正确先例），trends/agent_profile/bench 三处漏网。生产开鉴权后立即 401 显形。

## 怎么验证的

- `go test ./internal/panel/ -run 'TestAppJSStandaloneFetchBearer'` → PASS
  （牙齿验证：三处修复前该测试对三个文件报「未注入 Authorization」）。
- 全量 pre-commit 11/11（含 go test ./...、check-harness 检项 1-10、
  上下文预算 14842 压线不超）。
- 生产部署后：用量页三模块 HTTP 200 实测（见部署冒烟与面板复核）。

## 怎么回滚

- 删除三处 Bearer 注入 + 删除机制化测试（或整体 revert 本提交）。
- 无状态迁移、无外部副作用；独立文件不依赖 app.js，删除后不影响主路径。
