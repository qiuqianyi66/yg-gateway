# 补齐门禁缺口：跨文件加载顺序求值检查

日期：2026-11-17
类型：测试基建（新增一条牙齿测试，产品代码零改动）
前置：`.harness/changes/2026-11-17-playground-split/` 第五节记录的「暴露的门禁缺口」

## 一、缺口是什么

2026-11-17 拆 Playground 时踩到一个真 bug：`app.js` 尾部的

```js
if ($('btnPlayground')) $('btnPlayground').onclick = openPlayground;
```

是**立即求值**，而 `openPlayground` 定义在排在 `app.js` 之后的 `app_playground.js`。
浏览器里这一行抛 `Uncaught ReferenceError`，**`app.js` 后续语句（含末尾 `start()`）全部不执行**
→ 面板静态骨架照常渲染、动态数据全空白。

修好之后我把这个缺口记为「留作独立任务」。本轮把它做掉了。

## 二、为什么现有测试抓不到（根因，非猜测）

现有 `TestAppJSTopLevelSmoke` 跑的是 **`scriptBundlePath(t)`——全部脚本拼接后的完整 bundle**，
所有符号一开始就在作用域里，**不存在"后面还没定义"的时间窗口**。

`frontend_bundle_test.go:133` 的原始注释写着「顺序与浏览器一致，语义等价」——
**这句话是错的**，本轮实测证伪：拼接求值只保留"顺序"，抹掉了"逐文件执行的时间维度"。

第二个、更隐蔽的原因：该测试的 sandbox 是

```js
new Proxy({...}, { get(t, k) { return t[k]; }, has() { return true; } })
```

`has: () => true` 让**所有未声明标识符**都被视为"存在于全局"，于是 `vm` 不抛
`ReferenceError`，而是返回 `undefined`。**即使换成逐脚本求值，只要还用这个 sandbox，
这个 bug 依然抓不到**——这一条是我第一次写测试时实测发现的（新测试变异后仍绿）。

## 三、做了什么

新增 `TestScriptsEvalInBrowserOrder`（`internal/panel/frontend_test.go`）。

| 设计点 | 取值 | 理由 |
|---|---|---|
| 脚本清单来源 | `scriptFiles(t)`（读 `renderIndexHTML()` 里的 `<script src>`） | 与浏览器实际下发顺序同源，不手抄 `panelScripts` |
| 求值方式 | 逐个 `vm.runInContext` | 复现 `<script src>` 逐文件执行的时间窗口 |
| sandbox | **普通对象**，不是 Proxy | 让未声明标识符真的抛 `ReferenceError`（Proxy 的 `has:()=>true` 会吞掉） |
| 异步阶段 | `setTimeout(..., 120)` + `process.on('uncaughtException')` | 覆盖「延迟初始化里调用后续组件」抛的异步错误 |
| sandbox 补充 | `AbortController` / `AbortSignal` | `abortable()`（app.js:79）需要；首次运行时因缺失报 `TypeError` |
| 退出方式 | 显式 `process.exit(0/1)` | `start()` 的 `setInterval` 会让 Node 事件循环不退出 |

## 四、牙齿验证（变异测试，本轮核心证据）

把 `app.js` 的 `onclick = () => openPlayground()` 改回 `onclick = openPlayground`，跑两个测试：

| 测试 | 变异后 | 说明 |
|---|---|---|
| `TestScriptsEvalInBrowserOrder`（新） | **FAIL** | `SYNC FAIL at app.js` / `app.js:1421` / `onclick = openPlayground`，精确指向出错行 |
| `TestAppJSTopLevelSmoke`（旧） | **ok** | 证明它确实抓不到这类缺陷 |

还原后：`ok 0.23s`。**这条牙齿是真的会咬人的，不是空转。**

## 五、验证证据

| 项 | 命令 | 结果 |
|---|---|---|
| 单测（新用例） | `go test ./internal/panel/ -run TestScriptsEvalInBrowserOrder -v` | `PASS 0.23s` |
| 全量 panel | `go test ./internal/panel/` | `ok 55.922s` |
| 格式 | `gofmt -l ./cmd ./internal` | 无输出 |

## 六、过程中一次事故（已修复，记录以免重演）

第一次加 `AbortController` 时，`Edit` 的 `old_string` 写得太长、把 sandbox 定义后半段
（`console, JSON, …`、Proxy 收尾、`createContext`、`const files = …`）一起吞掉了。
发现方式：立刻 `Read` 该段，看到 `AbortController, AbortSignal,` 后面直接跟
`for (const f of files) {`。已补回。

**纪律**：`Edit` 的 `old_string` 只取**最小唯一片段**，改完长段落后必须回读确认。

## 七、能力边界（诚实声明）

本测试证明的是「**按当前下发顺序逐个求值，所有同步语句 + 延迟初始化不抛错**」。
它**不**证明：

- 页面功能真的可用（DOM 桩是惰性的，`getElementById` 恒返回 Stub，断言不到渲染结果）
- 异步分支里的业务逻辑正确（`fetch` 恒返回 pending Promise）

功能可用性仍需浏览器实测（本轮同批拆分已有 bu 平面实测记录，见前两份 summary）。

## 八、回滚

```powershell
git checkout -- internal/panel/frontend_test.go
```

回滚后 `panel` 包少一条用例，其余不受影响（本变更不碰产品代码）。
