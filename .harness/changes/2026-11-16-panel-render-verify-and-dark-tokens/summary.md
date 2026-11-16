# 变更：面板渲染真实验证 + 补齐暗色令牌

- 日期：2026-11-16
- 分诊：L1（纯前端样式 + 测试；无对外契约、无后端逻辑变化）
- 前置：`472fef4`

## 背景：为什么还要这一轮

上一轮（`fc54879`）把 `index.html` 的 804 行内联 `<style>` 拆成 `app.css` / `panel.css`，
当时的验证只有字符串断言 + HTTP 200。**没有实际渲染验证**——层叠顺序对错、暗色是否还活着，
字符串断言都看不出来。本轮补的就是这件事。

## 验证链（四层，逐层加强）

| 层 | 做法 | 结论 |
|---|---|---|
| 1. 字节等价 | `git show 36bd45e:internal/panel/index.html` 提取两段 `<style>` 体，与 `app.css`/`panel.css` 的尾部逐字节比对 | **等价**（新文件 = 头部注释 + 原块原文；app.css 52888B / 原 52544B，差值即注释） |
| 2. 真实 HTTP | 起临时实例（免鉴权，隔离端口 7868），`GET /panel/` + 四个 CSS | 页面 200/49KB；四个 `<link>` 存在且顺序递增；两个新 CSS 200 + `text/css`（59118B / 2714B）；CSP 含 `style-src 'self'` |
| 3. 浏览器实机 | 豆包浏览器（bu 平面）打开面板，观察首页 / 用量 / 配置三个视图 + 切换主题 | 骨架屏、provider 徽章、KPI 色条、表格、底部固定操作栏全部正常；console 仅 401（未登录）与 favicon 404，**无 CSS 加载错误** |
| 4. 机器断言 | 新增 3 条 Go 测试（见下） | 全绿 |

## 发现：暗色主题一直是空的（🟡，本轮修掉）

验证第 3 层时点「切换主题」：`data-theme` 变成了 `dark`，但 `--bg` 仍是 `#f0f4f3`（亮色值），
页面视觉零变化。查代码：

- `app.js` 的三态切换（light / dark / auto + `prefers-color-scheme` 跟随）写得完整，
  切的是 `<html data-theme>`；
- 但**所有 CSS 里没有任何暗色令牌块**——`dark|prefers-color-scheme` 全目录只有
  `app.css:757` 一条局部 `color-scheme: dark`；
- `app.css` 开头的注释却写着「明暗两套独立色板」。

即：**按钮是死的，且不报错**。属本仓最怕的「不报错的失效」，不是本轮重构引入的（`36bd45e`
的字节里同样没有暗色块），是历史遗留。

### 修法

1. `app.css` 补 `:root[data-theme="dark"]`（特异性 0,1,1 高于 `:root`，无需 `!important`）：
   翻中性层（`--bg/--surface/--surface-2/--raise/--line/--line-soft/--ink/--ink-2/--ink-3/--shadow`），
   语义色（accent/ok/warn/bad）**显式重声明**并沿用亮色值——不靠「不写即继承」，否则新增变量
   时的漏项无法被测试发现；`soft` 层在深底上从 `.12` 提到 `.16` 以保住可见度。
2. 两处硬编码 `#ffffff`（`.nav` 侧栏、`.topbar` 顶栏）改为 `var(--surface)`——否则暗色下
   会出现两块白板。这是全部样式里**仅有的**两处致命硬编码；其余 `rgba(0,0,0,.x)` 都是阴影，
   深色下不可见但不刺眼。

### 新增测试（3 条，都有牙齿）

| 测试 | 钉住什么 | 牙齿 |
|---|---|---|
| `TestPanelStyleLinksRenderedInCascadeOrder` | 四个 `<link>` 在**下发页面**里的先后（CSS 优先级由它决定），而非只在清单里 | 互换清单里 panel.css 与 gamify.css → 红 |
| `TestPanelStylesExternalized` | `index.html` 无内联 `<style>`；两个新 CSS 嵌入了且登记进 `panelStyles`；app.css 首位 / panel.css 末位 | 抄回一段 `<style>` 或漏登记 → 红 |
| `TestDarkThemeTokensComplete` | 暗色块覆盖亮色 `:root` 的每个变量（`--mono/--sans` 除外，同元素继承） | 往 `:root` 加一条变量而不加进暗色块 → 红（本轮就是被它当场抓出缺 10 个语义色） |

## 实机证据

- 切 dark：`--bg = #12161a`、`body bg = rgb(18,22,26)`、`nav/topbar = rgb(23,28,33)`，截图无亮块
- 切回 light：`body bg = rgb(240,244,243)`、`nav = rgb(255,255,255)`，恢复原样
- 临时实例已停止，临时目录已删，**生产 7863 全程未被触碰**（PID 35508 仍在）

## 回滚

`git revert <this-commit>` 即可；纯 CSS + 测试，无数据/配置影响。

## 未做

- `--accent` 等语义色在暗色下沿用亮色值（不做提亮），所以深色下的绿色按钮与亮色一致；
  若后续要更「夜间化」，是审美决策，需单独定色板。
- 暗色下的 `rgba(0,0,0,.2~.6)` 阴影（约 15 处）未改为随主题变化的变量；深色下它们不可见，
  不产生视觉错误，仅为层次感弱一点。
- `localStorage` 的主题键名未核实（`yg-theme`/`theme` 都读不到）——JS 侧能读写，
  说明键名是第三个常量；不影响功能，未深究。

## 工作区状态提醒

提交时工作区另有他人未提交的改动（`docs/adr/ADR-018-growth-package-split.md`、
`docs/归档精化-2026-11-13.md`），**未纳入本次提交**，按纪律保留原状。
