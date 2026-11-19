# 2026-11-19 upstream nil map 根因修复

## 改了什么

`null` body 触发 panic 的**真正根因在 `internal/upstream`，不在 `prompt`**。
`b4f3823` 只修了先撞到的表层；请求继续下走到出站规范化才炸。

三处判据统一为 `err != nil || obj == nil`（nil map 与「非对象输入」同义，
走同一条原样返回路径）：

- `internal/upstream/payload.go:50`（`PrepareBodyOptWithEffortsAndDefaultAndCache`）
- `internal/upstream/payload.go:129`（`PrepareBodyOptWithEffortsAndDefault`）
- `internal/upstream/cache_key.go:63`（`InjectPromptCacheKey`）

新增 `internal/upstream/payload_fuzz_test.go`（`FuzzPrepareBody`，11 个种子含
`null`/`[]`/`"x"`/`7`/空串/截断 JSON），断言不 panic + 非对象输入原样返回 +
顺带过一遍 `InjectPromptCacheKey`。

## 怎么验证

| 验证 | 命令 | 结果 |
|---|---|---|
| 单包测试（种子） | `go test ./internal/upstream -run FuzzPrepareBody` | ok |
| 变异验证（有牙齿） | 守卫改回 `err != nil` 后复跑 | **FAIL**，`seed#4` 即 `null` panic |
| 长 fuzz | `-fuzz FuzzPrepareBody -fuzztime 30s` | 244 万次执行，0 panic |
| gofmt | `gofmt -l ./cmd ./internal` | 空 |
| 全量 | `go test ./... -count=1` | 见下 |

## 证据来源

生产日志 `D:\yg-gateway\data\logs\yg-gateway-20261119-144340.err.log`
两次 `prepareBodyAST → obj["stream"] = true` 完整 stacktrace（14:44:29、14:44:44）。

## 怎么回滚

`git revert <sha>`；三处判据与 fuzz 文件独立于其它改动，回滚不影响 cache key 语义。

## 措辞修正

先前写成「整个进程崩溃」**过重**。实测是 `net/http` recover 后的**单请求级 panic
+ 连接丢弃**（`curl` 得 HTTP=000），进程存活。已在
`docs/prd/REQ-upstream-nilmap-panic-2026-11-19.md` 与
`docs/prd/REQ-prompt-null-panic-2026-11-19.md` 中统一为精确表述。
