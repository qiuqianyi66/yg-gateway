# gamify-p1 变更摘要

> 目录名与 git 分支同名（main 上直接落地，未开 feature 分支）。
> 本文件是这次改动的审计凭据。

需求：`docs/prd/REQ-yg-gateway-gamify-p1-2026-11-03.md`
分级：L1（前端展示 + 静态资源管线）
ADR：不需要（L3 才强制；无数据模型/上游协议/难逆转决策）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：internal/panel/app_views_achievements.js` | P1 成就带：`ACHIEVEMENTS` 5 项常量 + `achOps/noteOp/renderAchievements`。数据源全部本机记录（`yg_streak_v1` 连续天数 + `yg_ach_v1` 操作计数），UI 标「本机记录」（derivation）。顶层只放声明（数组结尾 `  ];` 缩进满足白名单）；动态创建 `#achStrip` 插到 `streakStrip`/`qProg` 前 |
| `修改：internal/panel/app_views_gamify.js` | `noteStreak()` 末尾追加 `noteOp(); renderStreak(); renderAchievements();`；**修复 P0 遗留 bug**：renderStreak 容器选择器 `'taskscenter'` → `'view-taskscenter'`（原 id 不存在 → 宿主恒 null、连续天数条从未渲染） |
| `修改：internal/panel/app_views_achievements.js` | （同上文件）renderAchievements 容器选择器同步用 `'view-taskscenter'` |
| `修改：internal/panel/gamify.css` | 追加 `.ach` 成就条样式（锁定=灰底虚线、解锁=accent 实底徽章 + `ach-pop` 动画，reduced-motion 由全局规则关闭） |
| `修改：internal/panel/index.go` | embed `app_views_achievements.js` + `appViewsAchievementsScript` handler（CSP 全套）+ `panelScripts` 清单插在 `app_views_tasks.js` 之后、`app_views_gamify.js` 之前 |
| `修改：internal/panel/panel.go` | routes() 注册 `GET /panel/app_views_achievements.js` |
| `修改：internal/panel/app.js` | `start()` 在 `renderStreak()` 后加 `renderAchievements()` |
| `修改：internal/panel/security_test.go` | 静态资源清单补 achievements 路由 |
| `修改：internal/panel/trends.js` | `smoothPath`（Catmull-Rom→贝塞尔）；成功率**分段平滑**（null 断开不插值，观测圆点保留）；请求柱状后 3 日均值线（accent-2 深绿 1.4px opacity .55）；note 文案追加「3 日均值（derivation）」 |
| `修改：internal/panel/frontend_bundle_test.go` | 新增 `TestViewScriptsReferenceExistingDOMIDs`：脚本 `getElementById` 的静态 id 必须存在于下发 HTML 或由脚本动态创建（防「id 打错→静默不渲染」回归，见踩坑） |
| `新增：docs/prd/REQ-yg-gateway-gamify-p1-2026-11-03.md` | 需求 + Given-When-Then 验收 |

**没做的（明写，免得下轮重提）**：
- P2（卡牌化+皮肤、叙事层）——明确不做，非排期。
- 后端改动、生产部署——不做（纯前端 L1）。
- 代理 fail-open / 11102 e2e / ban-risk 三条网关核心待办（并行工作流交接）——已登记任务
  #11-#13，**不并入**本 P1 范围，独立跟进。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 前端语法/拼接/白名单/运行时 smoke + 新 DOM id 回归 | `go test ./internal/panel/` | ok（49-55s 两轮，含 node --check、7 场景 bundle 求值、TestViewsComponentsAreFunctionOnly、TestViewScriptsReferenceExistingDOMIDs、serving 全路由、security 资源清单） |
| 新回归测试单独跑 | `go test ./internal/panel/ -run TestViewScriptsReferenceExistingDOMIDs -v` | PASS（0.01s） |
| 全仓门禁 | `scripts/pre-commit.ps1` | **PASS（11/11 全过，2026-11-03）**：gofmt/go build/vet/go test 25 包/边界/深度棘轮/治理体检/上下文预算/R6 全绿。首跑仅 1 项 FAIL（security_test.go 新增行 gofmt 对齐漂移）→ `gofmt -w` 修复后全绿 |
| 真实二进制链路 | `go build -trimpath -ldflags="-s -w"` 起 127.0.0.1:7864 临时实例 | `/panel/` 200（103880B，含 achievements 脚本进清单）；`/panel/app_views_achievements.js` 200（含 `view-taskscenter` 修复）；curl 逐字节验证 gamify.js/achievements.js 下发内容 |
| 视觉验收（补 P0 缺口） | 本机 Chrome（computer_use cu）截图核对连续天数条/成就带/平滑趋势 | **进行中**：修复前截图已证 bug 真实表现（任务中心无 streak/ach 条）；修复后服务已起（PID 32140）。**cu GUI 被并行 Magpie 桌面会话持续占用**（"Another Computer Use task in progress"）；**豆包内置浏览器（bu）对该面板页面禁用了全部 JS**（keyVeil 初始隐藏即未登录态、页面全局函数全 undefined——bu 环境限制，非面板缺陷；本机 Chrome 登录弹窗正常出现已证 JS 可用）。修复后含 streak/ach 条的截图核对待 GUI 释放后补 |

**踩坑记录（如实披露，两处，均已修 + 防回归）**：

1. **白名单不认 `]` 开头顶层行**：`TestViewsComponentsAreFunctionOnly` 判据只认非缩进行以
   `function/const/let/var//\*/*///}/)` 开头，成就带 `ACHIEVEMENTS` 数组结尾 `];` 顶格触发
   FAIL。修复：缩进成 `  ];`（2 空格，JS 不敏感缩进，语义不变）。
2. **P0 遗留：渲染容器 id 打错 → 静默不渲染**：`renderStreak`/`renderAchievements` 原查
   `getElementById('taskscenter')`，实际 DOM id 是 `view-taskscenter`（index.html
   `<section id="view-taskscenter">`）→ 宿主恒 null、函数静默 return，**连续天数条与成就带
   从未渲染**。P0 交付时无视觉验收（披露过缺口）所以未暴露；现有 smoke 桩
   `getElementById: () => inert` 对该类失效完全盲。修复：两处选择器改 `'view-taskscenter'`；
   新增 `TestViewScriptsReferenceExistingDOMIDs` 机器锁定（静态 id 必须存在于 HTML 或动态
   创建集合），同类错误以后直接红。

## 三、未完成 / 待补（诚实清单）

- [ ] 视觉验收截图证据：修复后截图核对待补——cu GUI 被并行 Magpie 桌面会话占用、bu 内置浏览器禁 JS（环境限制）。机器侧证据已闭合：panel 测试全绿（含 DOM id 回归）、服务 200 + 下发脚本逐字节含修复、修复前截图已记录 bug 真实表现
- [ ] git 提交：本文件落盘后执行（L-21 只提交本任务 12 项文件）

## 四、怎么回滚

纯前端 + 静态资源管线改动，`git checkout` 即可：

```
git checkout -- internal/panel/app_views_gamify.js internal/panel/gamify.css internal/panel/index.go internal/panel/panel.go internal/panel/app.js internal/panel/security_test.go internal/panel/trends.js internal/panel/frontend_bundle_test.go
git rm internal/panel/app_views_achievements.js docs/prd/REQ-yg-gateway-gamify-p1-2026-11-03.md
```

- 不涉及二进制换版（未部署生产）；`D:\yg-gateway` 生产实例不受影响。
- 本地存储键 `yg_streak_v1` / `yg_ach_v1`（浏览器 localStorage）不回滚、无害：派生展示，
  非权威源，重新打开自动重建。
- 验证回滚后：`go test ./internal/panel/` 全绿 + `scripts/pre-commit.ps1` 通过。

## 五、Learning

根因（两条，同源）：给「会被机器检查盯住」的前端加代码时，①测试判据的**白名单枚举**不覆盖
全部合法顶层行（`]` 结尾的跨行字面量）；②运行时 smoke 桩的 `getElementById` **恒返回 inert**
导致「选择器 id 不存在」成为静默失效——这两类都表现为**测试绿、真实 DOM 挂**。

处理：①缩进满足现有判据（不动判据：它是防 app.js 初始化前执行的结构保护）；②修复选择器 +
新增**静态一致性测试**（脚本引用的静态 DOM id ∈ 下发 HTML ∪ 脚本动态创建集合），把「id 打错」
从运行时盲区变成编译期红。

是否升级 pattern：Seen=2（「测试绿但真实 DOM 失效」——P0 占位符静默失效已升级为
TestIndexHTMLPlaceholdersReplaced；本目录第二次）。升级为规则：「给面板加 JS 渲染逻辑时，
getElementById 引用的必须是 index.html 既有 id 或本脚本动态创建的 id；合入前跑
TestViewScriptsReferenceExistingDOMIDs」。
