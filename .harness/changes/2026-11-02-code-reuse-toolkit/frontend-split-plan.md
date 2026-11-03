# 前端组件库拆分计划（`internal/panel/app.js` 4317 行 → 多组件）

- 状态：**第 1-4 步完成**（2026-11-02）；仅剩第 5 步（收紧棘轮，待覆盖率恢复后执行）
- 进度：`app.js` 4317 → **1622** 非空行（**−62%**）；已拆出 10 个组件：`app_core`(129) / `app_qr`(144) / `app_views_{models 97, packages 374, keys 127, usage 493, accounts 374, config 98, tasks 276, logs 687}`
- 目标：把 `app.js` 按**已有模式**拆成组件文件，每块 ≤ 600 非空行，可独立阅读与测试
- 不改的东西：`go:embed` 单二进制形态、严格 CSP、无构建步骤、无框架

## 已完成

### 第 1 步：测试地基（`frontend_bundle_test.go`）

新增三个 helper，把"取前端源码"收成一个入口：

| helper | 作用 |
|---|---|
| `scriptFiles(t)` | 解析 `index.html` 的 `<script src>`，返回**加载顺序**（单一来源，不手抄） |
| `scriptBundle(t)` | 按顺序拼接全部脚本（每段带 `/* ==== 文件名 ==== */` 标记，切片跨文件时能看出断点） |
| `scriptBundlePath(t)` | 写临时文件供 node harness 用（保持"全局作用域共享"的语义） |

替换：`frontend_test.go` 21 处 node 调用 + 4 处 `os.ReadFile("app.js")`；
`frontend_ux_test.go` 的 `readAppJS` 改为读全集；`TestAppJSSyntax` 从"只查 app.js"
改成"逐文件校验"（拆开后报错能指到具体文件）。

**副作用收益**：`TestAppJSTopLevelSmoke` 现在按 index.html 顺序执行**全部**脚本
（此前只跑 app.js，其余 6 个脚本的顶层代码从未被求值）。首次跑就抓到 DOM 桩缺
`MutationObserver` —— 已补 `MutationObserver` / `ResizeObserver` / `IntersectionObserver` 惰性桩。

### 第 2 步：试点拆 `app_qr.js`

搬出 QR 编码器（`QR_EXP/QR_LOG/gmul/QR_V/QR_ALIGN/QR_MASK/qrGenPoly/rsRem/qrDataCodewords/qrMatrix/qrSVG`），
`app.js` 原地留一行指针注释。配套：

- `index.go`：`//go:embed app_qr.js` + `appQRScript` handler（同口径安全头）
- `panel.go`：`GET /panel/app_qr.js` 路由
- `index.html`：紧跟 app.js 的 `<script src="app_qr.js">`
- `security_test.go`：把新路由加进"全响应安全头"清单
- `app_qr_test.go`：三条牙齿
  1. `TestAppQRScriptServing` —— 走**完整路由**（漏注册才抓得到）
  2. `TestAppQRComponentEmbedded` —— 嵌入非空 + 内容含 `qrMatrix` + **app.js 里不许留副本**
  3. `TestAppQRGolden` —— 金样钉住输出：`TEST123`→21×21/236 深色、`A`→21×21/226、
     长 URL→29×29/436；rect 数必须等于深色模块数

## 一、已有模式（照抄，不发明新机制）

```
index.go:20-41   //go:embed index.html / app.js / app_qr.js / acl_notice.js / savings_note.js
                 //         / opsagent.js / trends.js / agent_profile.js / bench.js
index.go:78+     每个文件一个 XxxScript(w,r) handler（Content-Type + setSecurityHeaders + 写回）
panel.go:175+    GET /panel/<file>.js 逐条注册路由
index.html:1897  <script src="...">（CSP script-src 'self' 放行同源外链）
```

所以拆分**不需要**改构建流程——只需加文件、加 embed、加 handler、加路由、加 script 标签（第 2 步已实证）。

面板**已经**是"多文件 + 每文件一个 embed + 一个 handler + 一个 `<script src>`"：

```
index.go:20-41   //go:embed index.html / app.js / acl_notice.js / savings_note.js
                 //         / opsagent.js / trends.js / agent_profile.js / bench.js
index.go:78+     每个文件一个 XxxScript(w,r) handler（Content-Type + setSecurityHeaders + 写回）
index.html:1897  7 个 <script src="...">（CSP script-src 'self' 放行同源外链）
```

所以拆分**不需要**改构建流程——只需加文件、加 embed、加 handler、加 script 标签。
`panel.go` 的 `routes()` 里注册 handler 路由（新 handler 要在这里挂一行）。

## 二、实测约束（这是拆分真正的成本，别低估）

| 约束 | 实测 | 影响 |
|---|---|---|
| 测试按文件名硬读 | `frontend_test.go` 里 **29 处** `"app.js"` | 拆完要改成"按 index.html 顺序拼接全部脚本" |
| 测试按源码切片取函数 | **≥6 处** `src.indexOf('function xxx')` … `src.indexOf('function yyy')`（如 `trimFixed`/`usStat`、`dur(`/`rateLimitRowsHtml`、`esc(`/`ago(`、`fmtTok(`/`usStat(`、`requestLogText`/`fmtBytes`） | 函数一旦换文件，`indexOf` 返回 -1 → 切片变垃圾 → **静默错测**。必须改成按文件查 |
| 顶层求值冒烟 | `TestAppJSTopLevelSmoke` 用 node 执行**单文件**（7 个 hash 路由各一次） | 拆分后必须依次执行全部脚本，否则漏检 TDZ/ReferenceError |
| 全局作用域 | 所有脚本共享全局（无模块系统），`const`/`let` 在顶层不挂 window | 跨文件引用只在**调用时**解析 → 加载顺序只需保证"调用晚于定义"；但**顶层立即执行**的代码要小心顺序 |
| 深度棘轮 | `maxFrontendLines` 基线 4317（只许降） | 拆分天然把它降下来，是正向收益 |

## 三、拆分方案（按域分组，附行数量级）

| 组件文件 | 内容 | 依据 |
|---|---|---|
| `app_core.js` | `esc` / `ago` / `dur` / `fmtTok` / `formatTokenCount` / `formatLatency` / `parseAPITime` / `toast` / `api` / `abortable` / `isAbort` / 主题 | 被最多组件复用，必须最先加载 |
| `app_qr.js` | `rsRem` / `qrDataCodewords` / `qrMatrix` / `qrSVG` / `copyText`（app.js:2763-2882） | **纯函数、零 DOM、零状态** → 最安全的试点 |
| `app_export.js` | `csvCell` / `exportCSV` / `exportJSON` / `downloadBlob` / `exportStamp` | 纯函数 + 一个下载动作 |
| `app_views_models.js` / `_accounts.js` / `_usage.js` / `_config.js` / `_logs.js` / `_tasks.js` / `_packages.js` | 各视图渲染与事件绑定 | 与 `index.html` 的视图分区一一对应 |

## 四、执行顺序（每步都要绿，不许一步到底）

| # | 步骤 | 状态 |
|---|---|---|
| 1 | 改测试地基：`scriptFiles/scriptBundle/scriptBundlePath` 三个 helper；替换 26 处 `"app.js"` 硬读 + 逐文件语法校验 | ✅ 完成（2026-11-02） |
| 2 | 试点拆 `app_qr.js`（纯函数）：embed + handler + route + script 标签 + 三条牙齿测试 | ✅ 完成（2026-11-02） |
| 3 | 拆 `app_core.js`（18 个纯函数：esc/ago/dur/时间解析/token 与延迟格式化/CSV 转义）——**必须排在 app.js 之前加载** | ✅ 完成（2026-11-02） |
| 4 | 按视图拆 7 个组件（models/accounts/usage/config/logs/tasks/packages） | ✅ 完成（7/7，另加 keys 视图共 8 个视图组件） |
| 5 | 收尾：`check-depth.ps1 -Update` 收紧 `maxFrontendLines`（当前 **1622**，基线 4317） | ⬜ **待执行**：`-Update` 会连带把 `minCoverage 73.3→72.9` 写进基线（放宽），须先补 `cmd/server` 覆盖率再收紧 |

第 3 步的关键约束（第 2 步没暴露、第 3 步会）：`app_qr.js` 的函数**只在事件处理器里调用**，
所以加载顺序随意；而 `app_core.js` 的工具函数很可能被 app.js 的**顶层**代码用到
（`start()` / `go()` 在顶层执行），因此必须让它先于 app.js 加载——届时 `index.html` 顺序
要改成 `app_core.js` → `app.js` → `app_qr.js` → 其余。

## 五、风险与红线

- 🔴 **不许改 CSP**：`script-src 'self'` 必须保持；拆出来的仍是同源外链，不能引 `unsafe-inline`。
- 🔴 **不许引构建步骤**（webpack/vite/babel）：单二进制零依赖是产品定位。
- 🟡 全局命名冲突：拆开后同一全局名可能两处定义（`const` 顶层重复声明会直接 SyntaxError）——
  拆分时每块搬完立刻跑冒烟，别攒到最后。
- 🟡 `index.html` 里若有内联 `onclick=`，拆分不影响（CSP 允许内联事件处理器？**不允许**——
  现有页面已是事件委托，`TestIndexHTMLNoInlineScript` 守着）。
