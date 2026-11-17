# 变更：后台 goroutine 崩溃隔离

- 日期：2026-11-17
- 分诊：L2（横切 15 个包，触及并发路径与依赖白名单）
- 基线：`main` @ `b2fb8c2`

## 问题（怎么发现的）

深审时按 R9「goroutine 必须有回收路径」逐个过了一遍全仓的 `go func`：生产代码 20 处，
**只有 3 处有 recover**（`reqlog`、`sse`、`chatrun` 的 SSE 分支）。其余 17 处跑在
后台、不在任何 HTTP 请求栈上——`net/http` 的 per-request recover 兜不住它们。

后果很具体：网关是 7×24 常驻服务，一个后台任务里的 nil 解引用（上游返回体字段缺失、
map 未初始化、切片越界）会**直接终止整个进程**，而不是只失败那一轮任务。

## 修法：一个叶子包 + 17 个接线点

新增 `internal/pkg/gosafe`（与 `internal/pkg/timex` 同级，零 internal 依赖）：

- `Go(name, fn)` —— 启动带防护的 goroutine（一步到位）
- `Recover(name)` —— 已有 goroutine 内 `defer` 用
- `RecoverWith(name, onPanic)` —— 记日志后把 panic 值交给调用方善后

**为什么保留两套 API**：现有 goroutine 都已有别的 `defer`（`wg.Done()`、`unlockAccount`、
`<-sem`），改成 `Go(...)` 要重排闭包结构；用 `defer Recover` 只加一行、改动面最小。
`Go` 留给将来新建的 goroutine。这不是"写了两套没人用"——`Recover` 是主力，
`Go` 是入口封装。

### 接线清单（17 处）

| 包 | 位置 | 点名 |
|---|---|---|
| scheduler | runBatch 四类任务 | `scheduler/runBatch/<kind>` |
| scheduler | 余额刷新 per-account | `scheduler/balance-refresh` |
| scheduler | 余额 ticker | `scheduler/balance-ticker` |
| pool | 落盘 flusher | `pool/flusher` |
| panel | 任务中心扫描 | `panel/scan-all` |
| panel | 队列准备 | `panel/taskcenter/prepare` |
| panel | 队列执行 | `panel/taskcenter/run` |
| panel | 校园券拉取 | `panel/school-vouchers` |
| panel | 一键完成流水线 | `panel/auto-all` |
| panel | 余额并发拉取 | `panel/credit-packages` |
| panel | 昵称同步 | `panel/sync-nicknames` |
| upstream | 模型探测 ×2 | `upstream/models-enterprise`、`/models-v3` |
| upstream | 全局模型探测 ×2 | `upstream/global-models/v3`、`/enterprise` |
| upstream | SSE 空闲监控 | `upstream/idle-watch` |
| usage | 用量落盘 | `usage/flusher` |
| session | GC ticker | `session/gc-ticker` |
| redisstore | 异步写 | `redisstore/async-write` |
| alerting | webhook 投递 | `alerting/webhook` |
| cmd/server | 优雅停机 | `server/shutdown` |
| cmd/server | key 用量告警 | `server/keyalert` |

### 三类不能只加 `defer Recover` 的地方

1. **channel 交回结果的（upstream 四处）**。`enterpriseCh` / `v3Ch` 是 `buffered(1)`，
   panic 后如果不补发值，接收端会**永久阻塞**——recover 救了进程却卡死请求。
   改用 `RecoverWith` 在 panic 时补 `probeResult{err: ...}`。

2. **持锁的（pool/flusher）**。原代码是 `Lock(); 收集; Unlock()` 三条语句，
   中间 panic 会让锁**永久留在持有态**，下一轮 ticker 自锁死、选号全线阻塞——
   比进程崩掉更难发现（没有崩溃日志，只是"网关不工作了"）。
   抽出 `flushOnce()`，锁放在闭包里 `defer Unlock`。

3. **有残留状态的（alerting/webhook）**。`n.last[key]` 是限流标记，
   panic 后不删掉的话该告警会被**永久限流**，后续同类事件静默不发。

## 附带处理

- **panel.go 拆出 `panel_syncnick.go`**（54 行）。理由不是"顺手重构"：panel.go 当时
  931 行正好压着 `maxFileLines` 棘轮上限，加防护要多两行就会 REGRESS。
  棘轮只许收紧，所以先把那段独立成文件腾地方，再加防护。**净结果 panel.go 931 → 880。**
- **`taskKind` 加 `String()`**。panic 记录里要能读出「哪类任务崩了」，
  没有它日志里只有一个 int。
- **依赖白名单 +4 条**：`panel`、`session`、`redisstore`、`alerting` 各加 `pkg`。
  理由是 `gosafe` 与 `timex` 同为纯叶子层——方向指向叶子 = 向内，不构成上行依赖。
  每条在 `check-boundaries.ps1` 里都写了引入理由。

## 覆盖率插曲（值得记）

首跑 `check-depth -Detail` 报 `minCoverage 78.4 → 53.8` REGRESS。**别急着 -Update**——
`minCoverage` 是「最低单包覆盖率」，53.8 是新增的 `internal/pkg/gosafe`（`RecoverWith`
完全没测）。补了 3 条测试（panic 传值、无 panic 不回调、nil 回调）后该包 **100%**，
全局回到 78.5。**这是"新增未测代码稀释覆盖率"的标准形态，修复方式是补测试。**

## 验证（实测）

```
go build ./...                              → 0
go vet ./...                                → 0
gofmt -l ./cmd ./internal                   → 无输出
go test ./... -count=1                      → 全部 ok
scripts/check-boundaries.ps1                → ✅
scripts/check-depth.ps1                     → ✅ maxFileLines 931→895、minCoverage 78.4→78.5
internal/pkg/gosafe -cover                  → 100.0%
```

新增测试 7 条（gosafe）+ 既有测试全绿。`-race` 由 CI 跑（本机无 gcc）。

## 未做 / 边界

- **HTTP handler 不加**：`net/http` 每请求自带 recover，重复加会掩盖真 bug。
- **`Go()` 目前零生产调用**：见上文「为什么保留两套 API」。若下轮仍无新调用点，
  可考虑删掉只留 `Recover`/`RecoverWith`——**留给下一轮判断，不在本轮顺手删**。
- **没做**：`panic` 后的**任务重试**。当前语义是「这一轮放弃」，下一轮排程照常。
  重试要设计幂等，属独立议题。

## 回滚

`git revert <本提交>`。`gosafe` 是新增包，删除即可，无数据影响。
