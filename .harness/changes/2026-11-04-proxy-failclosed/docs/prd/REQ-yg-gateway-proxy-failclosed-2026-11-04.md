# REQ-yg-gateway-proxy-failclosed-2026-11-04

需求：显式代理失败必须 fail-closed（不回退直连）；11102 目录为空时必须继续换号。

## REQ-1 代理构造失败 → 该账号出站失败

**Given** 账号 auth 文件配置了 `proxy_url`，且该 URL 无法解析（如 `://missing-scheme`）或 scheme 不受支持（如 `ftp://`）
**When** 该账号发起任意出站请求（聊天 / doJSON / 模型目录）
**Then** 请求失败，错误消息含 `proxy config error`；**绝不**成功出站（不得静默走直连）
**And** 失败请求进入既有网络错误路径（换号，不静默吞）

## REQ-2 未配置代理的账号行为不变

**Given** 账号未配置 `proxy_url`（空 = 直连）
**When** 发起请求
**Then** 行为与改动前完全一致（直连、连接池共享、超时参数不变）

## REQ-3 代理连接期失败（构造成功）同样冒泡

**Given** 代理可构造（如 `socks5://127.0.0.1:1`）但连接不可达
**When** 发起请求
**Then** 请求失败且错误冒泡（dial 错误），不静默直连

## REQ-4 11102 + 目录为空 → 继续换号

**Given** 模型目录为空（刚启动/拉取失败，modelCatalogUnknown），上游对首个账号返回 11102
**When** 客户端请求该模型
**Then** 响应**不是** 400 model_not_found；继续轮转尝试其他账号；换到健康号时返回 200

## REQ-5 11102 + 目录确认无此模型 → 仍 fail-fast

**Given** 模型目录非空且确定不含该模型（modelCatalogAbsent）
**When** 客户端请求该模型
**Then** 立即 400 model_not_found（既有行为，回归不破坏）

## 验收运行

```
go test ./internal/upstream/ -run 'TestProxyConfigFailClosed|TestProxyDialFailureSurfaces' -v
go test ./internal/server/ -run TestChat11102UnknownCatalogRotates -v
go test ./internal/server/ -run TestModelCatalogVerdictTriState -v   # REQ-5 既有三值回归
go test ./... -count=1
```

## 回滚

git revert 本提交；或语义级：transport.go 三处 `failDial(...)` 换回 `return tr`。
