# 2026-10-29-growth-split-stage2 — growth 包拆出代码落地（M4，ADR-018 A'）

> 状态：已实现并提交。解锁 REQ-growth-package-split 的②（六文件拆包 A' 方案）。
> 验收：go build ./... 通过；internal/upstream、internal/scheduler、internal/server
> 全量测试绿；internal/growth 新包构建/测试挂载正常；边界脚本 check-boundaries.ps1 通过。
> 决策依据：ADR-018-growth-package-split.md（e852fa4，候选 A/B/A' 对比）。

## 改了什么

- **新包 `internal/growth/`**（6 生产文件，逻辑从 upstream 逐字迁移）：
  - transport.go：`Transport` 接口（10 方法）+ `Error{Status,Msg}` + `Client{t Transport}`
    + `clientToken/ClientToken/InNightWindow`。
  - streak/tasks/travel/trial/blackcat/school.go：业务方法改委托 transport 接口，
    请求构造（路径/头/body）留在 upstream 实现侧（R8 报文零改动）。
- **upstream 侧**：
  - growth_transport.go：Transport 接口实现（DoChat/DoChatMP/DoBilling/DoWeb/DoSchool/
    DoReportMP/Stream/ReportActivity/SetNightSleep/NightSleep）+ `growthErr` 转 growth.Error
    + `wrapErr` 把 growth.Error 还原为 *upstream.Error（Kind=Classify 重算 + ErrClient 兜底，
    与 doJSON 分类口径一致——panel/scheduler/测试的 *upstream.Error 断言零改动）。
  - growth_stub.go：26 个转发存根（全部经 wrapErr）+ 类型别名 5（Task/SchoolVoucher/Buddy/
    TravelState/StreakFull）+ 私有别名 6 + 公开函数别名（InNightWindow/School* 事件构造等）
    + 常量（mpPlatform/schoolBase/tasksListPath/mpReportPath）+ `IsBuddyTaskIncomplete`
    独立实现（断言 *upstream.Error，与 growth 侧同值 marker）+ schoolJSON 保留本体
    + `growthClient()` sync.Once 惰性委托。
  - client.go：Client 加 `growthOnce sync.Once; gc *growth.Client` 字段 + import growth。
  - 删除 school/travel/tasks/streak/trial/blackcat.go 六文件。
- scripts/check-boundaries.ps1：allow 表加 `upstream → growth` 与 `growth → auth`（ADR-018）。

## 怎么验证的

- go build ./...（EXIT=0）；gofmt -l（零输出）；go vet growth+upstream（干净）。
- go test ./internal/upstream ./internal/scheduler ./internal/server（全绿；r8_golden_test
  / travel_test / coverage_sequences_test 均过——wrapErr 恢复 *upstream.Error 契约后
  全部原有断言恢复）。
- check-boundaries.ps1：✅ 通过（依赖方向无误）。
- **未纳入本提交的失败**：internal/panel 的 TestAppJSConfigDirtyGuard /
  TestAppJSRequestPagination（frontend_ux_test 对 app.js 的内容断言）——由**外部并发轮**
  （trends：internal/panel/app.js、panel.go、reqlog/trends.go 等未提交改动）引入，与本拆包
  无关，未触碰外部 WIP 文件。

## 怎么回滚

- 反向操作：删除 internal/growth/ 与 growth_stub.go/growth_transport.go、恢复六文件
  （git show <本提交>^:internal/upstream/school.go 等）、client.go 移除两字段、边界表还原。
- 或整提交 revert（纯代码移动 + 别名接线，无 schema/协议变化，可安全 revert）。
