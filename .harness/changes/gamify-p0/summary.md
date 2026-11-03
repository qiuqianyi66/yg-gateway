# gamify-p0 变更摘要

> 目录名与 git 分支同名（main 上直接落地，未开 feature 分支）。
> 本文件是这次改动的审计凭据。

需求：`docs/prd/REQ-yg-gateway-gamify-p0-2026-11-03.md`
分级：L1（前端展示 + 静态资源管线）
ADR：不需要（L3 才强制；本改动无数据模型/上游协议/难逆转决策）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/panel/app_views_gamify.js` | P0 动效组件：`setCount`（数字滚动）、`celebrate`（彩屑）、`noteStreak/streakDays/renderStreak`（本机连续操作天数，localStorage 派生，容器 JS 创建）。只放声明，遵守 TestViewsComponentsAreFunctionOnly |
| `新增：internal/panel/gamify.css` | toast 入场动画 + 状态图标、hero 能量环、彩屑覆盖层、连续天数条。独立文件：给 index.html 减负 + 可整体回滚；CSP style-src 'self' 已允许 |
| `新增：internal/panel/gamify_test.go` | serving 全路由 + embed 非空 + 反查 index.html 不残留游戏化样式（防双份实现） |
| `修改：internal/panel/index.html` | hero 积分卡 `hero-ring` + `id=statCredits`、`.stat` 补 `position:relative`。**资源标签不手写**：head 与 body 尾部改为 `<!--panel-styles-->` / `<!--panel-scripts-->` 占位符，由 `renderIndexHTML()` 按 `panelScripts`/`panelStyles` 清单展开（见并行改动 `.harness/changes/2026-11-03-panel-asset-registry/`）——index.html 非空行 1844→**1827**，棘轮同步收紧 |
| `修改：internal/panel/app.js` | `toast()` 加 `.tst-ic` SVG 形符；签到/领取/添加账号三处挂 `celebrate()+noteStreak()`；`start()` 加 `renderStreak()` |
| `修改：internal/panel/app_views_accounts.js` | loadOverview 统计赋值换 `setCount`（sTodayTok 保留直赋：小数后缀滚动会造假中间值）；`#statCredits` 写 `--ring` 百分比 |
| `修改：internal/panel/index.go` | embed `gamify.css` / `app_views_gamify.js` + 两个 handler；**资源清单生成式改造**（`panelStyles`/`panelScripts` 清单含 gamify 两个文件、`renderIndexHTML()` 展开占位符、`index` handler 改写渲染产物） |
| `修改：internal/panel/panel.go` | routes() 注册 `/panel/gamify.css`、`/panel/app_views_gamify.js` |
| `修改：internal/panel/security_test.go` | 静态资源清单补两个新资源（安全头全覆盖断言） |
| `新增：docs/prd/REQ-yg-gateway-gamify-p0-2026-11-03.md` | 需求 + Given-When-Then 验收 |

**没做的（明写，免得下轮重提）**：
- P1（成就带、趋势图平滑）/ P2（卡牌化+皮肤、叙事层）——未做，是后续排期。
- 批量签到/批量任务按钮不加庆祝（只单账号 checkin 触发，防噪声）。
- 不做浏览器视觉像素级验收（本机无 GUI 会话；DOM/CSS 结构、资源链路、语法、运行时已机器验证）。
- 不动 `#statCredits` 之外的其它统计卡布局；不改任何后端接口/字段。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 前端语法/拼接/运行时 smoke | `go test ./internal/panel/` | ok（47s，含 TestAppJSSyntax 逐文件 node --check、TestAppJSTopLevelSmoke 7 场景 bundle 求值、TestViewsComponentsAreFunctionOnly 白名单） |
| 全仓测试 + vet + build | `scripts/pre-commit.ps1` | 全绿：gofmt 干净、R10/R3 新增行检查过（`renderIndexHTML()` 的 `_ =` 已带「忽略」论证注释）、`go build ./...` 过、`go vet` 无输出、`go test ./...` 25 包 ok、依赖方向过、**深度棘轮未回退（maxFrontendLines 1827，由生成式方案收紧）**、治理体检无 error、上下文预算压线过 |
| 真实二进制链路 | `go build -o artifacts/manual-tests/gamify-p0-20261103/yg-gateway-test.exe ./cmd/server` 起 127.0.0.1:7864 临时实例后 curl | `/panel/` 200 且含 gamify.css link / app_views_gamify.js script / hero-ring / statCredits；`/panel/gamify.css` 200（2631B）、`/panel/app_views_gamify.js` 200（5225B），均带全套 CSP 头；streakStrip 由 JS 动态创建（HTML 无静态容器，符合棘轮） |
| 深度棘轮专项 | `scripts/check-depth.ps1` | ✅ 未回退（1842） |

**负向探针**：无（本改动无新脚本/新检查项，测试断言全部真跑过；serving 测试走 `p.ServeHTTP` 全路由，
漏注册路由会直接红——这正是补这两个测试的目的）。

**棘轮说明（如实披露，含并行协同）**：初版把动效 CSS 内联进 index.html，`maxFrontendLines 1842→1880`
回退；`check-depth.ps1 -Update` 在回退态被脚本短路（实测 exit 1 且不写基线，即「严禁 -Update 掩盖
回退」的强制实现）。随后拆 CSS 独立成 `gamify.css`（embed + 路由 + CSP 已含 'self' 无需放宽）、
streak/confetti 容器改 JS 动态创建。**并行工作区同目标推进**了更彻底的结构方案（见
`.harness/changes/2026-11-03-panel-asset-registry/`）：资源清单收敛到 `panelStyles`/`panelScripts`
一处、index.html 只留两个占位符、`renderIndexHTML()` 展开——`gamify.css` 与 `app_views_gamify.js`
登记进清单，index.html 非空行 **1844→1827**，`check-depth -Update` 合规收紧棘轮（唯一变动项），
并新增 `TestIndexHTMLHasNoHandWrittenAssetTags` / `TestIndexHTMLPlaceholdersReplaced` 两条防
静默失效测试。**两个改动不可拆提交**（清单与 embed 引用 gamify 文件；仅提交任一侧都会红门禁），
故合并为一个可独立验证绿的提交 `a2d99d5`。

## 三、未完成 / 待补（诚实清单）

- [ ] 浏览器视觉验收未做（requires：有 GUI 的会话/截图，确认彩屑观感、能量环在明暗主题下的
      可辨度、760px 窄屏下连续天数条表现）——P0 行为均已机器验证，视觉层建议人眼过一遍。
- [ ] P1/P2 未排期（成就带、趋势平滑、卡牌化）——下轮立项时补 PRD。

## 四、怎么回滚

纯前端 + 静态资源管线改动，`git checkout` 即可：

```
git checkout -- internal/panel/index.html internal/panel/app.js internal/panel/app_views_accounts.js internal/panel/index.go internal/panel/panel.go internal/panel/security_test.go
git rm internal/panel/app_views_gamify.js internal/panel/gamify.css internal/panel/gamify_test.go docs/prd/REQ-yg-gateway-gamify-p0-2026-11-03.md
```

- 不涉及二进制换版（未部署生产）；`D:\yg-gateway` 生产实例不受影响（改动未上线）。
- 本地存储键 `yg_streak_v1`（浏览器 localStorage）不回滚、无害：重新打开面板自动重建，
  旧记录不产生错误数据（派生展示，非权威源）。
- 验证回滚后：`go test ./internal/panel/` 全绿 + `scripts/pre-commit.ps1` 通过。

## 五、Learning

根因：给被深度棘轮（maxFrontendLines 只许收紧）盯住的 index.html 加功能，任何「新元素 + 新样式
内联/手写标签」都会推高非空行数；而 `check-depth.ps1 -Update` 在回退态被脚本短路、不存在合法抬棘轮
通道。

处理（两层，最终并行为一体）：①样式独立成文件 + 容器 JS 动态创建，给 index.html 减负；
②资源标签彻底生成式化（清单一处维护 + 占位符 + 展开缓存），**组件数增长不再吃页面骨架行数**——
index.html 净减负 17 非空行，棘轮 1842→1827 收紧。

是否升级 pattern：Seen=2（2026-11-03 占位符版为仓库既有组件拆分先例的延续，本目录首次单列）。
升级为规则：「给 index.html 加前端资源一律走 panelStyles/panelScripts 清单（禁手写标签），
合入前跑 check-depth.ps1 自查」。
