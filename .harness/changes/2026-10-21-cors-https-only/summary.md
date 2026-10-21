# cors-https-only 变更记录

> 需求来源：`docs/code-review/2026-10-20-four-dimensions.md` §一 [P3]
> 「CORS 回显任意 Origin + Allow-Credentials: true……建议收紧为已知前端源」
> （`server/handler.go:276-283`）。

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | cors-https-only（CORS 回显面收紧到 https 源） |
| 类型 | security-hardening + test |
| 涉及范围 | `internal/server/handler.go` + 1 个新测试文件 |

## 二、问题与风险界定

旧实现：**任意** Origin 原样回显 + `Access-Control-Allow-Credentials: true`。

先核实了三个事实再动手（结论以代码为准，不沿用审查稿的推测）：

1. **全仓无 cookie**（`SetCookie`/`http.Cookie` 零命中）：鉴权是 Bearer 头。
   → `Allow-Credentials` 没有可骑乘的环境凭证，CSRF 确不可利用（与审查稿一致）。
2. **面板全部走相对路径**（`app.js` `fetch('/panel/api/...')`、`fetch('/v1/...')`）：
   同源调用，**根本不参与 CORS**——收紧不影响面板。
3. CORS 头的存在意义是 `821a296` 的设计初衷：**公网 https 页经 Private-Network-Access
   调本机网关**。即合法调用方恒为 https 源。

所以「收紧为已知前端源」（白名单）不可行——调用方就是任意 https 页，没有已知清单；
但**回显任意 Origin** 同样没有依据：http 源与 `null` 源（沙箱 iframe / file:// /
data:）来源不可验证，没有理由拿到本机网关的跨域读权限。

## 三、方案

`reflectableCORSOrigin(origin)`：仅当 Origin 可解析、scheme 为 https、host 非空时
回显；否则**不回显**（浏览器自然拦下跨域读）+ WARN 留痕。

- 保留 `Allow-Credentials: true`：Bearer 鉴权无 cookie，该头零骑乘面；删它反而会
  打断 `credentials:'include'` 的调用方。已在注释中写明这个判断，防止后人误删。
- `Access-Control-Allow-Methods/Headers/Private-Network` 静态头照旧下发（无 ACAO 时
  浏览器忽略它们，零影响）。
- 未回显时不发 `Vary: Origin`（没有可缓存差异）。

**残余风险如实记录**：`api_key` 为空（配置允许）时网关本就不鉴权，此时任意 https
页借 PNA + CORS 即可驱动账号池——这不是 CORS 层能关的洞，根因是空 key 配置
（config.go:577 已有警告）。CORS 收紧只关「来源不可验证」那一类。

## 四、测试

`internal/server/cors_test.go`（新增，4 例）——该层此前**零测试**：

1. `TestReflectableCORSOrigin`：11 例策略表（https 各种形态放行；http / null /
   data: / 空 / 畸形 / 反斜杠 host 拒绝）。
2. `TestCORSPreflightHTTPSOriginReflected`：经 `ServeHTTP` 的 OPTIONS 预检拿齐
   ACAO + ACAC + Vary + PNA + Methods + Headers，204。
3. `TestCORSPreflightHTTPOriginNotReflected`：http 源预检不得出现 ACAO/ACAC/Vary。
4. `TestCORSNoOriginUnaffected`：无 Origin 请求（CLI/curl/SDK）零 CORS 头，
   静态头照旧。

## 五、自检清单

- [x] `go build ./...` + `go vet ./...` + `go test ./...` 全绿
- [x] gofmt 干净
- [x] 牙齿验证：把校验改为忽略（无条件回显）→ 策略表 + http 源两例精确 FAIL；还原 → PASS
- [x] 深度棘轮未回退（handler.go 1409 → 1435 非空行，远离 2058 基线）

## 六、Learning

又是「层无测试」同款：CORS 功能自 821a296 上线起**没有任何测试**，语义对错全靠
运气。与 supports_images 同一判法——顺着数据流问「这一层有没有测试直接钉住它」。
另：审查稿写「建议收紧为已知前端源」，但**先核实再动手**发现该方案与功能设计
（任意 https 页）根本冲突；按原话执行会直接把功能做废。审查结论是假设，验证是义务。
