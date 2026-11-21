# 面板视觉层（分区色相 / 渐进入场 / 毛玻璃顶栏 / 2026-11-20）

## 改了什么

新增 `internal/panel/aesthetic.css`（独立样式层，排在 `app.css` 之后、`panel.css` 之前）：

| 项 | 内容 |
|---|---|
| 分区色相 | 9 个视图各一支 `--sec`：账号池绿 `#10b981`、用量蓝 `#3b82f6`、积分紫 `#8b5cf6`、任务琥珀 `#f59e0b`、模型青 `#06b6d4`、Key 靛 `#6366f1`、配置灰蓝 `#64748b`、日志天蓝 `#0ea5e9`、运维 Agent 石板 `#475569`。只作用于标题方块 / 卡片顶线 / 悬停描边 / 表头微染，主按钮与 logo 仍是品牌绿。 |
| 卡片层级 | 顶部 1px 渐变色相线；hover 描边走分区色 + 一层 12px/28px 阴影（含 `inset 0 1px 0 var(--hair)` 防 hover 时抽掉高光）。卡片头 6% 色相渐变底 + `h3` 前 5px 方块。 |
| 顶层 | `.topbar` 改用已有 `--glass` 令牌 + `backdrop-filter: blur(14px) saturate(1.12)`（亮暗自动跟随）；`.view` 顶部 260px 铺 9% 色相 radial wash，压住背景点纹的均匀感。 |
| 渐进入场 | 只挂在 `.view:not([hidden])` 与 `.view > .box`（index.html 静态节点）：切视图整页 260ms 淡入，卡片 400ms 上浮并 40ms 递增延迟。5s 轮询重绘的是卡片**内部**内容，不会重播——这是本轮与第六批「逐卡入场」被否方案的关键区别。`prefers-reduced-motion` 下全部退化为静态终态。 |
| 表格 | `thead th` 4% 色相微染；行 hover 5% 色相；细滚动条（`scrollbar-width: thin`）。 |
| 交互 | 按钮按下 1px 位移；`.primary` 竖向轻渐变；全站 `:focus-visible` 2px 焦点环（键盘可见）。 |
| 第二轮加强 | `--sec` 改挂在 `body:has(可见视图)` 上（一次定义全页继承，含顶栏）；顶栏底部 2px 强调线随视图变色；视图 wash 提到 13%/7% 双层径向；卡片头染色 6%→10% 并把分隔线也掺 16% 色相。低于 105 的浏览器无 `:has` 时回落品牌绿。 |
| 加载指示 | 新增 `app_progress.js`：包一层 `window.fetch`，只统计 `/panel/api/*` 的并发请求，`.topbar.is-loading` 期间顶部扫一条 3px 色相线（`loadSweep`），停止后延迟 260ms 熄灭防闪烁；不追踪 `/v1/*` 流式代理，不改变 fetch 返回/错误语义。`prefers-reduced-motion` 下改为定格中段。 |
| 第三轮 | 账号卡状态轨：`renderAccountsUnsafe` 把原来统一的 `off` 拆成 `st-dis`（禁用）/`st-pause`（暂停）/`st-cool`（冷却），左缘 3px 色条分别取 red/amber/blue，健康卡不加轨；主指标 `#sHealthy`/`#sCredits` 数字按分区色相着色（82% sec + 18% ink，保留对比度，语义类 good/warn/bad 优先）。 |
| 第四轮 · 窄屏 | `≤760px`：`.nav` 子项 `flex:none` + `white-space:nowrap`，行模式下不再被压成逐字竖排；导航条 `max-height:56px` 并收窄 brand/sig 内边距，横向溢出交给 `.nav` 自身滚动。`≤560px`：`.topbar` 换行、`.view` 降 padding。 |
| 第五轮 · 悬停 | 注册 `@property --sec`（`<color>`、inherits），`body` 上 `transition: --sec .35s`，切视图时分区分色相平滑插值而非硬切；次级按钮 / 图标按钮 hover 改中性描边 + 4% 浅底（品牌绿只留主按钮，危险按钮仍红）；请求表可点击表头与积分分组开关 hover 跟随分区色相。`prefers-reduced-motion` 下取消 `--sec` 过渡。 |
| 第六轮 · 弹层 | `.veil.on` 遮罩 180ms 淡入，`.dlg` 260ms 上浮 10px + 1.5% 缩放；`.veil` 从 `display:none` 切到 `flex` 自动重播，不需改 `showVeil`。`prefers-reduced-motion` 下取消。 |
| 第七轮 · 头部溢出 | 1280 下「积分到期提醒」头部说明过长，把「检查」按钮顶出 `.box`（实测 header `scrollW=1033` / `clientW=1022`，裁 11px）。`.box > header .note` 加 `min-width:0 / overflow:hidden / text-overflow:ellipsis`，修后 `scrollW=clientW=1022`、按钮右缘 1238 < 头部 1255。 |

窄屏空白带的根因与修法（实测记录）：

- 375×820 下 opsagent 页内容仅 669px，`.shell{min-height:100vh}` 的 grid 把剩余 150.5px 均摊给 auto 行，导航条被拉成 163px 高、topbar 从 y151 才开始（accounts / logs 内容超过视口，行高正常 56px，所以两页表现不同）。加 `.shell{align-content:start}` 后三页行高都回到 56px，topbar y=56。
- 375 下账号页原「账号池」标题、导航项、按钮逐字竖排，原因是 flex 收缩；`flex:none` 后标题单行、按钮成行。

同批修两个实测渲染缺陷：

1. **请求表底部半行**：`max-height:460px` 落在 10.4 行处，底部永远半行被分页条压住。实测表头 34.3px + 行高 40.8px → 10 行整 = 442.4px，取 **440px**，截图为完整 10 行。
2. **积分骨架竖排**：`skelCards()` 产出的 `.skel-grid` 是 `.pk-grid`（flex）的单个子项，未给 `flex-basis` 时被压成 220px 宽，4 张骨架竖着堆。加 `.pk-grid > .skel-grid{flex:1 1 100%}` 后 4 张横排，与加载完成后的卡片布局一致。

`trends.js` 3 日均线由 `1.4px / opacity .55` 提到 `1.6px / .8`：图例写着「深绿线」，原透明度在浅底上几乎读不出来。

## 怎么验证

- `go build ./...` 通过；`go test ./internal/panel/...` 51.8s ok；`gofmt -l ./cmd ./internal` 无输出。
- 生产实例（7863，只注入新 CSS，不改服务）逐屏截图：`%TEMP%\panel-aes-prod3\*.png`（9 视图）；暗色 `%TEMP%\panel-aes-dark\*.png`；第二轮 `%TEMP%\panel-aes-v2\*.png`。
- 加载指示在隔离实例（7864）用 Playwright 延迟 1.2s 的 `/panel/api/*` 验证：请求中 `is-loading=true`、截图见顶栏 3px 扫线；2.2s 后 `false`。截图 `%TEMP%\panel-progress2\loading.png`。
- 生产实测（`4bb2daf` 上线后）：`#accBody` 4 张卡里 2 张为 `acc-card off st-pause`，暂停轨 `::after` = `rgb(245,158,11)`；`#sHealthy` 数字 = `color(srgb 0.0698 0.6196 0.4501)`（分区绿）。截图 `%TEMP%\panel-aes-live3\accounts.png`。
- 实测：请求表 `clientH=460→440`、`rowH=40.8`、`theadH=34.3`，截图底部无半行；积分页加载态 4 张骨架横排；`/panel/aesthetic.css` 由测试实例正常 200 下发。
- 门禁 `scripts/pre-commit.ps1` 全绿后提交，`deploy-prod.ps1 -Build` 退出码 0 + 冒烟 3/3。
- 生产实测（`70c82b1`，报告 `deploy-prod-20261120-220305.json`）：375×820 下 accounts / opsagent / logs 三页均 `navH=56`、`topY=56`、`overflow=0`；768 / 1024 均为侧栏 `navH=820`、`overflow=0`。截图 `%TEMP%\panel-mobile2\prod375-*.png`。
- 第五轮注入预览实测：`--sec` 在切页后 0/120/240/400ms 依次为 `rgb(16,185,129) → rgb(44,149,206) → rgb(57,132,242) → rgb(59,130,246)`，插值生效；模型页顶栏图标 hover 描边 `color(srgb .707 .730 .739)`（中性灰），账号卡 ghost 按钮同值。
- 第六轮注入预览实测：点击「添加账号」后立即采样 `.dlg` = `dlgIn` / `matrix(.985,0,0,.985,0,10)` / `opacity:0`，400ms 后 `matrix(1,0,0,1,0,0)` / `opacity:1`；`.veil` 同步跑 `veilIn`。
- 暗色 9 视图与焦点核对（生产）：9 张截图 `%TEMP%\panel-final\dark-*.png` 无对比度缺陷；Tab 焦点环 `2px solid rgba(16,185,129,.62)`；`overflow=0`；`emulateMedia({reducedMotion:'reduce'})` 下 `.view` `animationName=none`、body `transition=none`。

## 被否方案 / 边界

- **不可直接引入 react-bits / magicui / animate-ui / motion-primitives**：全部是 React + Framer Motion + Tailwind，本项目是 go:embed 纯 CSS/JS 单二进制零依赖；只蒸馏手法（1px 细边、光晕、骨架 shimmer、分区色相、渐进入场）。
- 指针跟随光斑、逐卡入场：仍需 JS 改动（app.js 1368 行棘轮）且 5s 轮询会重播闪烁，不做。
- 极光 / 3D 粒子 / 渐变文字 / 霓虹外发光：此前已明确排除，本轮不碰。
- `app.css` 仍是 999/1000 非空行，新视觉增量只能落新文件；**禁止 `-Update` 抬 `check-depth` 阈值**。

## 回滚

`git revert <sha>` 即可整体回退（新 CSS 为独立文件，删掉后其余样式不受影响）；接线四处（embed 变量 / `panelStyles` 一行 / handler / 路由）一并回退。
