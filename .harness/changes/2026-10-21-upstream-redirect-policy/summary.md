# upstream-redirect-policy 变更记录

> 需求来源：`docs/code-review/2026-10-20-four-dimensions.md` §一 [P3]
> 「全仓无 `CheckRedirect`——建议显式设重定向策略，不依赖 net/http 默认值」。

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | upstream-redirect-policy（出站重定向策略显式化） |
| 类型 | security-hardening + test |
| 涉及范围 | `internal/upstream/transport.go`、`internal/upstream/client.go`（行数不变）+ 1 个新测试文件 |

## 二、问题

`HTTP` / `ChatHTTP` 两个 client 都没设 `CheckRedirect`，重定向行为完全交给
net/http 默认值（最多跟随 10 次）。上游 base 是配置固定的受信端点
（copilot.tencent.com / www.codebuddy.cn / www.workbuddy.cn / workbuddy.ai），
API 调用本不该发生跳转；一旦上游（或被攻陷的中间盒）回 3xx，网关会**静默跟随**
到任意 host——SSRF 探测面。默认值会跨 host 剥掉 Authorization（风险因此不高），
但「依赖默认值」意味着策略从未被显式声明，也没有任何测试钉住它。

## 三、方案

`sameHostRedirect` 策略（transport.go，与 newTransport 同属「出站连接单一事实来源」）：

- **同 host 放行**：hostname（EqualFold）+ 生效端口都相等。覆盖合法的站内跳转
  （路径调整、http→https 升级；`host` 与 `host:443` 按 scheme 补缺省端口后比对，
  不误判；`host:8443` 判跨 host——换端口=换服务）。
- **跨 host 拒绝**：WARN 留痕（from → to，`URL.Redacted()` 脱敏）+ 返回
  `http.ErrUseLastResponse`——调用方拿到 3xx 响应本体，走既有状态码分类，
  而不是被引去未审计 host，也不把异常推进传输层错误路径丢失线索。
- 不返回普通 error：那会让 `Do` 包一层 `*url.Error`，把「上游回了什么」的
  诊断信息冲掉。

接线方式：新增 `newUpstreamHTTP(timeout, tr)` 构造器；client.go 两个字面量改为
调用它——**行数不变**（client.go 是全仓最大文件 2058 非空行，深度棘轮只许降不许涨，
加行即 FAIL；这次把构造逻辑搬到 transport.go，调用点原地替换）。

**刻意不动**：`panel/login.go` 的 `loginHTTP`、`alerting/notifier.go` 的 webhook
client 不在审查条目内（后者目标是用户自定义 URL，跟随重定向是合理行为），不扩改动面。

## 四、测试

`internal/upstream/redirect_policy_test.go`（新增，4 例）：

1. `TestUpstreamClientsHaveExplicitRedirectPolicy`：`New()` 两个 client 的
   CheckRedirect 均非 nil——「不依赖默认值」本身成为可断言命题。
2. `TestSameHostRedirectPolicy`：7 例判定表（同 host 路径/http→https/大小写/
   显式缺省端口放行；跨 host/换端口/相似域名后缀拒绝）。
3. `TestRedirectPolicyEndToEndSameHostFollowed`：真实 client + httptest 服务器，
   站内 302 仍被正常跟随（防策略过紧误伤）。
4. `TestRedirectPolicyEndToEndCrossHostRejected`：双服务器（端口不同=跨 host），
   第二个 host 零请求、调用方拿到 302 本体。

## 五、自检清单

- [x] `go build ./...` + `go vet ./...` + `go test ./...` 全绿
- [x] client.go 非空行 **2058 与基线持平**（未加一行）
- [x] gofmt 干净
- [x] 牙齿验证：`sameHostRedirect` 改为放行一切 → 跨 host 两例精确 FAIL；还原 → PASS
- [x] 深度棘轮未回退

## 六、Learning

「不依赖默认值」的安全项，测试要钉的是**默认值被替换**这个事实本身（例 1），
而不只是策略分支（例 2-4）——否则有人删掉 CheckRedirect 赋值，分支测试仍然全绿
（策略函数没被删，只是没接上）。接线与实现是两道独立的防线。
