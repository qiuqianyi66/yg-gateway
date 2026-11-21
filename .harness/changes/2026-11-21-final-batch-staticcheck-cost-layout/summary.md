# 变更：staticcheck 清零 + 成本 tier 置信度 + 面板布局重排

- 日期：2026-11-21
- 分诊：L2（选号成本判定契约变更；其余为测试诊断清理与布局样式）
- 前置：`92e8741`

## 一、staticcheck 9 条清零（测试侧）

`go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./internal/...` 修复：

- `internal/bench/bench_test.go`：S1009 去掉 `map != nil && len>0` 的冗余 nil 判断。
- `internal/panel/coverage_handlers_test.go`：SA4006 删除被立即覆盖的 stub 首赋值。
- `internal/panel/coverage_autotask_test.go`：U1000 删除从未调用的 `caTaskState`。
- `internal/pool/modelcooldown_test.go`：S1024 改 `time.Until`。
- `internal/pool/transition_test.go`：SA4000 拆成三次独立调用与断言。
- `internal/pkg/gosafe/gosafe_test.go`：SA5000 加 `//lint:ignore`（有意写 nil map 制造 panic）。
- `internal/scheduler/activity_test.go`：U1000 删除 `noUserId` 与 `activityStreakScheduler`。
- `internal/upstream/coverage_parse_test.go`：ST1012 改名为 `errUpstreamparseTransport`。

复核：staticcheck 零输出，相关 6 包 `go test` 全绿。

## 二、成本 tier 置信度（行为变更）

问题：单条 `credit=0` 观测直接判 tier 0（免费），而 `credit` 字段缺失也会是 0。
上游已收费时，免费层垄断会把积分快速耗光。

改动 `internal/pool/pick.go` / `entry.go`：

- 新增 `minFreeCostSamples=3`：仅实测零扣费时，累计 3 条才判 tier 0；
  单条/两条留在 tier 1（unknown）。
- 目录显式倍率 `0` 作为第二证据源，可提前判免费；目录收费 + 实测 0 → tier 2。
- `catalogRate` 统一“未知 / 显式 0 / 收费”三态，缺失不再当 0。
- `floorBlockedForRealmModel` 同口径：单条 0 不再覆盖目录收费；
  累计 3 条后才按实测免费放行；未知仍放行（保留避免死锁的既有契约）。
- `modelCostTier` 从闭包提为方法，便于直接测试。

契约测试：新增 `TestModelCostTierRequiresRepeatedFreeEvidence`、
`TestCreditFloorWeakFreeEvidenceWithPaidCatalogBlocks`；原免费断言改用 3 条观测。

## 三、面板布局重排

前置拆分：`app.css` 已到 999/1000 非空行，新增 `internal/panel/views.css`
（Key 弹窗 / Toast / 用量 / 请求表 / 日志 / 渐进入场等尾部样式，493 行）。
`app.css` 保留设计令牌与基础组件（550 行）。同步 `index.go` embed/panelStyles、
`routes.go` 静态路由、CSP 静态资源测试。

布局改动：

- 侧栏 208 → 224px；导航项 9px/12px、图标 16px、品牌 16px。
- 顶栏标题 15.5 → 18px；视图内边距 24/26px。
- 账号网格显式列数：≥1360 四列、1080–1359 三列、760–1079 两列、<760 单列。
  1440 下 4 张卡单行排满，不再出现“第 4 张单独换行 + 右侧两列空白”。
- 卡片密度：头像 34px、内边距 15px、统计条间距/留白收紧；长账号名/UID 省略号。
- 统计条：普通值 30px、hero 38px、标签 11.5–12.5px，建立字号层级。

## 四、验证

- `go test ./internal/pool ./internal/panel -count=1`：ok。
- `go test ./internal/bench ./internal/panel ./internal/pool ./internal/pkg/gosafe ./internal/scheduler ./internal/upstream -count=1`：6 包 ok。
- `staticcheck ./internal/...`：零诊断。
- 隔离实例（7865，4 个假账号）Playwright 实测：
  - 1440：4 张卡同排，卡宽 273px、单行高 248px；
  - 1280：3 列（315px），第 4 张换行（预期）；
  - 768：2 列（224px）；375：单列（317px），无横向溢出；
  - 暗色 1440 卡片/统计条/侧栏正常。

## 五、生产换版与回归

`powershell -NoProfile -File scripts/deploy-prod.ps1 -Build -Feature cost-tier-layout`：

- 报告：`D:\yg-gateway\data\logs\deploy-prod-20261121-112507.json`
- `stage=done`、`git_head=daeb559`、`git_dirty=false`、健康 `healthy=2/4`
- 冒烟 3/3：`/v1/models` 18 模型；非流式 200；流式 200 + 增量帧 + `[DONE]`
- 备份：`D:\yg-gateway\yg-gateway.exe.pre-cost-tier-layout-20261121.bak`
  （旧 sha256 `3E7CF85D…`，新 `293CCF31…`）

生产回归 `python scripts/yg-regression-test.py --base http://127.0.0.1:7863 --skip S4,S8`：

- S1/S2/S3/S5/S6/S7：**14 PASS / 0 FAIL**
- S2 20/20 200；S7 SSE 5/5 完整；S3/S6 分别 413 边界正确
- S8 补跑：**1 PASS / 0 FAIL**，16/16 有响应（12×200、3×429、1 次超时观察），
  5xx=0、无线程挂死；S4 仍按设计不跑生产（会故意打冷账号）

生产面板 Playwright 实测：1440 账号卡 4 张同排（273px、单行 248px）；
1280 三列 315px；375 单列 317px；无 pageerror/console error；添加账号弹层样式正常。

成本台账生产抽样：12 条 `model_costs`，其中 3 条零观测 `samples=1`
（`hy4flash-free` ×2、`space-bunny` ×1）。`hy4flash-free` 目录优惠倍率显式 `0`，
新旧规则都判 tier 0；`space-bunny` 目录 `0.08`，新旧规则都判 tier 2——
本轮变更未在这两条上产生未预期的免费层丢失。

## 六、回滚

- 成本 tier：回滚 `internal/pool/pick.go`、`entry.go` 与对应测试；`state.json` 无迁移。
- 面板：回滚 `app.css` / `views.css` 与 `index.go` / `routes.go` 资源清单即可；
  两份 CSS 拆合为纯机械搬移，无数据影响。
- 生产换版失败按 `deploy-prod.ps1` 退出码处理（2 已自动回滚；4 需看 stderr）。
