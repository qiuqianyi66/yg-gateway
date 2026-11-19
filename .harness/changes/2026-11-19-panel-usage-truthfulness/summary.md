# 2026-11-19 用量页「说真话」三修（KPI 分色 / 三卡不发请求 / 趋势图轴与图例）

## 1. KPI 一排同绿 → 按指标分色

`usKpi` 里请求数与总 Token 都传 `c-accent`，四张卡视觉上等于没有层次。改为
请求数 `c-blue`、总 Token `c-violet`；成功率与延迟**仍走语义色**（`c-ok`/`c-warn`/`c-soft`），
不把「好/坏」语义塞进蓝紫。`app.css` 新增 `.kpi.c-blue` / `.kpi.c-violet`（只改 `--kpi-c` 与 `.v`）。

## 2. 点导航进用量页，三张数据卡永不加载（真 bug）

现象：生产实例 `/panel/api/trends`、`/agent_profile`、`/benchmark` 三个接口都 200 且
有数据（3194 条事件），但卡片全显示「暂无数据」，且浏览器**根本不发这三个请求**
（Playwright 采集：`trChart` innerHTML 长度 0，API 列表里没有 trends）。\]

根因：`app.js` 导航走 `history.pushState` 改 hash，**pushState 不触发 `hashchange`**；
而 `trends.js` / `agent_profile.js` / `bench.js` 只挂 `hashchange` + 首屏 hash，
所以正常点击导航进入用量页时 `maybeLoad()` 永不执行。只有前进/后退或手改地址栏才会加载。

修法：三处各补一个**捕获阶段** click 监听，命中 `a[data-view="usage"]` 后
`setTimeout(maybeLoad, 0)`（此时 pushState 已完成、`location.hash` 已更新）。
不改 `app.js`（1368 行棘轮卡死）。`app_core.js` 不能放：`TestAppCoreHasNoDuplicateInApp`
禁止该文件出现 `document.`。

验证：Playwright 实测三个请求 200，`trChart=3085 / agProfileHost=1933 / benchHost=3247` 字符。

## 3. 趋势图四处失真

| 缺陷 | 根因 | 修法 |
|---|---|---|
| X 轴 6 天只显示 1 个日期 | `const target = t0 + span * k`，`t0` 是 Date，`Date + number` 走**字符串拼接** → `pts[i].t - target` 得 NaN → 最近点全落回索引 0，`used` 去重后只剩一个标签 | 时间轴统一毫秒：`+pts[i].t`、`+t0` |
| Y 轴数值被首根柱压住 | 柱心落在 `PL`，柱左边缘在 `PL-15`，而刻度右对齐在 `PL-6` | 绘图区左右各让 16px（`I0=PL+16`、`I1=W-PR-16`），网格线同步 |
| 图例颜色说谎 | 实际画法是「成功率=琥珀曲线（右轴 %）、活跃账号=绿点」，图例 `sw-c` 绿 / `sw-t` 琥珀正好相反 | 按实际画法改正 `sw-c=var(--warn)`、`sw-t=var(--ok)` |
| 柱内出现白痕 | 柱 `opacity=.78`，底下网格线透出来 | 柱 `opacity=.95` |

## 验证（全部实跑）

- `scripts/pre-commit.ps1` 11/11 exit 0（每次提交都跑）
- e2e 冒烟 `npm test`：4 passed / 无 JS 报错
- 生产部署 `deploy-prod.ps1 -Build`：`trends-chart-axis` exit 0、冒烟 3/3
- 生产实拍：`%TEMP%\panel-prod-final\{accounts,usage,chart}.png`
  —— X 轴 5 个日期标签、Y 轴 1128/846/564/282/0 无遮挡、图例三色与实际一致、柱体干净

## 未验证 / 残余

- 「11-16」这一天因是均值跳过，X 轴不一定每天都有标签（6 点只出 5 个），属设计取舍。
- 活跃账号与请求量图例同为绿色（一个柱一个点），色弱用户仍可能混；未改。
- 深色主题下这三张卡未逐屏核对。

## 回滚

`git revert` 本批提交（`e3e5520`、`be09fab`、`bb832b4`、`ad578e5`、`c5bb00c`）后重新
`deploy-prod.ps1 -Build`；纯前端（CSS/JS），无数据迁移。
