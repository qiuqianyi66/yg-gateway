# 面板渲染打磨（第六批 / 2026-11-20）

## 改了什么

| 提交 | 内容 |
|---|---|
| `5802d81` | 运维 Agent 去内联样式（`.pad`+`.ops-body`+复用 `.acc`/`.us-empty`，h2→h3，按钮 `.xs`），并改成进视图即加载；请求表模型/账号列套 `.clip` 防换行；积分条 2px→1px。 |
| `f95c779` | 请求表 UA 列 300px 把表顶出卡片，改 `.clip.ua{190px}`。实测 `wrapW=1182 / tableW=1182`，末列右缘 1415 vs 卡片 1416。 |
| `02a234f` | 活跃账号线改 `--blue`；积分条去缝隙 + 图例空名兜底；hero 环轨道由 `--line` 改 ink-3 混色；`trends.js` 的 `--muted/--err` 换成真实令牌 `--ink-3/--bad`。 |
| `5308ab8` | 新增 `app_core.js acctLabel()`：昵称只有 `.` / 单个 emoji（本机 4 个号里 2 个）时回落 uid8，替换 8 个拼接点；趋势图柱宽 30→44、圆角 4；两个 node 沙箱补 stub。 |
| `6b5e83c` | 模型表能力徽标从独占第三行并入副标题行，行高实测 88→66px（18 行）；`gamify.css` 里 hero 环 `opacity .45` 把整环洗淡，提到 .78；连续天数未点亮的点改虚线空心；`app.js` 到期提醒改用 `acctLabel`。 |
| `1d3e327` | 配置页开关改用 Key 页同款药丸样式（原生方块 checkbox 与全站不一致），`#view-config .grid2` 列间距 20→28。 |
| `62a2d59` | 趋势图移除活跃账号系列：实测 acct=2–3 / req 最高 1128，同轴画线贴在基线上是假波动；改在标题给「活跃账号 2–3/日」。3 日均线恢复 `--accent-2`（图注写的是「深绿线」）；Token 图均值线改蓝匹配 `sw-t`。 |
| `26540e9` | 流量画像标签去重（「未识别 UA（未识别 UA）」自重复）。 |

## 怎么验证

- 每批 `powershell -NoProfile -File scripts/pre-commit.ps1` 全绿（11 项）后提交；`deploy-prod.ps1 -Build` 退出码 0，冒烟 3/3。
- 生产实测：请求表 `wrapW=1182/tableW=1182`；模型行高 66px；积分条 `gap=0px`、`segW=boxW=972`；趋势标题文案 `活跃账号 2–3/日`。
- 截图：`%TEMP%\panel-views-b5\*.png`（accounts/usage/packages/models/taskscenter/keys/config/logs/opsagent）。

## 被否方案 / 边界

- 指针跟随光斑、逐卡入场动画：撞 `app.js` 1368 行棘轮 / 引发 5s 轮询重播闪烁，不做。
- 趋势图给活跃账号加独立右轴：右轴已是成功率 %，双语义叠加会误导，改为标题给区间。
- `app.css` 曾因新增规则到 1003 非空行触发 `check-depth` 的 `oversizeFrontendFiles 1→2`，已压缩到 999；**不许 -Update 抬阈值**。

## 回滚

单提交 `git revert <sha>`；样式批次整体回滚到 `347f368`（第五批前）即可。
