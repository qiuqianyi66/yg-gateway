# /status 代理三态观测（P0-2）

日期：2026-11-04

## 背景

magpie REA（docs/reference/magpie-rea-full-2026-11-03.md）第④节结论：显式代理必须
fail-closed，且 per-account 代理观测是 yg 自己能补的（magpie 自身也是 unknown）。
P0-1（316c0e7）落地 fail-closed 后，**代理配错 = 该账号出站全失败、请求持续换号**——
运维必须能从 /status 看出「代理没生效」，否则会把持续换号误判成账号本身的问题。

规划 P0-2：/status 输出账号代理 `proxy_url`（脱敏）+ 生效三态
（direct / active / failed）。

## 设计决策（Evidence-locked）

1. **三态语义**（per-proxyURL，非 per-account 独享）：
   - `active`   = 代理已配置且 Transport 构造成功（连接期失败由 dial 错误冒泡，是"瞬时不可达"不是"配置错误"，不改此态）
   - `failed`   = 代理已配置但构造失败（fail-closed，该账号出站必失败）
   - 直连账号不出现（accounts[].proxy_url 为空表达 direct）
2. **脱敏必须在下沉层做**：pool.Status 直出 /status + panel（panel.go:354/803/817/907/999）
   + opsagent + bench，**原文会泄密**（R1/R2 红线）。而 pool 不能依赖 upstream
   （check-boundaries.ps1:67 `pool = @(auth,evidence,logfmt,pkg,unknown)`）。
   因此把 redactProxyURL 逻辑**下沉 internal/logfmt**（pool/upstream/auth 三者的共同
   依赖叶子包，单一实现）：
   - `logfmt.RedactProxyURL`（单一实现，upstream.redactProxyURL 变薄包装，禁止另起实现）
   - `auth.Auth.RedactedProxyURL()`（视图取值）
   - `pool.Status.ProxyURL`（脱敏值；json `proxy_url,omitempty`）
3. **登记时机**：clientFor/chatClientFor 构造代理 client 时（与 proxyClients 同一临界区），
   `newTransportWithProxy` 返回 `proxyEffective`；`Client.ProxyStatusSnapshot()` 输出
   脱敏 URL → "active"/"failed"。
4. **/status 输出**：顶层恒输出 `proxy_status` 键（零登记时 {}，键缺失不可读成「无此观测」）；
   accounts[].proxy_url 脱敏值随 pool.Status 透出。

## 改动清单

| 文件 | 改动 |
|---|---|
| internal/logfmt/logfmt.go | + RedactProxyURL（单一实现，R1/R2 红线注释） |
| internal/upstream/transport.go | redactProxyURL → logfmt 薄包装；+ proxyEffective 三态；newTransportWithProxy 返回 (tr, eff) |
| internal/upstream/client.go | Client + proxyEff 登记；clientFor/chatClientFor 构造即登记；+ ProxyStatusSnapshot() |
| internal/auth/auth.go | + RedactedProxyURL()（视图脱敏取值） |
| internal/pool/entry.go | Status + ProxyURL（脱敏，json proxy_url,omitempty） |
| internal/pool/state.go | statusOf 透出 e.a.RedactedProxyURL() |
| internal/server/status.go | **新文件**：status/proxyStatusSnapshot/agentProfileSnapshot/latencySnapshot/countsMapFrom 自 handler.go 拆出（depth 门禁：handler.go 被推超 1000 行） |
| internal/server/handler.go | status 块移出；路由不变 |

**测试**（新增 8 处断言点）：
- logfmt/redactproxy_test.go：脱敏 6 用例（含 userinfo 隐去/解析失败/空串）
- upstream/proxy_status_test.go：登记→快照三用例（failed/active/直连不出现、脱敏、chat 路径共享）
- server/handler_proxy_test.go：/status 键恒存在 + accounts 脱敏 + 直连省略
- pool/pool_test.go：List() 透出脱敏值 + 直连空
- transport_failclosed_test.go：适配新签名 + 断言 eff==proxyFailed

**顺带修复的真 bug**：newTransportWithProxy 的 `default`（不支持 scheme）分支
原实现落函数尾 `return tr, proxyActive`——TestProxyConfigFailClosed 抓出
（生效状态=active 而实际已 failDial），已改 `return tr, proxyFailed`。

## 验证

- `go test ./... -count=1`：30 包全绿
- `go vet ./...`：无输出
- `gofmt -l ./cmd ./internal`：零输出（拆分后 handler.go 925 行）
- `check-boundaries.ps1`：通过
- `check-depth.ps1`：oversizeFiles 2（未回退），最低包覆盖率 74% 保持

## 回滚

git revert 本提交。语义级：
- /status 不需要 proxy_status：删 handler.status 里 `"proxy_status"` 一行；
- 不想要账号级 proxy_url：删 pool.Status.ProxyURL 字段 + statusOf 一行；
- 不想要状态登记：删 clientFor/chatClientFor 的 proxyEff 两行 + 字段。

## 观察（未修，记录）

- proxyFailed 是**构造期**判定（配置错误）；连接期（代理可达性）失败走 dial 错误冒泡，
  不改变 effective——「代理半死」仍需靠请求失败日志/换号观察，/status 不承诺该维度。
- 账号共用同一 proxyURL 时状态共享（per-proxyURL 键）；同一代理部分账号生效部分失败
  的场景不存在（构造失败只取决于 URL 本身，与账号无关）。
