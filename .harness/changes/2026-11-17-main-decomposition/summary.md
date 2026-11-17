# 变更：main() 装配外移（最后一个超长函数清零 + 最大文件从 cmd/server 让位）

- 日期：2026-11-17
- 分诊：L1（纯内部重构，无对外契约变化；启动行为、defer 顺序、日志文案逐位不变）
- 前置：`554f04f`

## 为什么

`cmd/server/main.go` 的 `func main()` 是全仓最后一个超 120 行的函数（**228 代码行**），
同时 `main.go` 本身就是最大文件（942 非空行，与棘轮零余量）。它把五类事情串在一个栈帧里：

1. **配置准备**：flag / Load / 首次运行自动生成 / 暴露安全闸；
2. **核心装配**：账号扫描 → pool → redisstore → 会话粘性 → upstream → scheduler → live → usage；
3. **观测装配**：延迟 / TTFB / 错误分类 / 放大率 / 慢请求 / 未知注册表；
4. **HTTP 面装配**：panel + handler + 漂移观测器 + 护栏校验（含 F6 晚绑定闭包）；
5. **启动与收尾**：信号 / 后台任务 / serveHTTP / 五个 defer。

删除测试：这些段各自内部强内聚、相互只通过已构造部件连接；切出去后 `main()` 只剩
「读配置 → 装配 → 起服务」的主干，复杂度是集中到各自文件的单一职责，不是搬家。

## 改动

| 文件 | 承载 | 非空行 |
|---|---|---|
| `cmd/server/main.go` | `main()` 只剩主干（约 100 代码行）；其余既有函数不变 | 942 → **741** |
| `cmd/server/main_prepare.go`（新） | `prepareConfig`：加载 / 首次运行生成 / 暴露安全闸 | 44 |
| `cmd/server/main_observability.go`（新） | `observability` 结构体 + `buildObservability` | 41 |
| `cmd/server/main_scheduler.go`（新） | `buildScheduler`（含任务横幅） | 35 |
| `cmd/server/main_http_runtime.go`（新） | `httpRuntimeDeps` + `buildHTTPRuntime`（panel / handler / 漂移观测器） | 162 |
| `cmd/server/main_session.go`（新） | `buildSessionRouter`（router + 两个只读闭包 + redisMode） | 45 |
| `cmd/server/main_split_test.go`（新） | 6 条测试覆盖上述新函数 | — |

**defer 顺序保持原样**（LIFO 执行序 = `requestLog.Close` → `rec.Stop` → `sessRouter.StopGC`
→ `store.Close` → `stop`），全部留在 `main()`；被切出的函数都不注册 defer，清理责任不外溢。

## 数字

| 指标 | 前 | 后 |
|---|---|---|
| `main()` 代码行 | 228 | 约 100 |
| 全仓 >120 行函数 | 1 | **0** |
| `oversizeFuncs`（棘轮） | 1 | **0** |
| `main.go` 非空行 | 942 | **741** |
| `maxFileLines`（棘轮） | 942 | **931**（现最大是 `internal/panel/panel.go`） |
| 最低包覆盖率（棘轮） | 75.5% | **78.4%** |
| `cmd/server` 包覆盖率 | — | **79.3%** |

## 验证

1. `go build ./...` = 0；`go vet ./cmd/server` = 0；`gofmt -l` 无输出
2. `go test ./cmd/server/ -count=1` 全绿（含 6 条新增测试）
3. **覆盖率修复（不是放宽阈值）**：拆分后首跑 `check-depth` 报
   `REGRESSED: minCoverage 75.5% → 74.9%`——新增 300 余行未被执行的代码稀释了包覆盖率。
   补 `main_split_test.go`（`prepareConfig` 存在/首次运行两条路径、`buildObservability`
   六字段与未知注册表接线、`buildSessionRouter` 开/关两态、`buildScheduler` 构造）后
   `cmd/server` 达 79.3%，全局最低回到 78.4%，才执行 `-Update`。脚本明令「严禁通过放宽阈值
   或 -Update 掩盖回退」，本次是按序执行的：先补测 → 复查 → 再抬棘轮。
4. **牙齿验证（关键）**：把 `buildSessionRouter` 的 `store.(redisstore.Noop)` 类型断言
   改成恒真（`if true { redisMode = "upstash" }`），`TestBuildSessionRouterDisabled`
   **立即 FAIL**（`main_split_test.go:113: redisMode = "upstash", want noop`）；
   改回后立即绿。
5. 棘轮：`check-depth -Update` 输出三条 `+ maxFileLines 942 → 931` /
   `+ oversizeFuncs 1 → 0` / `+ minCoverage 75.5% → 78.4%`。

## 回滚

`git revert <this-commit>`；纯包内重构，无数据/配置/接口影响。启动语义（含首次运行
自动生成配置、暴露安全闸拒绝启动、F6 晚绑定闭包、guardrails fail fast）逐位未动。

## 主动未做

- 未拆 `internal/panel/app.js`（1,640）：check-depth 脚本原注释写明「本轮只纳入度量 + 冻结基线，
  不拆文件：没有浏览器级 E2E 兜底，风险大于收益」，本轮沿用该结论。
- 未动 `buildHTTPRuntime` 内部顺序：panel → 日志出口 → 漂移观测器 → 护栏校验 → handler
  是语义顺序（handler 持有 Panel 引用、护栏须在 handler 前 fail fast），拆分时逐位保留。
- 未改任何权重/选号/聚合算法本身。
