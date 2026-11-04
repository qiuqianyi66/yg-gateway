# 显式代理 fail-closed + 11102 空目录 e2e 补测

日期：2026-11-04

## 背景

magpie 全盘 REA（docs/reference/magpie-rea-full-2026-11-03.md）对照 yg 三条 ban-risk 后，
第 ④ 节（出口面）核心结论：**显式代理 = fail-closed**——代理失败不静默回退直连，
dial 失败冒泡报错驱动换号。查当前 HEAD 发现 yg 正是相反的 fail-open：
`internal/upstream/transport.go` 三处失败分支（URL 解析 / SOCKS5 初始化 / 不支持 scheme）
打 WARN 后**返回直连 Transport**——显式配代理的账号在代理配错时走共享出口 IP，
多账号风控关联重新出现，且不报错（最危险形态）。

上一轮 4fb5639 的 summary 把"自动回退直连"写成了既定行为（第 71 行），本轮修正该语义。

同时补 #12 的 e2e 缺口：11102 三值判定的 chatrun 分支（目录空 → 继续换号）此前零覆盖。

## 改动清单

### 1. 代理失败 fail-closed（P0-1，#11）

**文件**：`internal/upstream/transport.go`、`internal/upstream/transport_failclosed_test.go`（新）

**问题**：代理配置失败（URL 解析失败 / SOCKS5 初始化失败 / 不支持 scheme）时静默回退直连。
直连会让多账号共用一个出口 IP，per-account 代理想切断的风控关联重新出现，且成功响应不报错。

**改法**：
- 新增哨兵 `errProxyConfig`（`proxy config error (fail-closed)`）+ `failDial(cause)`：
  失败时把 Transport 的 DialContext 替换为必然返回 cause 的 dialer——请求错误冒泡 →
  chatrun 网络层抖动分支（chatrun.go:683-695）→ rotateBackoff 换号。
- 三处失败分支 WARN 日志改文案（标注 fail-closed），不再返回直连。
- 直连路径（`proxyURL == ""`）不受影响，仍是唯一合法直连来源。
- 错误消息经 redactProxyURL 脱敏（R1/R2：代理 URL 可含 userinfo）。

**效果**：显式代理配错 = 该账号出站失败（可观测、可排查），不再静默共享出口 IP。
HTTP CONNECT / SOCKS5 连接不可达本就是 dial 期错误冒泡，本次不改（已 fail-closed）。

### 2. 11102 空目录 e2e 补测（P0-3，#12）

**文件**：`internal/server/models_11102_rotate_test.go`（新）

**问题**：11102 三值判定逻辑已实现（chatrun.go:813-828：仅 modelCatalogAbsent fail-fast，
Unknown 继续换号），isKnownModel 三值只有函数级单测（models_catalog_state_test.go），
"目录为空 + 11102 → 继续换号"的 chatrun 端到端分支此前零覆盖（handler_test.go 无 11102 用例）。

**改法**：新增端到端测试——11102 号先被选中（积分更高）→ 目录空（modelCatalogUnknown）→
断言不 400、换到健康号 200、两号各尝试一次。

**牙齿**：把 chatrun.go:815 的 modelCatalogAbsent 判据误扩到 Unknown（目录空），
第一条断言（不 400）立即红。

## 验证

- `go test ./... -count=1`：30 包全绿（含新测试 TestProxyConfigFailClosed /
  TestProxyDialFailureSurfaces / TestChat11102UnknownCatalogRotates）
- `go vet ./...`：无输出
- `gofmt -l ./cmd ./internal`：零输出
- `check-boundaries.ps1`：通过
- `check-depth.ps1`：未回退（覆盖率 74% 保持）

## 回滚

- 改动 1：恢复 transport.go 三处失败分支为"WARN + return tr（直连）"（git revert 本提交即达）。
  语义级：若需临时放行，可把三处 `failDial(...)` 换回 `return tr`。
- 改动 2：删除 models_11102_rotate_test.go（纯测试，无生产影响）。

## 观察（未修，记录）

`internal/upstream/client.go:680`：ChatStreamContext 对 404/405 走"换路径重试"（continue），
但 #119 后 CN/global 聊天域均单路径——单路径下 404/405 会耗尽路径列表走到 :709
`panic("unreachable: chatPaths is never empty")`。该分支是历史遗留死路（注释自述"chatPaths
恒非空由构造保证"，但 404 continue 会消费完路径）。真实上游聊天端点 404/405 罕见，暂不修；
若后续出现，应把"404/405 且无下一路径"改为返回 4xx 错误而非 panic。
