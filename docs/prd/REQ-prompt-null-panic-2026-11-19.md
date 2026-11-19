# REQ-prompt-null-panic-2026-11-19 — `null` 请求体不得打死进程

## 必要性

`Rewrite` / `Append` 是出站请求改写的关键路径，处理的是**外部客户端发来的原始
请求体**。原实现对字面 `null` 会 panic：

```
json.Unmarshal([]byte("null"), &obj) → err == nil，obj == nil
obj["messages"] = ...            → 对 nil map 赋值 → panic
```

对 7×24 常驻网关而言这不是「一个请求失败」，是**进程死掉、所有在途请求一起失败**。

需求来源不是推测：`FuzzRewrite` 的种子集里放 `null` 后，第一次运行即复现，
栈顶为 `prompt.go:62`。

## 现状（改前）

| 输入 | Rewrite | Append |
|---|---|---|
| `""` | 原样返回 | 原样返回 |
| `{` | 原样返回 | 原样返回 |
| `[]` | 原样返回（Unmarshal 到 map 失败） | 同 |
| `123` / `"x"` | 原样返回（同上） | 同 |
| **`null`** | **panic** | **panic** |
| `{}` | 插入 system 消息 | 插入 system 消息 |

## 验收（Given-When-Then）

**G1** Given 请求体为字面 `null`，When 调用 `Rewrite(null, "sys")`，
Then 返回原 body，且**不 panic**。

**G2** Given 请求体为字面 `null`，When 调用 `Append(null, "sys")`，
Then 同 G1。

**G3** Given 请求体为任意字节串，When 连续 fuzz 30 秒，
Then 不 panic 且当输入不是 JSON 对象时输出与输入逐字节相同。

**G4** Given 请求体为合法对象但无 `messages` 字段，When 调用两者，
Then 仍按原语义插入 system 消息（**不得因本次修复而改变**）。

## 非目标

- 不改变正常请求的改写语义（G4 是回归护栏）。
- 不引入"空 body 也注入"的行为。

## 实现口径

判据从 `err != nil` 扩到 `err != nil || obj == nil`。nil map 与"非对象输入"同义，
走同一条原样返回的降级路径——这样两个函数的行为矩阵只剩两列（改 / 原样），
不新增第三种状态。

## 验证

| 检查 | 结果 |
|---|---|
| `FuzzRewrite` 种子（含 `null`） | 修复前 panic / 修复后 ok |
| `go test -fuzz FuzzRewrite -fuzztime 30s` | **8,776,237 次执行，0 panic** |
| `go test ./internal/prompt ./internal/server -count=1` | 两包均 ok |
| `gofmt -l ./internal/prompt` | 无输出 |
