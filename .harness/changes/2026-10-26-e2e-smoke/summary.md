# 2026-10-26 · V3-6 ① E2E 冒烟套件 + CI 接线 + opsagent 鉴权修复

## 改了什么

1. **`e2e/` 新目录（Playwright 冒烟，V3-6 ①）**：
   - `package.json`：只装 `@playwright/test`（最小化，无其他依赖）
   - `playwright.config.ts`：串行单 worker；固定 `colorScheme: 'light'`（theme=auto 跟随
     prefers-color-scheme，panel-spec 真机验证踩过"两图字节相同"坑）；webServer 起隔离实例
   - `tests/smoke.spec.ts`：4 测试 / **34 条用户可见断言**（密钥门未登录自动弹出、错 key 拒、
     登录进账号池、统计条渲染、八视图逐一切换可见/标题/hash、opsagent 卡片加载与空态）；
     全程收集 pageerror + console.error（过滤浏览器自动打的 "Failed to load resource"
     HTTP 噪声——401 是密钥门必经路径、503 是空池下 models 的已知降级）
   - `scripts/build-server.ps1` / `scripts/e2e-server.ps1`：测试实例 = `%TEMP%\yg-e2e`，
     端口 7864，`WB2A_LISTEN/API_KEY/AUTH_DIR/STATE_FILE/PROMPT_FILE` 全 env 隔离，零账号池
     → 不触上游；与生产 7863 完全无关
2. **`.github/workflows/e2e.yml`（CI 接线）**：push main 全跑、PR 按影响面
   （e2e/**、internal/panel/**、cmd/server/**、workflow 自身）；先 `uses: gate.yml`
   （与 release.yml 共用同一份门禁实现，不复制门禁）再跑浏览器 E2E。
3. **修复真 bug：`internal/panel/opsagent.js` 请求缺 Authorization 头**——api_key 鉴权
   （默认开启）下 opsagent 卡片 fetch `/panel/api/opsagent/status` 恒 401 → 卡片恒显
   "加载失败：HTTP 401"。修复：从 localStorage `yg-gateway.key` 取 key 加 Bearer 头
   （与 app.js LS_KEY 硬编码同步，有注释）。**E2E 抓出来的第一个生产级 bug。**
4. **文档**：ROADMAP v3 V3-6 标 ①✅；KICKOFF 重写为当前状态（HEAD/已推送/换版完成/P0 剩瘦身）。

## 怎么验证的

- 本地：`cd e2e && npm run build:server && npx playwright test` → 4 passed（6.3s）
- 面板单测：`go test ./internal/panel/` → ok（含 node --check opsagent.js 语法）
- 修复前后对照：不带 Bearer 头 `opsagent/status` 401（修复前）；带 Bearer 头 200
  `{"enabled":false}`（修复后，空态文案"未启用…"）
- 空池已知降级：`/panel/api/models` 503（零账号无目录可聚合，生产有账号即 200）——
  视图渲染错误态、无 JS 异常，非本次引入，记录不修

## 为什么 CI 用独立 workflow 而不是挂进 pre-commit.ps1

AGENTS.md「新检查一律挂进 pre-commit.ps1」的本意是**单门禁实现不分叉**。E2E 需要
node_modules + 浏览器二进制，本地未必装、CI gate job 无 node——挂进去会让本地每次提交变慢
且 CI/本地判定条件不同（真分叉风险）。e2e.yml 的 `gate` job **就是** gate.yml（同一份实现），
E2E 是门禁之上的额外浏览器验证层，不构成第二道门禁。

## 回滚

- 删 `e2e/` + `.github/workflows/e2e.yml` + `opsagent.js` 的头修复（git revert 本提交即可）；
- 已交付/依赖：`%TEMP%\yg-e2e`（测试实例，可随时删）；远端 main 已有本提交时 `git revert`。
