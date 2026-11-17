# Playground 前端组件拆分（app.js 1640 → 1485 非空行）

日期：2026-11-17
类型：降体量（结构拆分，产品行为零改动）

## 一、做了什么

把 `internal/panel/app.js` 里的 API 调试台（Playground）块剪出到独立文件
`internal/panel/app_playground.js`。

| 文件 | 变化 |
|---|---|
| `app_playground.js` | **新增** 176 行（13 行注释头 + 163 行原 Playground 块） |
| `app.js` | 1541 物理行 → **1485 非空行**（原 1640） |
| `index.go` | 加 `//go:embed app_playground.js` + `appPlaygroundJS` 变量；`panelScripts` 在 `app_qr.js` 后插入条目；新增 `appPlaygroundScript` handler |
| `routes.go` | 加 `GET /panel/app_playground.js` 路由 |
| `security_test.go` | `TestSecurityHeadersOnAllPanelResponses` 路径表加一行 |
| `.harness/depth-baseline.json` | `maxFrontendLines` 1640 → 1481 → **1485**（二次收紧） |

## 二、拆分的合法性依据

面板前端的加载顺序由 `index.go` 的 `panelScripts` 数组唯一决定。既有的
`app_qr.js` 是「排在 `app.js` 之后」的先例，理由：它的函数只在事件处理器里调用，
首次调用时 `app.js` 早已求值完毕。

Playground 块形态相同：`pg*` 函数只在 `onclick` 与异步回调里被调用，
`let pgModels/pgAbort/pgBusy` 不被 `app.js` 的顶层代码读取。因此可以排在 `app.js` 之后。

## 三、拆分过程中发现的真 bug（本次最有价值的产出）

**症状**：拆分后首次运行时，浏览器 console 报

```
Uncaught ReferenceError: openPlayground is not defined
    at http://127.0.0.1:17863/panel/app.js:1534:54
```

**根因**：`app.js` 尾部的五处顶层绑定是**立即求值**的形式：

```js
if ($('btnPlayground')) $('btnPlayground').onclick = openPlayground;
```

`app_playground.js` 排在 `app.js` 之后 → 执行到这一行时 `openPlayground` 还未定义 →
抛 `ReferenceError` → **`app.js` 后续语句（含末尾的 `start()`）全部不再执行**。

后果不是"Playground 打不开"，而是**整个面板的动态数据不加载**——比原本要防的风险严重得多，
因为它不报错到用户可见的层面（页面静态骨架仍渲染）。

**修法**：五处绑定全部改为箭头函数延迟求值。

```js
if ($('btnPlayground')) $('btnPlayground').onclick = () => openPlayground();
if ($('btnPgSend')) $('btnPgSend').onclick = () => sendPlayground();
if ($('btnPgStop')) $('btnPgStop').onclick = () => pgStop();
// btnPgClose / btnPgClear 原本已是箭头函数，无需改
```

**为什么静态门禁没抓到**：`go test ./internal/panel/` 的三道牙齿测试
（`TestComponentLoadOrder` / `TestViewsComponentsAreFunctionOnly` /
`TestIndexHTMLHasNoHandWrittenAssetTags`）校验的是**文件名顺序**与**组件是否只含函数**，
不校验「跨文件符号在顶层是否被立即求值」。这是本次的**门禁缺口**，见第五节。

## 四、验证证据

| 项 | 命令 / 动作 | 结果 |
|---|---|---|
| 单元测试 | `go test ./internal/panel/` | `ok 48.229s`（三道牙齿测试未触发） |
| 编译 | `go build -trimpath -o %TEMP%\yg-play.exe ./cmd/server` | exit 0，16,154,112 B |
| 页面含新标签 | `curl http://127.0.0.1:17863/panel/` | `app_playground.js` 标签计数 **1** |
| **浏览器实测 · 打开** | bu 平面点 `#btnPlayground` | `#pgVeil` className `veil on`；10 个控件齐备（`pgModel`/`pgTemp`/`pgMaxTok`/`pgStream`/`pgSystem`/`pgPrompt`/`btnPgClear`/`btnPgSend`/`btnPgClose`） |
| **浏览器实测 · 关闭** | bu 平面点 `#btnPgClose` | className 回到 `veil`；无 console error |
| **浏览器实测 · console** | 全流程 | 仅 1 条 503（临时实例无上游账号池导致的 `/v1/models` 拉取失败），无 JS 错误 |
| 门禁 | `scripts/pre-commit.ps1 -Fast` | 11 项全绿 |
| 体量 | `scripts/check-depth.ps1 -Update` | `maxFrontendLines 1640 → 1481`，其余基线不变 |

### 一次排障记录（误判）

第一次浏览器验证时 `curl` 的 `app_playground.js` 匹配数为 0，一度怀疑是构建缓存。
实测根因：**临时实例与生产实例同时监听 7863**（`Get-NetTCPConnection` 显示两个 PID），
`curl` 命中的是**生产实例的旧二进制**。改用独立端口 17863 后立即匹配到 1。
这条记在这里，避免下次再把"端口串台"误读成"构建未生效"。

## 五、暴露的门禁缺口（未补，需单独决策）

现有牙齿测试不覆盖「`app.js` 顶层是否立即求值了排在它之后的组件符号」。
可加的检查形态：静态扫描 `app.js` 里所有 `= <identifierWithoutParens>` 形式的
`onclick` 赋值，断言被引用的标识符在 `app.js` 自身内有定义（即不得指向后续文件）。

未补的理由：需要遍历所有前端组件文件建立符号表，属于新增检查项而非本次拆分的一部分；
按「一轮改动只做一件事」的纪律留作独立任务。

## 六、回滚

```powershell
git checkout -- internal/panel/app.js internal/panel/index.go internal/panel/routes.go internal/panel/security_test.go
Remove-Item -LiteralPath internal/panel/app_playground.js
git checkout -- .harness/depth-baseline.json
```

回滚后 `app.js` 回到 1640 非空行，`maxFrontendLines` 基线需同步改回 1640。
