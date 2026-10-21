# school-marshal-r10 变更记录

> 需求来源：`docs/code-review/2026-10-20-four-dimensions.md` §一 [P3] R10
> 「`internal/upstream/school.go:29`、`payload.go:93-95` — 静默 marshal / merge」。

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | school-marshal-r10（schoolJSON marshal 失败不再静默） |
| 类型 | fix + test |
| 涉及范围 | `internal/upstream/school.go` + 1 个新测试文件 |

## 二、逐条核实：审查记录有一处误判

**`school.go:29`（真问题，已修）**：`raw, _ = json.Marshal(body)` 丢弃 error。
body 不可序列化时 `raw` 为 nil → **发一个空 body 的 POST**，上游报参数错误，
运维看到的全是「上游拒绝」，查错方向整个错。且同文件的 `ReportMPEvent`
（school.go:99-102）**做了检查**——schoolJSON 是同一文件里唯一漏检的，不一致
本身就是修复理由。

**`payload.go:93-95`（误判，不改）**：审查把它归为「静默 merge」，实际这三行是

```go
msgs, _ = mergeAdjacentToolCalls(msgs)
msgs, _ = repackToolResultBlocks(msgs)
msgs, _ = cleanupOrphanToolCalls(msgs)
```

三个函数的签名都是 `([]any, bool)`——**第二返回值是「有无改动」标志，不是 error**
（见 tool_pairing.go:35/123/281）。丢弃 changed 标志是刻意设计：回写
`obj["messages"]` 必须无条件执行（payload.go:96-98 的注释已写明理由）。
这里没有任何错误被吞噬，按原记录去「修」只会制造无意义 churn。
**教训：`_, _` 不等于 `err`，动手前先看被丢弃值的类型。**

## 三、方案

```go
b, err := json.Marshal(body)
if err != nil {
    return fmt.Errorf("序列化 school-season 请求体失败（请求未发出）: %w", err)
}
raw = b
```

请求未发出即报错，带 `%w` 保留原始错误链；错误信息点明后果（请求未发出），
避免与上游错误混淆。

## 四、测试

`internal/upstream/school_json_test.go`（新增，2 例）：

1. `TestSchoolJSONMarshalFailureIsNotSilent`：body 含 `chan int`（json 明确不支持）
   → 必须返回 error，且 transport **零命中**（证明没有发出空 body 请求）。
2. `TestSchoolJSONMarshalOKStillSends`：对照——正常 body 照旧发出，路径前缀
   `/portal/activity/school` 与 body 序列化结果原样（修复不得把路堵死）。

## 五、自检清单

- [x] `go build ./...` + `go vet ./...` + `go test ./...` 全绿
- [x] gofmt 干净
- [x] 牙齿验证：改回 `raw, _ = json.Marshal(body)` → 例 1 精确 FAIL；还原 → PASS
- [x] 深度棘轮未回退（school.go 231 → 236 非空行）
