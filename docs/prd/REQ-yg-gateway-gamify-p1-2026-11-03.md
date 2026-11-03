# REQ-yg-gateway-gamify-p1-2026-11-03 — 面板游戏化（P1：成就带 / 趋势平滑 + 视觉验收）

- 状态：已落地（P1 两块 + 补 P0 视觉验收缺口）
- 来源：P0 交付后用户拍板「重新规划一下，继续去做吧」；P1 规划陈述并开工（成就带 + 趋势平滑 +
  视觉验收），P2（卡牌化/皮肤/叙事层）明确不做
- 流程级别：L1（前端展示 + 静态资源管线，同 P0）
- 关联：P0 见 `docs/prd/REQ-yg-gateway-gamify-p0-2026-11-03.md`（动效基座 setCount/celebrate/renderStreak）

## 必要性

**要解决的真实问题**（接 P0 观测，非清单套话）：

| 观测 | 出处 |
|---|---|
| P0 只给了「反馈型动效」，没有「长期目标感」：养号行为的持续投入没有任何可累积、可解锁的回报 | P0 交付后用户要求「往游戏化发展」的延续 |
| 趋势图成功率折线是直线连接，观感生硬；请求量没有趋势辅助线，变化方向靠人眼猜 | `internal/panel/trends.js`（P0 未动） |
| P0 交付披露的浏览器视觉验收缺口仍未补（P0 无 GUI 会话，机器验证绿但人眼未过） | P0 变更记录「未完成」清单 |

**不做会怎样**：游戏化停在「每次动作撒点彩屑」，留不住长期激励；趋势图仍是直线折线；
视觉层未经人眼核对，明暗主题/窄屏下的观感风险悬置。

**为什么是纯前端零依赖**：延续 P0 红线——成就带数据源**全部是本机记录**（localStorage
`yg_streak_v1` 连续天数 + 新增 `yg_ach_v1` 操作计数），后端**没有**操作历史，派生展示标
「本机记录」（四态纪律：derivation，不冒充上游 observation）；趋势平滑是**纯视觉插值**
（Catmull-Rom），数据点位置不变（observation 圆点保留可见），不产生新事实。

## 范围

1. `app_views_achievements.js`（新增）：`ACHIEVEMENTS` 常量数组 5 项（初次上手 ops≥1 / 渐入
   佳境 ops≥5 / 老练玩家 ops≥10 / 连登三日 streak≥3 / 七日同行 streak≥7，每项
   `{id,n,d,c,t}`，`c` 为徽章内展示字——反 AI slop：数字/汉字，不用 emoji）；`achOps()` 读
   `yg_ach_v1`（异常按 0 不臆造）；`noteOp()` 递增写回；`renderAchievements()` 动态创建
   `#achStrip` 插到 `streakStrip`/`qProg` 之前（同 P0 renderStreak 模式）。**顶层只放声明**
   （遵守 TestViewsComponentsAreFunctionOnly 白名单；实测踩坑：数组结尾 `];` 顶格触发白名单
   FAIL——判据只认 `function/const/let/var/*/…`，缩进成 `  ];` 修复）。
2. `app_views_gamify.js`（修改）：`noteStreak()` 末尾追加 `noteOp(); renderStreak();
   renderAchievements();`（每次操作同时累计操作数并重绘两条）。
3. `gamify.css`（修改）：追加 `.ach` 成就条样式（横向徽章、锁定=灰底虚线、解锁=accent 实底
   徽章 + `ach-pop` 到位动画；reduced-motion 由 index.html:35 全局规则关闭）。
4. `index.go`（修改）：embed `app_views_achievements.js` + `appViewsAchievementsScript` handler
   （CSP/setSecurityHeaders 全套 + `// 忽略：`论证注释同口径）+ `panelScripts` 清单插在
   `app_views_tasks.js` 之后、`app_views_gamify.js` 之前（满足 TestComponentLoadOrder）。
5. `panel.go`（修改）：routes() 加 `GET /panel/app_views_achievements.js`。
6. `app.js`（修改）：`start()` 在 `renderStreak()` 后加 `renderAchievements()`（初始渲染）。
7. `security_test.go`（修改）：静态资源清单加该路由（标注 P1）。
8. `trends.js`（修改）：新增 `smoothPath(xy)`（Catmull-Rom → 三次贝塞尔，返回 SVG path d，
   不足 2 点返回 null）；成功率曲线改为**分段平滑**——遇 `rate===null` 时 flush 当前段，
   **无观测日断开、不插值**（四态纪律）；请求柱状后加 3 日均值线（`ma` 从 i≥2 起
   `(req[i-2]+req[i-1]+req[i])/3`，`var(--accent-2)` 深绿 1.4px opacity .55）；`trChartNote`
   文案追加 `' · 深绿线 = 3 日均值（derivation）'`。
9. `frontend_bundle_test.go`（新增测试）：`TestViewScriptsReferenceExistingDOMIDs`——视图脚本
   `getElementById('X')` 的 X 必须存在于下发 HTML 的 id 集合或脚本自身动态创建的 id 集合。
   **动机（P1 实测踩坑）**：renderStreak/renderAchievements 原查 `'taskscenter'`，实际 DOM id
   是 `'view-taskscenter'` → 宿主恒 null、静默 return，P0 连续天数条与 P1 成就带**从未渲染**；
   现有 smoke 桩 getElementById 恒返回 inert，对该类「id 打错但不报错」失效全盲。修复两处选择器
   为 `'view-taskscenter'` 并加本测试防回归。
10. 视觉验收（补 P0 缺口）：`computer_use`（cu 平面）开本机 Chrome 访问临时实例
    `127.0.0.1:7864/panel/`，截图核对彩屑/能量环/连续天数条/成就带/平滑趋势；验收后停进程、
    删临时 config.json（防测试 api_key 泄漏，P0 先例）。

**非目标**：P2（卡牌化+皮肤、叙事层）不做；不动后端接口/字段/上游协议；不部署生产。

## 验收（Given-When-Then）

- **F1** Given 任务中心打开且 `renderAchievements()` 已执行，When 本机 localStorage 操作计数
  ops / 连续天数 streak 已知，Then `#achStrip` 出现在连续天数条之前，5 项按 `t(state)` 判定
  解锁（accent 实底徽章 + ach-pop）或锁定（灰底虚线）；初始 0 记录时全部锁定但条可见。
- **F2** Given 任意成功操作（签到/领取/加号，经 noteStreak 路径），When noteStreak 执行，
  Then `noteOp()` 递增 `yg_ach_v1` 并重绘成就带；重复无新增操作不凭空解锁。
- **F3** Given 趋势页有 ≥2 个有成功率观测的连续日，When 渲染，Then 成功率折线为平滑曲线且
  观测点圆点仍在原位；Given 某日无观测（rate===null），Then 该处曲线**断开**，不跨 null 插值。
- **F4** Given 趋势页 ≥3 天请求数据，When 渲染，Then 出现 3 日均值线（深绿、细、半透明）且
  note 文案含「3 日均值（derivation）」；不足 3 天不画。
- **F5** Given P0+P1 全部改动，When 本机 Chrome 打开 `/panel/` 走密钥门登录，Then 任务中心
  可见连续天数条与成就带、账号池 hero 卡可见能量环、趋势页可见平滑曲线（无 JS 报错）。
- **F6** Given 全部改动，When 跑 `scripts/pre-commit.ps1`，Then 退出码 0；深度棘轮未回退；
  资源清单生成式不变（成就带走 panelScripts，index.html 只留占位符）。

## 红线自查

- 零依赖：未新增 go.mod 依赖，无外部库（纯 CSS/SVG/DOM）。✓
- 四态纪律：成就带/连续天数标「本机记录」（derivation）；成功率无观测日断开不插值；3 日均值
  标注 derivation；`--ring` 总额未知留空（P0 不变）。✓
- 反 AI slop：徽章用数字/汉字不用 emoji；无紫渐变；颜色全用现有 CSS 变量。✓
- reduced-motion：全局规则（index.html:35）关闭 ach-pop/彩屑。✓
- R8/依赖方向/R10：未触碰上游协议、依赖表、Go 业务代码。✓
- 深度棘轮：只许收紧；achievements 走清单不手写标签。✓
