# REQ-yg-gateway-gamify-p0-2026-11-03 — 面板游戏化动效层（P0：数字滚动 / 庆祝动效 / 连续天数）

- 状态：已落地（P0 三块）
- 来源：第一轮评估结论（用户给定 huashu-design / huashu-art-motion 两个参考仓库，评估后拍板
  「养号游戏化仪表盘」P0 路线并执行）
- 流程级别：L1（前端展示）+ 资源管线（新增 go:embed CSS 文件与两条静态路由，无业务逻辑/
  数据模型/上游协议改动）

## 必要性

**要解决的真实问题**（引评估轮观测，非清单套话）：

| 观测 | 出处（第一轮评估实读代码） |
|---|---|
| 面板数据层已有游戏结构（成长任务 +300 分、奖励领取、幸运值、夜猫子、签到/保活/旅行巡检），但 UI 平铺成表格，没有任何「奖励到手」的即时反馈 | `internal/panel/` 任务中心与账号池视图 |
| 积分/账号统计是纯数字，无动画、无比例可视化；`#statCredits` 只显示 `剩余 / 总额` 字符串，占比要心算 | `internal/panel/app_views_accounts.js` loadOverview |
| 动效反馈缺失：签到/领取成功只有一条 toast，无庆祝态 | `internal/panel/app.js` checkin/claim 分支 |
| 连续签到/保活是上游强激励（连登点亮），面板无「本机操作连续性」的展示 | 上游签到/活跃上报字段，面板无对应 UI |

**不做会怎样**：游戏化三层路线（P0→P1→P2）无从谈起，面板继续「数据全但无温度」；
P1/P2 的成就带、趋势平滑都依赖 P0 打下的动效基座（setCount 数字滚动 / celebrate / streak）。

**为什么是纯前端**：零依赖红线（go:embed 单二进制，不引 ECharts 等外部库）——三块动效全部
纯 CSS + 原生 DOM 实现；后端**没有签到历史**（只有 `last_checkin_day` 当日态，跨零点过期），
连续天数只能由 localStorage 派生，UI 文案强制标「本机记录」（四态纪律：derivation，不冒充
上游 observation）。

## 范围

1. `app_views_gamify.js`（新增）：`setCount`（整数前缀 600ms easeOutCubic 滚动，reduced-motion/
   无 rAF 直接落值，负数/非数字不滚，末帧用原文防取整残留）、`celebrate`（26 片 CSS 色块飞散，
   reduced-motion 直接 return，animationend 自清理）、`noteStreak/streakDays/renderStreak`
   （localStorage `yg_streak_v1` 存最近 90 天操作日，7 日圆点条 + 连续天数，容器 JS 动态创建）。
2. `gamify.css`（新增）：toast 入场动画 + 状态图标、hero 卡 `hero-ring` 能量环（conic-gradient +
   mask，`--ring` 百分比由 JS 写）、`#confetti` 覆盖层、`.streak` 连续天数条。
   **独立 CSS 文件而非内联进 index.html**：index.html 已超深度棘轮（maxFrontendLines 只许收紧），
   动效样式拆出既减负又可整体回滚；CSP `style-src 'self' 'unsafe-inline'` 已含 'self'，无需放宽。
3. `index.html`：hero 积分卡加 `hero-ring` class 与 `id="statCredits"`、`.stat` 规则补
   `position: relative`。**资源标签不手写**：head 与 body 尾部为 `<!--panel-styles-->` /
   `<!--panel-scripts-->` 占位符，`gamify.css` / `app_views_gamify.js` 登记进 `panelStyles` /
   `panelScripts` 清单，由 `renderIndexHTML()` 展开（并行改造 `.harness/changes/
   2026-11-03-panel-asset-registry/`，消除「组件数增长吃页面骨架行数」的结构性缺陷）。
   streak/confetti 容器由 JS 创建（HTML 骨架保持最小）。
4. `app.js`：`toast()` 加 `.tst-ic` SVG 形符（✓/✗/•，aria-hidden，颜色只是第二通道）；签到成功
   （accBody 委托 checkin 分支）、任务领取成功（taskBody 委托 claim 分支）、添加账号成功三处挂
   `celebrate() + noteStreak()`；`start()` 顶部加 `renderStreak()`（含后台切回重算）。
5. `app_views_accounts.js`：loadOverview 统计赋值从 `textContent` 换 `setCount`（sTotal/sHealthy/
   sCooling/sDisabled/sPaused/sCredits/sSticky/sTodayReq；sTodayTok 保留直接赋值——fmtTok 是小数
   带后缀，整数前缀滚动会制造「1.2k→1.5k」的假中间值）；`#statCredits` 写 `--ring` 百分比
   （totSum≤0 时留空，不臆造占比）。
6. 资源管线：`index.go` 加 `//go:embed gamify.css` / `//go:embed app_views_gamify.js` + 两个
   handler；`panel.go` routes() 加两条注册；两个文件登记进 `panelStyles`/`panelScripts` 清单
   （`app_views_gamify.js` 在 `app_views_tasks.js` 之后、`app_views_logs.js` 之前——满足
   TestComponentLoadOrder：app_views_* 必须在 app.js 之前）。
7. 测试：`gamify_test.go`（serving 全路由 + embed 非空 + 反查 index.html 不残留游戏化样式）；
   `security_test.go` 静态资源清单补两个新资源。

**非目标**：P1（成就带、趋势图平滑）与 P2（卡牌化+皮肤、叙事层）不在本 PRD；不做浏览器视觉
像素级验收（无 GUI 环境，交付说明披露）；不动批量按钮（btnCheckinAll 等，仅单账号 checkin 庆祝）。

## 验收（Given-When-Then）

- **F1** Given 面板任意统计卡（sHealthy 等）数据刷新，When loadOverview 赋值，Then 整数前缀在
  600ms 内滚动到目标值、后缀原样保留；`prefers-reduced-motion` 或环境无 `requestAnimationFrame`
  时直接落值（不滚）。
- **F2** Given 用户对单账号点「签到」，When 上游 checkin 成功，Then toast 带 ✓ 形符、`celebrate()`
  在屏幕上方撒 26 片彩屑（reduced-motion 下零 DOM）、`noteStreak()` 记当天。
- **F3** Given 任务中心「领取奖励」成功，When claim 分支执行，Then 同样触发庆祝 + 记当天；
  重复领取（已领取过，无新增）不额外庆祝。
- **F4** Given 添加账号成功，When addDone 提示出现，Then 庆祝 + 记当天（弹窗期间彩屑在最上层）。
- **F5** Given 任务中心打开，When `renderStreak()` 已执行，Then header 下方出现连续天数条：
  「连续 N 天」+ 最近 7 日圆点（有操作日高亮）+「本机记录 · …」标注；跨零点后首次动作自动
  续期，今天未操作则从昨天起算。
- **F6** Given 全部改动，When 跑 `scripts/pre-commit.ps1`，Then 退出码 0；深度棘轮
  `maxFrontendLines` 未回退（index.html 只留两个占位符，资源标签全部由清单生成，非空行 1844→1827
  收紧；`TestIndexHTMLHasNoHandWrittenAssetTags` / `TestIndexHTMLPlaceholdersReplaced` 兜底）。

## 红线自查

- 零依赖：未新增任何 go.mod 依赖，无外部库（纯 CSS/SVG/DOM）。✓
- 四态纪律：连续天数标「本机记录」（derivation），不冒充上游；`--ring` 在总额未知时留空。
- 反 AI slop：toast 图标用 SVG 形符不用 emoji；无紫渐变；颜色全用现有 CSS 变量。✓
- reduced-motion：全局规则（index.html:35）+ setCount/celebrate 双重降级。✓
- R8/依赖方向/R10：未触碰上游协议、依赖表、Go 业务代码。✓
- 深度棘轮：index.html 只留占位符（资源清单在 Go 侧一处维护），非空行 1844→1827 收紧、未回退。
