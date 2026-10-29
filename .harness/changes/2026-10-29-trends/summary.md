# 2026-10-29-trends — 时间序列证据聚合（池健康度 / 选号 / 路由趋势）

> 状态：🔄 实现完成，待门禁提交（并行会话 growth 半成品占用共享 worktree，
> 全量 pre-commit 暂被阻塞）。改动范围：reqlog（聚合纯函数 + 全量读取）+
> panel（趋势端点 + 前端趋势块）。未触碰并行会话工作区（L-21）。

## 改了什么

1. **`internal/reqlog/trends.go`（新）**：`AggregateDaily(events) []DailyPoint`
   纯函数——按天（本地时区）聚合 requests/succeeded/failed/success_rate/
   active_accounts/prompt+completion tokens，时间升序。对齐 profile.go 的
   纯函数纪律（无 IO/时钟/全局态，测试直接构造 Event 钉死聚合逻辑）。
   四态：success_rate 是 derivation；0 请求天不产生点（unknown ≠ 成功率 0%）。
2. **`internal/reqlog/reqlog.go`**：`Recorder.ReadAll(filter)` + 
   `archiveWriter.readAll(filter)`——归档全量读取（无条数截断，时间升序）。
   理由：时间序列按 ReadArchive 的 1000 条截断会系统性丢早期数据（推导失真）。
   文件级粗筛与行过滤逻辑与 read 完全一致（结果集 = read 在 limit=∞ 时）。
3. **`internal/panel/trends.go`（新）**：`GET /panel/api/trends`（鉴权路由在
   panel.go routes() 注册）——ReadAll → AggregateDaily → 叠加 `Pool.List()`
   账号现态（credits/到期批次/健康度/最近成功），输出 `{days, accounts,
   window}`。R1：账号字段只取展示/健康/到期项，不含任何凭证。独立文件是
   check-depth 拆分产物（panel.go ≤1000 行棘轮）。
4. **前端（check-depth 拆分产物）**：`trends.js`（新，自包含 IIFE，对齐
   opsagent.js 独立嵌入模式——app.js 冻结在 maxFrontendLines 棘轮上限，新
   功能一律独立文件）：`loadTrends/renderTrends/renderTrendsAccounts`——双轴
   SVG 图（柱=请求量、折线=成功率仅观测天、点=活跃账号数）+ 账号现态表
   （last_success_known=false 显示「从未成功」）。自包含原因：跨文件依赖有
   顶层求值竞态 + TDZ 风险（frontend_test 有 node 语法/顶层求值闸门），且
   app.js 已冻结；trends.js 自带 esc/loadingMsg/loadErrorHTML/api，DOMContentLoaded
   挂按钮 + 监听 hashchange 按需加载，不依赖 app.js 全局。
   `index.go` 加 `//go:embed trends.js` + `trendsScript` handler；panel.go
   routes() 注册 `GET /panel/trends.js`；index.html 在 opsagent.js 后加
   `<script src="trends.js">`。app.js 的 `loadTrends()` 调用点（go('usage')
   与顶栏刷新）已移除，改由 trends.js 自行监听 hashchange 触发。

## 怎么验证的

- `go test ./internal/reqlog/ -count=1` 全绿（含新 `TestAggregateDaily*`、
  `TestReadAllFullWindow`、`TestReadAllAggregateDaily`）；`go vet ./internal/reqlog/` 0。
- `node --check` 语法校验 app.js + trends.js 均 0（frontend_test 的 node 闸门
  口径）；app.js 已无 loadTrends 引用（跨文件解耦确认）。
- check-boundaries 通过（依赖方向无违规）。
- gofmt：panel/reqlog 两目录输出为空（clean）。
- **受阻（客观阻塞，非本改动）**：`go build ./...` / `go test ./internal/panel/`
  / check-depth 均被并行会话的 growth 半成品挡住——`internal/growth/`（未
  跟踪）编译失败（streak.go undefined: auth 等），`internal/upstream/client.go`
  已 import growth（+6 行，未提交）。L-21 不触碰并行会话文件，等其收尾后
  补跑全量 pre-commit 再提交。

## 怎么回滚

- 代码回滚：`git revert` 本次提交即可（新增端点/函数随 revert 移除；
  前端区块随 revert 消失，无残留挂载点——trends.js 自包含、无跨文件引用，
  随文件回滚）。
- 运行期：本功能纯读（reqlog 归档 + Pool.List），无写路径、无状态变更、
  无新出站请求；下线不改变任何路由/选号/记账行为。

## 已知 unknown / 边界

- **历史存活曲线**：本版只给账号现态（当前快照，observation）。账号「从何时
  开始活跃/沉默」的历史曲线需要定期落盘快照才能合成——当前无该数据源，
  标 unknown，requires 后续轮次快照积累（如每日落盘 state.json 副本）。
- 活跃账号数口径：`active_accounts` 按 reqlog 的 Account 字段去重（观测），
  与 /status 的池总数可能不同（未产生请求的号不计入当日活跃，这是语义不是 bug）。
- ReadAll 数据量：受归档 retention（7 天）+ FileMaxBytes 上限约束，量级可控；
  面板每次进入用量视图现算，不做进程内缓存（数据小，现算最干净）。
