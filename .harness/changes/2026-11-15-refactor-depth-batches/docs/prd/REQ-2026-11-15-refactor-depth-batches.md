# REQ — 三处深度债清理（度量假阳性 / config 归一化 / SSE 流状态所有权）

## 必要性

三处都是「改一改就更好、但没人被拦着」的存量债，各有可测的证据：

1. **度量假阳性**（`scripts/check-depth.ps1` 度量 3）：函数体按「本行 `^func` 到下一个
   `^func` 的前一行」界定，于是两个函数之间的 `type`/`const`/`var` 声明被整段算进**上一个**
   函数。实测 `internal/pool/entry.go:25 CoolKind.String()` 真身 **8 行**、门禁报 **155 行**
   （它后面紧跟 `TokenUsage`/`Status`/`ModelCostStatus` 等 5 个类型）。两个方向都错：
   `oversizeFuncs` 里有幽灵条目；且**新增一个类型声明等于偷上一个函数的额度**——写类型的人
   替别人的函数体量负责，激励方向反了。
2. **`Config.normalize()` 218 代码行**：`cmd/server/config.go` 是近 60 次提交里改动第 2 频繁
   的文件（10 次），原因不是逻辑复杂，而是 25 个互不相干的配置段串成一条直线。加一个配置项
   要在 200 行里找落点，还要自己判断该排在哪个解析表之前。
3. **`StreamHint` 260 代码行 + `sse.go` 全仓唯一 >1000 行文件**：7 个跨帧状态与 7 个闭包共处
   一个作用域，闭包靠「变量在不在作用域里」隐式耦合。上一轮（`fc54879`）正是在这里查出
   `[DONE]` 兜底写没加锁的真竞态——那是「状态所有权不可见」的直接后果。上一轮已显式冻结本项
   （「需重排状态所有权，单独立项」），本 REQ 即该项。

## 需求

三批，每批一个原子提交，**对外契约与出站字节流零改动**：

- **B0**：`check-depth.ps1` 函数体边界改按花括号深度判定（新增最小词法剥离，避免 `"{"` 污染
  计数）；无函数体的声明（`//go:linkname`）回落「下一个顶层声明」兜底。
- **B1**：`normalize()` 按配置段拆成 9 个文件方法（`config_normalize.go`），`normalize()` 退化
  为顺序声明；调用顺序保持逐段一致。
- **B2**：`StreamHint` 的跨帧状态与写路径收进 `streamRun` 结构体（`stream.go`），`StreamHint`
  退化为「装配 → 起心跳 → 读循环 → 收尾」四步。

## 验收（GWT）

- Given 同一份代码 → When 分别用旧/新度量跑 `check-depth.ps1 -Detail` → Then
  `oversizeFuncs` 7 → 6，**仅** `CoolKind.String()` 从名单消失，其余 6 项数字逐位不变。
- Given 旧 `normalize()` 体与新方法体都剥掉空行与 `//` 注释行 → When 逐行比对 → Then
  旧 216 条代码行**全部**在新集合命中，差异只有函数签名/花括号/import/编排调用/`var err`。
- Given `go test ./internal/upstream -count=1` → Then 全绿（`sse_test.go` 1113 行 +
  `coverage_parse_test.go` 的逐字节与金样断言即等价性判据）。
- Given 全量门禁 → Then `scripts/pre-commit.ps1` 退出码 0，`check-depth -Update` 后
  `maxFileLines 1041→997`、`oversizeFiles 1→0`、`oversizeFuncs 7→4`、`minCoverage 75→75.5`。

## 不做

- 不改任何报文内容（R8）、不改 `StreamHint` 的出站字节、不改配置语义与默认值。
- 不抽公共 `writeJSON`（`panel.go:988` / `handler.go:907`）：只有 2 处，未达「≥3 处稳定重复」
  判据，且抽出去要让 `server` 反向依赖 `panel`（红线）。
- 不合并 `cmd/login`(338 行) 与 `panel/login.go`(324 行) 的登录协议：2 处 + 需新增包与边界表
  条目；已实测两者**已漂移**（`panel/login.go:106` 限 1 MiB body，`cmd/login` 不限），记待办。
- 不再拆 `main()`（228 代码行）：上一轮已论证继续拆须把 30+ 局部变量变参数，收益为负。
- 不动 `usage.go:753 SnapshotWindow`(193)、`sse.go:105 Aggregate`(211)、`app.js`(1640)。
