# 修复 a270626 引入的延迟初始化竞态

日期：2026-11-17
类型：回归修复（CI e2e 打穿）+ 顺带收回 1 行前端棘轮
上游：`.harness/changes/2026-11-17-panel-splits-2/`（通知中心 + 命令面板拆分）

## 一、症状

CI run `44083934563`（commit `2c585a3`，只改了注释）e2e 3 failed / 1 passed：

```
pageerror: initNotifCenter is not defined
```

三个失败用例（密钥门 / 正确密钥进池 / opsagent 空态）全部停在
`expect(errs).toEqual([])`。**而 `a270626` 的 e2e（`44081795954`）是 success** ——
同一份 `app.js`，两次判定相反。

## 二、根因（实测确认）

`a270626` 把两处顶层 init 改成了：

```js
setTimeout(() => { initNotifCenter(); initCmdPalette(); }, 0);
```

**`setTimeout(0)` 不是"等所有脚本加载完"的可靠信号**。解析器遇到 `<script src>`
是"发起请求 → 阻塞等返回 → 执行 → 继续解析"，**请求往返期间事件循环可以跑别的任务**
（含 0ms 定时器）。因此该回调有机会在 `app_notifs.js` 还没执行时就触发 →
`initNotifCenter` 未定义 → 未捕获 ReferenceError → `pageerror`。

这解释了两次判定相反：它是**竞态**，不是稳定复现。本地同机响应在微秒级，
手工测不出来；CI 上网络往返更难料。

**旁证**：4 个用例里唯一通过的那个（八视图切换）耗时 1.6s，三个失败都在 0.5–0.7s ——
跑得久的那个，定时器早已执行完。

## 三、修法（两次迭代，第一次被自己的新测试打回）

### 第一版：DOMContentLoaded —— 被 `TestScriptsEvalInBrowserOrder` 否掉

```js
document.readyState === 'loading' ? document.addEventListener('DOMContentLoaded', lateInit, { once: true }) : lateInit();
```

本地 e2e 4/4、重复 4 轮 16/16 全过。但 `go test ./...` 里新加的
`TestScriptsEvalInBrowserOrder` 报：

```
SYNC FAIL at app.js / app.js:1418 / const lateInit = () => { initNotifCenter(); initCmdPalette(); };
```

原因：测试 sandbox 的 `document.readyState` 是 `undefined`（≠ `'loading'`），
走 `else` 分支**立即调用** `lateInit()` → 未定义符号。
**这不是测试的假牙，是真实缺陷**：脚本被动态插入到已解析 DOM 时，
`readyState` 不是 `loading`、`DOMContentLoaded` 已触发，照样会炸。

### 第二版（最终）：组件自己在自己文件末尾初始化

```js
// app_notifs.js 末尾
initNotifCenter();
// app_cmdp.js 末尾
initCmdPalette();
```

`app.js` 里的延迟调用整段删除。**时序上不可能失配**：调用点与定义点在同一文件，
执行到该行时符号必然已存在；两个脚本都在 `</body>` 前（`index.html:1037`），
DOM 已完整，`$('btnNotif')` / `$('cmdInput')` 都能取到。

不再需要 `setTimeout` / `DOMContentLoaded` / `readyState` 任何一种"猜时机"的手法。

## 四、验证证据

| 项 | 命令 | 结果 |
|---|---|---|
| 本地 e2e（第一版） | `npx playwright test` | 4 passed (5.9s) |
| 本地 e2e（第一版重复） | `--repeat-each=4` | 16 passed (16.5s) |
| JS 守卫测试（第一版） | `go test -run TestScriptsEvalInBrowserOrder` | **FAIL**（见第三节） |
| 本地 e2e（最终版） | `--repeat-each=4` | **16 passed (17.1s)** |
| JS 守卫测试（最终版） | `-run 'TestScriptsEval\|TopLevelSmoke\|LoadOrder\|FunctionOnly\|AppJSSyntax'` | ok 2.405s |
| 行数棘轮 | `check-depth.ps1 -Update` | `maxFrontendLines 1369 → 1368`（收紧） |
| 全量门禁 | `pre-commit.ps1`（11 项） | 全部通过 |

**诚实的边界**：本地能证明"最终版 16 次重复通过、守卫测试通过"，
但**不能证明原实现本地必红**——竞态性质决定了它在同机快响应下不暴露。
原实现必红的证据来自 CI log，不是我本地复现的。

## 五、为什么前面的门禁没拦住原实现

- `TestScriptsEvalInBrowserOrder`（同日新增）用 `setTimeout(..., 120)` 收尾，
  给异步初始化留了 120ms —— **模拟时序比真实浏览器宽松**。
  该测试的能力边界应记为「证明求值阶段不抛错」，**不**证明「真实加载时序下不抛错」。
- `TestAppJSTopLevelSmoke` 跑完整 bundle，本来就不覆盖时间窗口。
- 唯一真信号是浏览器级 e2e —— **它抓到了**（且连带抓出了第一版修法的缺陷）。

## 六、回滚

```powershell
git checkout -- internal/panel/app.js internal/panel/app_notifs.js internal/panel/app_cmdp.js
```

回滚即回到 `setTimeout(0)` 版本（e2e 会重新变成 flaky）。
