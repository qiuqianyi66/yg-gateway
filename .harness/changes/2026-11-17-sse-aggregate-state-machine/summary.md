# 变更：把 SSE 非流式聚合结构体化（Aggregate 211 代码行 → 读取循环）

- 日期：2026-11-17
- 分诊：L1（纯内部重构，无对外契约变化；`Aggregate` 签名与输出 JSON 形状逐位不变）
- 前置：`1a1acbd`

## 为什么

`internal/upstream/sse.go` 的 `Aggregate` 是全仓第 2 大函数（**211 代码行**，阈值 120），
把「14 个可变状态 + 4 条合并规则 + 读取循环 + 收尾组装」压在一个栈帧里：

- **状态**：id/model/created/content/reasoning/role/finishReason/usage/gotAnyContent/
  validEvents/sawDone/toolCalls/toolOrder/toolSeq/idIndex；
- **4 条规则**：`trackedBytes`（content+reasoning+args 三路计量）、`appendContent`
  （latch 唯一写入点）、`mergeToolCallsChunk`（按 index/id 归位）、`mergeMessageFields`；
- **3 条收尾**：空流判定、id/created 补齐、tool_calls 截断清理。

删除测试：结构体化后「改任何一条规则只读对应方法」，读取循环只剩 4 行判断——复杂度是集中
到 8 个方法（每个单一职责），不是搬家。

## 改动

| 文件 | 承载 |
|---|---|
| 新增 `internal/upstream/sse_aggregate.go`（282 非空行） | `sseAggregator` 结构体 + `newSSEAggregator` + 8 个方法（`trackedBytes` / `appendContent` / `nextToolIndex` / `mergeToolCallsChunk` / `mergeMessageFields` / `consumeLine` / `consumeChunk` / `build`） |
| `internal/upstream/sse.go`（951 → 395 非空行） | `Aggregate` 只剩读取循环（约 26 代码行）：读行 → 体量上限判定 → `consumeLine` → EOF/DONE 收尾；`readSSELine` / `normalizeFrame` / 帧构造等基础件不变 |

`Aggregate` 签名 `(r io.Reader) (map[string]any, error)` 未变；输出 JSON 字段（id/object/
created/model/choices/message/finish_reason/usage）逐位不变。

## 数字

| 指标 | 前 | 后 |
|---|---|---|
| `Aggregate` 代码行 | 211 | 约 26（读取循环） |
| 全仓 >120 行函数 | 2 | **1**（仅剩 `main()` 228） |
| `oversizeFuncs`（棘轮） | 2 | **1** |
| `sse.go` 非空行 | 951 | 395 |
| `sse_aggregate.go` 非空行 | 0 | 282 |
| 最大文件（942） | `cmd/server/main.go` | 未变 |

## 验证

1. `go build ./...` = 0；`go vet ./internal/upstream` = 0；`gofmt -l` 无输出
2. `go test ./internal/upstream/ -count=1` 全绿（3.031s）；8 条 Aggregate 用例全过
3. **牙齿验证（关键）**：把 `trackedBytes` 退化成只量 `content.Len()`，
   `TestAggregateReasoningAndArgsCountedInBudget` **立即 FAIL**
   （`纯 reasoning 流超上限应报 errFrameTooLarge, got <nil>`）；改回后立即绿——
   证明 F-705 的三路计量口径真被测试锁住，不是「搬完恰好全绿」。
4. 棘轮：`check-depth -Update` 输出 `+ oversizeFuncs 2 → 1`

## 回滚

`git revert <this-commit>`；纯包内重构，无数据/配置/接口影响。

## 主动未做

- 未动 `main()`（228）：下一个目标，单独一批。
- 未动权重算法本身（`pickWeighted` / `routingWeightOf`）：上一批已说明理由。
- 未拆 `internal/panel/app.js`（1,640）：冻结理由不变（无浏览器级 E2E 兜底）。
