# 变更记录 — REA 三问收口（2026-11-04）

> 触发：用户「现在都做了哪些重构？达到了什么样的效果？这个点是否真的有用？还能不能更好？」+「可以去做呗」。
> 三个方向此前已实证为真实缺口（机制就位、闭环未到位），本批逐项落实。REQ：
> `docs/prd/REQ-2026-11-04-rea-closeout.md`（本目录内）。

## 改了什么（三个方向）

### ① 出站 RPM 硬闸开启阈值校准（数据留档，零代码）
- 新增 `.harness/changes/2026-11-04-rpm-calibration/`：`analyze_rpm.py`（可复跑，
  8 天 6191 条 chat 请求 / 10 账号按 60s 窗 + 1s 槽 + 间隔<1s 计算）+ `summary.md`（数据表 + 结论）。
- 结论：真实账号 60s 峰值最高 37 → **建议 `upstream.rpm_per_account = 40`**（零误伤，防御更宽 50）；
  1s 槽位影响可忽略；默认保持 0 关不变；unknown 显式登记（探测类请求未入日志 / `-` 桶 X-Machine-ID 未知）。
- `docs/PLAN-magpie-borrow-2026-11-04.md` RPM 行补「阈值已校准：建议 40」。
- 启用与否由用户 config.json 决定（校准只给值）。

### ② 代理状态面板可见（overview 注入 + 首页渲染）
- `internal/panel/panel.go` overview：`Upstream != nil` 时恒输出 `proxy_status`
  （`ProxyStatusSnapshot()`，与 /status 同源同键；未接线省略键——四态：未观测）。
- `internal/panel/app_views_accounts.js` loadOverview：首页提示行渲染代理状态
  （failed 红字 + 悬停明细、active 计数）；提示行由 textContent 改 innerHTML（esc 转义，无注入面）。
- 测试：`coverage_handlers_test.go` `TestPanelHandlersOverviewProxyStatus`
  （未接线省略 / 接线 {} / failed 透出，经导出入口 ChatStream 触发登记）；
  `frontend_ux_test.go` `TestAppJSProxyStatusObservability`（源码断言 5 项）。
- 未做（留档）：告警推送——failed 持续时长分布无数据，面板先见、推送后定（REQ 不做节）。

### ③ 治理证据链机器化（check-harness 检项 [10]）
- `scripts/check-harness.ps1` 新增检项 [10]：changes/*/summary.md 引用的 REQ/ADR 断链检查。
  三条实测校准判据：
  1. **双基地路径**：仓库根 + 变更目录内（.harness/changes/{feat}/docs/… 是合法落点，
     summary 引用写相对路径，实测 4 处）；
  2. **docs/ 前缀保留原样**（docs/REQ-INDEX.md 在仓库根 docs/ 下，实测 1 处）；
  3. **命令输出形态行豁免**（`[OK]/[FAIL]/$ ` 是验证快照非引用，实测 1 处）。
- 首跑抓到 8 处疑似断链，逐条核实：1 处脚本 bug（ADR 归错目录，已修）、5 处双基地解析假报、
  1 处 docs/ 前缀假报、1 处探针输出假报——**真实断链 0 处**，重跑 100 处引用 0 断链。
- `scripts/pre-commit.ps1` 检查 9 标题「9 检项」→「10 检项」（数字漂移同步）。

## 怎么验证的

- `go test ./internal/panel/ -run 'TestPanelHandlersOverviewProxyStatus|TestAppJSProxyStatusObservability'` → PASS（新增两测试）。
- `powershell -NoProfile -File scripts/check-harness.ps1` → exit 0，检项 1-10 全 OK（[5] 未测由 pre-commit 检查 8 承担）。
- 五门禁 + pre-commit 全量见下方提交记录；面板属 go:embed 资源，生产生效需换版部署。

## 怎么回滚

- 方向 ②：改回 overview 不输出 proxy_status 键 + app_views_accounts.js 提示行回 textContent
  （删两段即可，无状态迁移）；依赖它的前端断言测试同步删。
- 方向 ③：删检项 [10] 段落 + pre-commit 标题回「9 检项」；脚本无外部状态。
- 方向 ①：删除 rpm-calibration 目录 + PLAN 行回退；配置 40 若已启用，改回 0/删键即关。
- 全部无持久状态迁移、无外部副作用，单提交可整体 revert。
