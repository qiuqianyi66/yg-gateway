# 变更：P1 面板鉴权机制化 + 代理 client 构造去重（P1-3 / P1-2）

日期：2026-11-04
基线：`6bef0d2`（P0-2）
状态：已实施，未部署（仅提交）

## 改了什么

### P1-3 面板鉴权组合拳（机制化）

审计结论：规划假设的 cookie/HttpOnly/BREACH 攻击面**前提不成立**——面板是
**Bearer 鉴权**（`withAuth` → `authenticateAdmin`，`internal/panel/panel.go`），
只认单 api_key（Live 优先回落静态），不认多 Key；空 key 多 Key 模式 fail-closed；
`httpauth.VerifyBearer` 常量时间比较（R5）。CSRF 天然免疫（跨站不携带自定义
Authorization 头）。因此不引入 cookie 方案。

真实缺口（机制性）：42 个 API 端点全部**手工** `mux.HandleFunc(..., p.withAuth(h))`，
新增端点漏包 = 管理端点裸奔（可读写配置、禁用/移除账号、重置密钥）。

落地：
- 新增 `p.api(method, path, h)` 私有注册方法，内部**强制** `p.withAuth(h)`；
  全部 42 个 API 端点改走 `p.api`，静态资源（前端壳/JS/CSS，21 条）保留裸注册
  （路径不在 `/panel/api/` 下）。
- 新增 `TestPanelAPIMethodRequiresAuth`：无 token → 401 且不达 handler；管理员
  token → 200 达 handler。牙齿：`api()` 内部换回裸 `h`，第一条断言立即红。

### P1-2 clientFor / chatClientFor 去重

- 抽私有构造器 `clientForProxy(proxyURL, timeout, cacheKey)`：双检锁 + proxyEff
  登记下沉一处；`clientFor`（120s 总时长，key=proxyURL）、`chatClientFor`
  （0=无总时长 SSE，key="chat:"+proxyURL）变薄，直连兜底语义不变。
- 新增 `TestProxyCacheKeyIsolation`：同一 proxyURL 的普通/chat client 不同实例
  （key 隔离），各命中缓存不重复构造。

### P1-1 min_request_interval 默认值 —— 留档（未设值）

规划标注「待校准参数（requires 观测账号请求分布）」。当前无运行观测数据，
按仓库纪律「未知不签发」**不编默认值**：保持现状（空/0 → 关闭，配置归一已有
`ratelimit_config_test.go` 钉住）。需要观测后定值：跑一段真实请求分布，按
单账号实际 RPM 分位校准；可对照 magpie 60s+1s 滑动窗量级。

### 伴随：depth 门禁回退修复（非 -Update 掩盖）

`api()` 迁移使 `panel.go` 达 1002 行，触发 `check-depth.ps1` oversizeFiles
2→3。将 `routes()` / `api()` / `ServeHTTP` 拆到新文件 `internal/panel/routes.go`
→ panel.go 回到 981 行，oversizeFiles 恢复 2。

## 怎么验证的

- `go test ./internal/upstream ./internal/panel -count=1`：upstream ok / panel ok
- `go test ./... -count=1`：30 包全绿（改动后全量）
- `go vet ./...`：无输出
- `gofmt -l ./cmd ./internal`：零输出
- `check-boundaries.ps1`：✅ 通过
- `check-depth.ps1`：✅ 未回退（oversizeFiles=2，覆盖率 74%）

## 回滚

- `git revert` 本提交即可：`api()` 恢复为裸注册的等价行为需确认——回滚后
  `routes.go` 删除、`panel.go` 恢复原 42 行手工 `withAuth` 包裹（鉴权行为不变）。
- 无配置、无外部副作用、无持久状态变化。

## 已知留档（未修）

- `internal/upstream/client.go:680,709` 单路径 404/405 耗尽路径撞
  `panic("unreachable: chatPaths is never empty")`（沿用 P0-3 记录，测试用 400 规避）。
- P1-1 默认间隔值待观测校准（见上）。
