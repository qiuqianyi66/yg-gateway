# REQ-upstream-nilmap-panic-2026-11-19

**触发**：2026-11-19 部署 `b4f3823`（修 `prompt` 包 nil map）后，用真实 `null` body 打生产，
客户端拿到空响应（`curl` HTTP=000 / exit 52），服务未整体崩溃。

**根因（生产日志原文，非推断）**：

```
http: panic serving 127.0.0.1:60693: assignment to entry in nil map
  yg-gateway/internal/upstream.prepareBodyAST(0x0, ...)       internal/upstream/payload.go:66
  yg-gateway/internal/upstream.PrepareBodyOptWithEffortsAndDefaultAndCache(...)  payload.go:53
  yg-gateway/internal/upstream.(*Client).prepareBody(...)      client.go:383
  yg-gateway/internal/upstream.(*Client).ChatStreamContext(...) client.go:692
  yg-gateway/internal/server.(*chatRun).rotateAndServe(...)     chatrun.go:281
  yg-gateway/internal/server.(*Handler).chatCompletions(...)    handler.go:694
```

日志：`D:\yg-gateway\data\logs\yg-gateway-20261119-144340.err.log`（14:44:29、14:44:44 两次）。

## 事实

`json.Unmarshal([]byte("null"), &obj)` 是**合法 JSON、`err == nil`**，但 `obj` 是 `nil` map。
随后 `obj["stream"] = true` 对 nil map 赋值 → panic。

`prompt` 层的修复（`b4f3823`）只挡住了先撞到的那一层；请求继续下走到
`upstream.prepareBody` 才炸。**两个函数是同一类 bug 的两处实例。**

## 穷尽（生产代码 9 处同模式，逐处判定）

| 位置 | 后续赋值 | 结论 |
|---|---|---|
| `prompt/prompt.go:61` | — | 已修（b4f3823） |
| `prompt/prompt.go:121` | — | 已修（b4f3823） |
| `upstream/payload.go:50` | `prepareBodyAST` → `obj["stream"]=true`（:66） | **panic** |
| `upstream/payload.go:129` | 同上 | **panic** |
| `upstream/cache_key.go:77` | `obj["prompt_cache_key"]=key` | **panic** |
| `upstream/payload.go:349` | `obj["messages"]=...`，但前置 `!ok → return` | 安全 |
| `server/logging.go:266` | `obj["model"]=bare`，前置 `!ok → return` | 安全 |
| `upstream/sse.go:327` | `e["gateway_hint"]`，前置 `!ok → return` | 安全 |
| `upstream/stream.go:404` | 无赋值 | 安全 |

判据统一为 `err != nil || obj == nil`：nil map 与「非对象输入」同义，
走同一条原样返回的降级路径，行为矩阵不新增第三种状态。

## 验收（GWT）

- **G1** 给 `PrepareBodyOptWithEffortsAndDefaultAndCache([]byte("null"), ...)` 不再 panic，原样返回。
- **G2** 给 `InjectPromptCacheKey([]byte("null"), ...)` 不再 panic，原样返回。
- **G3** `FuzzPrepareBody` 种子集含 `null`，日常 `go test ./...` 守住；长 fuzz 30s 无 panic。
- **G4** 变异验证：把守卫改回 `err != nil`，`FuzzPrepareBody/seed#4` 必须 FAIL（实测已 FAIL）。

## 风险措辞修正

先前 PRD/变更记录写的「整个进程崩溃」**过重**。`net/http` 的 recover 让每个 panic
只打死那一个 goroutine 连接、进程存活，客户端收到空响应。
精确表述：**单请求级 panic + 连接被丢弃**，不是进程级。
