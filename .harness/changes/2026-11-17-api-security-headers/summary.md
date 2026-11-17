# 变更：API 侧安全响应头（nosniff + 只读端点 no-store）

- 日期：2026-11-17
- 分诊：L1（响应头新增，无报文形状变化，无鉴权/业务语义变化）
- 前置：`21b0ee4`

## 为什么

面板侧 `setSecurityHeaders` 早已下发 CSP / nosniff / X-Frame-Options / Referrer-Policy /
COOP，但 **`Handler.ServeHTTP` 一条安全响应头都没有**——`/v1/models`、`/status`、
`/metrics` 全部裸奔。

真实后果不是理论风险：

1. **缓存绕过鉴权**：`/status` 含账号积分、成本分档、错误分布；`/metrics` 含延迟分位与
   错误分类。这两个端点都要鉴权，但**一旦中间代理（公司网关 / CDN / 本地缓存代理）把它们
   缓存下来**，后续请求可能命中缓存绕过 `withAuth`。`Cache-Control: no-store` 是唯一
   能阻止这件事的响应头，而此前完全没有。
2. **MIME 嗅探**：`/v1/*` 返回 `application/json` 但没有 nosniff，老浏览器（IE/旧 Edge）
   会按内容猜类型，把 JSON 当 HTML 解析；JSON 里若含 `<>` 片段即可触发脚本执行面。

## 改动

`internal/server/handler.go` 的 `ServeHTTP`，在 OPTIONS 分支之后、鉴权与 mux 之前：

```go
w.Header().Set("X-Content-Type-Options", "nosniff")
if r.Method == http.MethodGet {
    w.Header().Set("Cache-Control", "no-store")
}
```

**为什么只对 GET 设 no-store**：POST 的 `/v1/chat/completions` 响应已由
`upstream/stream.go` 自带 `Cache-Control: no-cache` + `X-Accel-Buffering: no`，
重复设 `no-store` 无害但会与流式语义混淆；而所有只读端点（models/status/metrics/healthz）
都是 GET，一条规则完整覆盖。

**为什么放在 OPTIONS 之后**：预检响应 204 无 body，不需要缓存语义；放在鉴权之前是为了让
**401/403/429 的拒绝响应也带上 nosniff**（拒绝响应同样可能被嗅探）。

## 验证

1. `go test ./internal/server/ -run TestAPISecurityHeaders -count=1` → PASS
   - 四个端点逐个断言 `X-Content-Type-Options: nosniff`
   - `/status`、`/metrics`、`/healthz` 断言 `Cache-Control: no-store`
   - `POST /v1/models`（405 分支）断言有 nosniff、**不**断言 no-store（与规则一致）
2. `go test ./internal/server/ -count=1` → 全绿（9.66s，无既有断言被破坏）
3. **牙齿验证**：把 `nosniff` 值改成 `nosniff-TEETH-DISABLED`，
   `TestAPISecurityHeaders` 立即 FAIL（四处逐个报
   `X-Content-Type-Options="nosniff-TEETH-DISABLED" want nosniff`）；改回即绿。

## 主动未做（审计结论，逐条有实测依据）

审计过但**没有**改的项，理由是现状已经成立：

| 面 | 现状 | 结论 |
|---|---|---|
| Bearer 比较 | `httpauth` 走 SHA-256 摘要 + `subtle.ConstantTimeCompare`，缺头也走一次假比较保持耗时形状 | 已加固，无需动 |
| CORS | `reflectableCORSOrigin` 只回显 https 源，http/null 一律拒绝并留 WARN | 已加固 |
| 入站 body 上限 | chatflow / anthropic / responses / count_tokens 四处全部 `MaxBytesReader` | 已加固（有牙齿测试） |
| `/metrics` 鉴权 | `metrics_handler_test.go` 断言无 key → 401 | 已加固 |
| 只读端点限流 | `ReadOnlyRateLimit` + `/healthz` 豁免（探活友好） | 已加固 |
| 面板安全头 | CSP / nosniff / DENY / no-referrer / COOP 全套 | 已加固 |
| 密钥比较 `==` | 全仓 grep `apiKey ==` / `token ==` 只命中空串检查（`AccessToken == ""`），无明文比较 | 无缺口 |

## 回滚

`git revert <this-commit>`。纯响应头，客户端行为不受影响（OpenAI SDK 不依赖缓存头）。

## 残余风险（已知未覆盖）

- 未验证真实中间代理下的缓存行为（本地无代理环境），`no-store` 是标准手段但有效性依赖
  中间件合规。
- `string(ReadOnlyRateLimit)` 只对 `/v1/models` 生效的既有设计未改。
