# 封号风险降低改动

日期：2026-11-02

## 背景

用户看到面板上"模型锁池"显示 `nonexistent-model-xyz`，担心账号被封。
排查后发现：是回归测试脚本发了一个假模型名，触发了 11102 负缓存。

## 改动清单

### 1. Session-ID 改成进程级随机（P0-2）

**文件**：`internal/upstream/headers.go`、`internal/upstream/client.go`

**问题**：原来 `X-Session-ID` 按 uid 固定派生，跨重启不变。真实客户端每次启动会生成新会话 ID，固定 Session-ID 像机器人。

**改法**：
- `X-Machine-ID` 保持账号固定（模拟一台设备一个号）
- `X-Session-ID` 改成进程级随机（每次网关启动生成一次 UUID，重启即换）

**风险**：低。只改一个请求头的生成逻辑，不影响业务逻辑。

### 2. 11102 负缓存白名单（P1-3）

**文件**：`internal/server/models_handler.go`、`internal/server/handler.go`

**问题**：任何不存在的模型名（包括客户端拼错的）都会被缓存 6h。今天的 `nonexistent-model-xyz` 就是例子。

**改法**：
- 加 `isKnownModel()` 方法：查动态模型目录缓存，判断模型名是否已知
- `applyErrorPolicy` 的 ErrModelBlocked 分支：先查白名单，已知模型才写负缓存
- 目录为空（刚启动）时不写缓存——宁可多试一次，不可误缓存真实模型

**效果**：客户端拼错模型名不会污染面板冷却，也不会浪费选号槽位。

### 3. 回归测试脚本不再污染生产（P2-5）

**文件**：`scripts/yg-regression-test.py`

**问题**：
- S5.3 发假模型名触发 11102 负缓存，污染面板
- S4 21 连发 RPM 测试故意打上游 429 冷却
- 硬编码打生产 7863

**改法**：
- 加 `--base` 参数，可切换测试端口
- S4 默认 skip（需要显式开启才跑）
- S5.3 发完假模型名后，主动从 `state.json` 清理冷却条目
- 文件头加警告说明

### 4. 多账号共用 IP：per-account proxy 完整链路（P0-1）

**文件**：`internal/auth/auth.go`、`internal/upstream/transport.go`、`internal/upstream/client.go`、`internal/upstream/desktop.go`、`internal/upstream/growth_stub.go`、`internal/upstream/growth_transport.go`、`internal/upstream/profile.go`、`internal/upstream/report.go`、`internal/upstream/models_fetch.go`、`internal/upstream/global_models.go`

**问题**：4 个账号的请求全部从网关机器同一个 IP 发出，上游风控很容易关联"这个 IP 上有 N 个账号在活动"。

**改法**：
- Auth 结构体加 `ProxyURL` 字段，从 auth 文件的 `proxy_url` 键读取
- transport.go 加 `newTransportWithProxy()` 函数，支持 HTTP + SOCKS5 代理
- Client 加 `proxyClients` 缓存和 `clientFor()` / `chatClientFor()` 方法
- **全路径接入**：聊天主路径 + doJSON 通用请求（12 个调用点）+ 模型目录拉取 + global 模型目录，所有出站请求都按账号选代理

**用法**：在 auth 文件里加 `"proxy_url": "http://user:pass@proxy:8080"` 或 `"socks5://user:pass@proxy:1080"`。
空 = 直连（默认行为，向后兼容）。

**支持的代理类型**：
- `http://` 或 `https://` → HTTP CONNECT 隧道
- `socks5://` → SOCKS5 代理（x/net/proxy）

**错误处理**：代理解析失败或初始化失败时打 WARN 日志，自动回退直连。

### 4b. 最小请求间隔限流（主动 RPM 风控规避）

**文件**：`internal/pool/pick.go`、`internal/pool/pool.go`、`internal/pool/entry.go`、`internal/server/chatrun.go`、`cmd/server/config.go`、`cmd/server/main.go`

**问题**：多账号共用 IP 时，除了 per-account proxy，还需要分散单账号的请求时间——真实用户不会每秒发多次请求，短时间打太密触发上游风控。

**改法**：
- 选号时加 `minIntervalBlocked()` 判定：距上次请求小于该间隔的账号跳过
- CN 域 `min_request_interval`、global 域 `min_request_interval_global` 分档（国际版风控更敏感）
- 兜底逻辑：全池被间隔限制时，选最早恢复者，不 503（和 failureWindow 同哲学）
- 默认 `"0"` = 关闭（零回归）；建议 CN 配 `"1500ms"`、global 配 `"3s"`
- 字段热生效（SetMinRequestInterval），改配置无需重启

### 5. 清理现有污染（运维操作）

从 `D:\yg-gateway\data\state.json` 删掉了 `269f9704` 账号上的 `nonexistent-model-xyz` 冷却条目。

**注意**：网关运行时内存里还有旧状态，下次 Flush 会覆盖磁盘文件。需要重启网关才生效。

## 验证

- `go build ./internal/upstream/ ./internal/pool/ ./internal/server/ ./cmd/server/` 全部编译通过
- `go test ./internal/upstream/ ./internal/pool/ ./internal/server/` 全部测试通过

## 回滚

改动 1：恢复 `headers.go` 里 `injectAccountStableHeaders` 的两行（Machine-ID 和 Session-ID 都用 `deriveAccountStableID`）。
改动 2：恢复 `handler.go` 的 ErrModelBlocked 分支，去掉 `if h.isKnownModel(model)` 包裹。
改动 3：恢复 `scripts/yg-regression-test.py` 到 git HEAD。
改动 4：删掉 config.go 里加的风险注释段落。
改动 5：重启网关即可重新加载 state.json。
