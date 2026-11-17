# app.js 再拆两块：通知中心 + 命令面板（1481 → 1369 非空行）

日期：2026-11-17
类型：降体量（结构拆分，产品行为零改动）
前置：`.harness/changes/2026-11-17-playground-split/`（同一天的第一刀）

## 一、做了什么

从 `internal/panel/app.js` 再剪出两块独立组件。

| 文件 | 变化 |
|---|---|
| `app_notifs.js` | **新增** 68 行（13 行注释头 + 55 行通知中心块 + 补回的收尾 `}`） |
| `app_cmdp.js` | **新增** 70 行（13 行注释头 + 57 行命令面板块） |
| `app.js` | **1481 → 1369 非空行**（两轮拆分合计 1640 → 1369，减 271 行 / 16.5%） |
| `index.go` | 两个 embed 变量 + `panelScripts` 两条 + 两个 handler |
| `routes.go` | 两条静态路由 |
| `security_test.go` | 安全头测试路径表两条 |
| `.harness/depth-baseline.json` | `maxFrontendLines` 1481 → **1369** |

## 二、为什么这两块可以排在 app.js 之后

沿用 `app_qr.js` / `app_playground.js` 的既有模式，但**增加了两个必要条件**：

1. **块内所有对后续符号的引用都在函数体内**。逐个核过：
   - `app_notifs.js`：`pushNotif` 被 `toast()`（app.js 函数体内）调用；`initNotifCenter` 只绑事件处理器
   - `app_cmdp.js`：`CMD_ITEMS` 的 18 个 `action` 全是箭头函数，`go`/`openAdd`/`openPlayground`/`$` 都在调用时才求值
2. **顶层初始化调用必须延迟**。这是本轮唯一的实质改动：

```js
// 原（会 ReferenceError：本文件求值时后续脚本还没加载）
initNotifCenter();
initCmdPalette();

// 改
setTimeout(() => { initNotifCenter(); initCmdPalette(); }, 0);
```

`<script src>` 无 `async`/`defer`，同步按序执行；`setTimeout(0)` 落在 macrotask 队列，
届时全部同级脚本已求值完毕。这是 `btnPlayground` 那个坑的通用解法——
**凡是「app.js 顶层调用了排在它之后的组件函数」，都必须走这条路径**。

## 三、过程中的一次返工（诚实记录）

首次剪除用了 Python 切片，通知中心的边界算错一行（`lines[126:181]` 少取了收尾 `}`），
结果：`app.js` 残留一个孤立 `}`，`app_notifs.js` 缺收尾括号。

**抓到它的不是浏览器，是 `TestAppJSSyntax`**（`go test ./internal/panel/` 里的 Node 语法校验）：

```
--- FAIL: TestAppJSSyntax
    frontend_test.go:94: app.js syntax error
--- FAIL: TestAppJSTopLevelSmoke
    frontend_test.go:190: app.js 顶层求值 #taskscenter 崩溃: exit status 1
        SMOKE FAIL: app.js:3288  Unexpected token '}'
```

修法：删掉 `app.js` 里孤立的 `}`，给 `app_notifs.js` 补回收尾 `}`。
**这条值得记住**：前端切片改动的第一道防线是 `TestAppJSSyntax`，不是浏览器实测。

## 四、验证证据

| 项 | 命令 / 动作 | 结果 |
|---|---|---|
| 单元测试 | `go test ./internal/panel/` | `ok 55.552s` |
| 语法 + 顶层冒烟 | 同上（`TestAppJSSyntax` / `TestAppJSTopLevelSmoke`） | 全绿 |
| 编译 | `go build -trimpath -o %TEMP%\yg-play.exe ./cmd/server` | exit 0 |
| 页面含新标签 | `curl http://127.0.0.1:17863/panel/` | `app_playground.js` / `app_notifs.js` / `app_cmdp.js` 三条都在 |
| **浏览器实测 · 通知** | bu 平面点 `#btnNotif` | `#notifPanel` hidden=false，列表已渲染 |
| **浏览器实测 · 命令面板** | bu 平面 `Ctrl+K` | `#cmdOverlay` hidden=false，18 项，首项「跳转账号池」 |
| **浏览器实测 · console** | 全流程 | **零 JS 错误** |
| 体量 | `scripts/check-depth.ps1 -Update` | `maxFrontendLines 1481 → 1369`，其余基线不变 |

## 五、剩余可拆项与不拆的理由

| 候选 | 行数 | 判断 |
|---|---|---|
| 积分构成纯函数（`pkColor`/`pkAccountColorMap`/`pkBySource`/`pkDetailLimit*`/`pkExpiryMs`/`pkDetailCompare`） | ~88 | **暂不拆**：`PK_DEFAULT_DETAIL_LIMIT` 在 `app.js:1381` 被顶层立即求值（`lastDetailLimit = PK_DEFAULT_DETAIL_LIMIT`），`renderExpirySnapshot`（1400-1474）与 `app_views_packages.js` 双向依赖，拆它要连带改三处调用点，收益/风险比不划算 |
| 时间范围控件（`trange*`） | ~130 | 暂不拆：`trangeStates`/`trangeHandlers` 是跨视图共享状态，拆出后状态归属变模糊 |
| 其余散块 | — | 单块均 < 40 行，拆出后新文件的注释头成本接近收益 |

Go 侧保持不动：10 个文件卡在 700-800 行（`maxFileLines 801`），拆分边际收益低。

## 六、回滚

```powershell
git checkout -- internal/panel/app.js internal/panel/index.go internal/panel/routes.go internal/panel/security_test.go
Remove-Item -LiteralPath internal/panel/app_notifs.js, internal/panel/app_cmdp.js
git checkout -- .harness/depth-baseline.json
```

回滚后 `app.js` 回到 1481 非空行，`maxFrontendLines` 基线需同步改回 1481。
