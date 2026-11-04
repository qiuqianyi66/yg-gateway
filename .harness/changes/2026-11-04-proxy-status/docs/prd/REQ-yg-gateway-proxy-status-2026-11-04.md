# REQ-yg-gateway-proxy-status-2026-11-04

需求：/status 输出账号出站代理的生效三态（direct / active / failed），代理配错在运维视图可见。

## REQ-1 账号级脱敏 proxy_url

**Given** 账号 auth 配置了 `proxy_url`（可含 `user:pass@` 凭据）
**When** GET /status（或 panel / opsagent 读 pool.List()）
**Then** `accounts[].proxy_url` 输出**脱敏值**（仅 scheme://host，无 userinfo）
**And** 任何可见面（日志 / status / 面板 JSON）不得出现代理凭据片段（R1/R2 红线）

## REQ-2 直连账号不输出 proxy_url

**Given** 账号未配置 `proxy_url`
**When** GET /status
**Then** 该账号不输出 proxy_url 键（omitempty，空 = 直连表达）

## REQ-3 构造失败 → failed 可见

**Given** 账号配置了无法构造的代理（坏 URL / 不支持 scheme / SOCKS5 初始化失败）
**When** 该账号的 client 被构造过（clientFor / chatClientFor 任一调用）
**Then** GET /status 的 `proxy_status` 含 `"<脱敏URL>": "failed"`
**And** 该账号出站必失败（fail-closed，REQ-proxy-failclosed 2026-11-04 既有语义）

## REQ-4 构造成功 → active 可见

**Given** 账号配置了可构造的代理（http/https/socks5）
**When** 该账号的 client 被构造过
**Then** GET /status 的 `proxy_status` 含 `"<脱敏URL>": "active"`

## REQ-5 proxy_status 键恒存在

**Given** 任何 /status 响应
**When** 解析响应
**Then** `proxy_status` 键恒存在（未装配 upstream / 零登记时 = `{}`）
**And** 键缺失不可被消费方读成「无此观测」

## 验收运行

```
go test ./internal/logfmt/ -run TestRedactProxyURL -v
go test ./internal/upstream/ -run 'TestProxyStatusSnapshot|TestProxyConfigFailClosed' -v
go test ./internal/server/ -run TestStatusExposesProxyFields -v
go test ./internal/pool/ -run TestStatusProxyURLRedacted -v
go test ./... -count=1
```

## 回滚

git revert 本提交；或按 summary.md「回滚」三档语义级删除。
