# 面板视觉扁平化（panel-visual-flat）变更摘要

> **历史记录 · 2026-11-20 注**：本记录中的「不叠渐变 / 不用彩色方块图标 / 按钮纯色」
> 三条已被 [ADR-028](../../docs/adr/ADR-028-panel-visual-direction.md) 部分取代；
> 霓虹、极光、渐变文字、指针光斑等禁令仍然有效。本文件其余内容保持当时原样。

分级：L1（纯展示层，无数据/协议/选号影响）
复杂度：A 单点（`internal/panel/index.html` 样式令牌与硬编码色值）

## 一、改了什么

参照 `taste-skill`（Leonxlnx/taste-skill v2）的 **app-screen 规则**——「quiet borders、小标签、
重复面板是常态；渐变、发光、彩色方块图标不是」——收敛仪表盘视觉，同时修掉一处真实色值漂移：

| # | 位置 | 旧值 | 新值 | 理由 |
|---|---|---|---|---|
| 1 | 设计令牌注释 | 「单一**靛蓝**强调」 | 「单一**绿**强调」 | 注释与实现漂移（accent 实为 #10b981 绿） |
| 2 | `--accent-gradient` | 135° 绿渐变 | **删除**，三处引用改 `var(--accent)` | 仪表盘不叠渐变 |
| 3 | `body` 背景 | 180° 绿→灰渐变 | `var(--bg)` | 去掉页面级装饰渐变 |
| 4 | `.nav a.on` | 硬编码 `#e8f5e9` / `#059669` | `var(--accent-soft)` / `var(--accent-2)` | 消除硬编码漂移 |
| 5 | `.nav a.on::before` | 硬编码 `#10b981` | `var(--accent)` | 同上 |
| 6 | `.acc-card:hover` 阴影 | `rgba(99,102,241,.15)`（**靛蓝**） | `rgba(16,185,129,.15)`（品牌绿） | **真实 bug**：品牌早已转绿，阴影仍是旧靛蓝 |
| 7 | `.acc-avatar` / `.acc-cred .bar i` | `var(--accent-gradient)` | `var(--accent)` | 同上，扁平化 |
| 8 | `button.primary` 及 hover | 渐变 + hover 阴影 `rgba(99,102,241,.5)` | 纯色 + 绿色阴影 | 同上 |

保留：`.stat.hero` 的柔和 tint（`accent-soft → transparent`）与骨架屏 shimmer——前者是层级手段、
后者是加载态语义，均不属于装饰性发光。

## 二、怎么验证的

- `go test ./internal/panel -count=1` → **ok 47.533s**（前端 bundle / UX / 覆盖率测试全过）
- `grep`：`accent-gradient` 仅剩注释里一句历史说明；`rgba(99,102,241` / `#e8f5e9` / `background: #10b981` 零残留
- 未做截图对比：本轮未起本地实例。**视觉需人工看一眼**（打开 /panel/ 对比账号卡片 hover 与主按钮）

## 三、回滚

单文件：`git checkout -- internal/panel/index.html`。无数据/接口影响。

## 四、未吸收的 taste-skill 规则（写明理由）

- **四张以上真实图片 / hero 动画 / 滚动场景**：那是落地页规则，运维面板不适用（信息密度优先）。
- **`inspect.mjs` 真机三宽度检查**：需要 Node 22 + Chrome，与「单二进制零依赖」定位冲突；等价能力用现有
  `e2e/tests/smoke.spec.ts`（Playwright）替代，不新增运行时依赖。
- **整包安装**：115k star 的 skill 有 100+ 条官网规则，塞进本项目即「屎山堆积」；只提炼
  app-screen 的禁令 + 「看截图判断」的方法论。
