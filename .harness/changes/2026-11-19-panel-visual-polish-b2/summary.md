# 2026-11-19 面板视觉第二批：统计条走形 / 积分环假象 / 空池误报 401

## 发现（四类，前三类有像素证据，第四类是读代码 + curl 实证）

1. **统计条 7 列塞 9 个指标**：第一行两张主指标各占 1 列，第二行 7 个次要指标只填满
   剩余空白的一半，右侧留出一整片无内容白块；主/次指标高度也不齐。
2. **`.hero-ring` 从来没实现**：JS 一直在赋 `--ring`，CSS 里没有任何 `conic-gradient`，
   环永远不画；更糟的是总额未知时 JS 把占比算成 0——把「未知」渲染成「积分用完」，
   违反项目核心纪律（unknown ≠ 0）。
3. **多处正文用 `--ink-3`(#9aa8b5)**：白底对比度约 2.5:1。统计标签、表头、空态文案、
   图表刻度、左栏版本行几乎看不清。
4. **空账号池被报成「读取账号池失败 / 密钥无效或未填写」**（实机截图复现）。根因是
   两个缺陷叠加：
   - `loadOverview` 的 catch 把错误条**绕过 `accRenderSig`** 直接写进 `accBody`，且用
     `<tr><td colspan="9">` 包一个 `<div>`（`accBody` 是 `.acc-grid`，非法表格行）。
   - `accountsSig([])` 返回**空串**，`accRenderSig` 初始值也是空串 → 登录前那次 401
     留下的错误条，在登录后空池成功渲染时撞上「数据没变就不重建」的短路，**永久留存**。
   健康实例因此看起来像鉴权坏了。

## 改了什么

`internal/panel/app.css`

- `.stats` → `repeat(7,1fr)`；新增 `.stat.span-4` / `.stat.span-3`，配合 HTML 让两张主
  指标 4+3 占满第一行、7 个次要指标占满第二行；响应式重写（≤1180 span 2 / ≤980 span 3 /
  ≤560 span 2）。
- 真做 `.hero-ring`：`::after` + `conic-gradient(var(--accent) var(--ring,0%), var(--line))`
  + `mask: radial-gradient` 中空环；`[data-unknown="1"]` 时整环 `display:none`。
- 对比度：`.stat .k`、`.box>header .note`、`.box h3 .hint`、`.acc th`(500→600)、
  `.empty`、`.uschart-body .tk`、`.uschart-hd .legend`、`.load-error .detail`、`.nav .sig`
  统一提到 `--ink-2`。
- `.uschart-body` 97→72→48px；`:empty` 兜底「暂无数据」(64px)；`.us-empty` padding 30→10、
  文字提 `--ink-2`；新增 `.pad:has(.uschart-body:empty)` 收掉空图表卡的上下一圈 17px 留白。

`internal/panel/index.html`

- 两张主指标加 `span-4` / `span-3`。
- 模型页 `<h3 class="hint">` 里泄漏的开发备注（「max_tokens bug已修复…不保证完全正确」）
  改成产品口径「max_tokens 原样透传；上游返回的最大输出仅供参考」。

`internal/panel/app_views_accounts.js`

- `statCredits`：`totSum>0` 才写 `--ring` 并清 `data-unknown`；否则 `removeProperty('--ring')`
  + `data-unknown="1"`（未知不再伪装成 0）。
- catch 分支：`accRenderSig = null`（让下一次成功渲染强制重画）+ 错误条改为直接写
  `loadErrorHTML(...)`，去掉非法 `<tr><td colspan="9">` 包裹。

`e2e/scripts/shot-views.mjs`（新增）

- Playwright 逐视图截图（1440×900、light、登录取 `e2e-test-key`，`SHOT_DIR` 指定输出），
  用于改造前后像素对比。只截首页，不跑交互断言。

## 怎么验证

| 验证 | 结果 |
|---|---|
| 改造前截图 | `%TEMP%\panel-before\*.png`（10 视图） |
| 改造后截图 | `%TEMP%\panel-after\*.png`：统计条两行干净、无白块、无幽灵环；空池显示「账号池是空的 / 点击右上角「添加账号」…」；用量页三张空卡收窄 |
| `/panel/api/accounts` | 404（该接口不存在，属误探路径，不是缺陷） |
| `/panel/api/overview` 带 key | 200，`accounts:[]` |
| `/panel/api/usage?hours=24` 带/不带 key | 200 / 401 `invalid_api_key` |
| `npm test`（e2e 面板冒烟） | 4 passed / 32 断言，无 JS 报错 |
| `scripts/pre-commit.ps1` | 11/11 全过，exit 0（go test 28 ok、gofmt 无输出、边界/体量/治理/预算/R6） |

## 未验证 / 观察

- **未知总额下的环**只在隔离实例（0 账号）看过「不画环」，**没有在「有账号但总额未知」的
  真实数据上验证**；那是一份混合态，需要生产账号配合。
- 深色主题只改了 token 级对比度，**未逐屏截图核对**。
- `.load-error .detail` 的对比度提升未单独截图量测，靠同批规则一致性。
- 生产实例有账号，本次改动在生产的「有数据」路径上靠部署冒烟覆盖，空池路径只在 e2e
  实例覆盖。

## 回滚

`git checkout -- internal/panel/app.css internal/panel/app_views_accounts.js internal/panel/index.html`
并重新构建替换（`.hero-ring` 回到不显示、错误条回到可残留状态；无数据迁移、无配置变化）。
