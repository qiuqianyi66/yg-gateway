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

同批修两个实测渲染缺陷：

1. **请求表底部半行**：`max-height:460px` 落在 10.4 行处，底部永远半行被分页条压住。实测表头 34.3px + 行高 40.8px → 10 行整 = 442.4px，取 **440px**，截图为完整 10 行。
2. **积分骨架竖排**：`skelCards()` 产出的 `.skel-grid` 是 `.pk-grid`（flex）的单个子项，未给 `flex-basis` 时被压成 220px 宽，4 张骨架竖着堆。加 `.pk-grid > .skel-grid{flex:1 1 100%}` 后 4 张横排，与加载完成后的卡片布局一致。

`trends.js` 3 日均线由 `1.4px / opacity .55` 提到 `1.6px / .8`：图例写着「深绿线」，原透明度在浅底上几乎读不出来。

## 怎么验证

- `go build ./...` 通过；`go test ./internal/panel/...` 51.8s ok；`gofmt -l ./cmd ./internal` 无输出。
- 生产实例（7863，只注入新 CSS，不改服务）逐屏截图：`%TEMP%\panel-aes-prod3\*.png`（9 视图）；暗色 `%TEMP%\panel-aes-dark\*.png`。
- 实测：请求表 `clientH=460→440`、`rowH=40.8`、`theadH=34.3`，截图底部无半行；积分页加载态 4 张骨架横排；`/panel/aesthetic.css` 由测试实例正常 200 下发。
- 门禁 `scripts/pre-commit.ps1` 全绿后提交，`deploy-prod.ps1 -Build` 退出码 0 + 冒烟 3/3。

## 被否方案 / 边界

- **不可直接引入 react-bits / magicui / animate-ui / motion-primitives**：全部是 React + Framer Motion + Tailwind，本项目是 go:embed 纯 CSS/JS 单二进制零依赖；只蒸馏手法（1px 细边、光晕、骨架 shimmer、分区色相、渐进入场）。
- 指针跟随光斑、逐卡入场：仍需 JS 改动（app.js 1368 行棘轮）且 5s 轮询会重播闪烁，不做。
- 极光 / 3D 粒子 / 渐变文字 / 霓虹外发光：此前已明确排除，本轮不碰。
- `app.css` 仍是 999/1000 非空行，新视觉增量只能落新文件；**禁止 `-Update` 抬 `check-depth` 阈值**。

## 回滚

`git revert <sha>` 即可整体回退（新 CSS 为独立文件，删掉后其余样式不受影响）；接线四处（embed 变量 / `panelStyles` 一行 / handler / 路由）一并回退。
